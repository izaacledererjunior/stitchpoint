// Package live is real dynamic SSAI for a live channel, not a static VOD
// asset (see ADR 0003): it polls an upstream live HLS manifest, detects
// #EXT-X-CUE-OUT breaks as they appear, resolves a VAST ad in the
// background, and splices it into a continuously-updated output window.
// Genuinely different from internal/server's VOD path: the matching
// content doesn't exist yet when a break starts (live splicing works
// from the signaled CueOutDuration, not real segments already in place),
// and ad resolution takes real wall-clock time a poll loop can't hide —
// this package's answer is fail-open (forward original content until the
// ad is ready, splice in mid-break, suppress until CUE-IN). One shared
// output window per channel; no per-viewer ad personalization (see
// README "Future ideas").
package live

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
	"github.com/izaacledererjunior/stitchpoint/internal/safego"
	"github.com/izaacledererjunior/stitchpoint/internal/transcode"
	"github.com/izaacledererjunior/stitchpoint/internal/vast"
)

// Config configures a Watcher.
type Config struct {
	// UpstreamURL is the live HLS media playlist to watch. Required.
	UpstreamURL string

	// DefaultVASTURL is used for every break — configured per-channel,
	// not per-viewer (there's one shared output window per channel).
	DefaultVASTURL string

	// DefaultBreakDuration is used when a #EXT-X-CUE-OUT tag carries no
	// duration. Defaults to 30s.
	DefaultBreakDuration time.Duration

	// PollInterval is how often the upstream manifest is re-fetched.
	// Defaults to 4s (real live segment durations are commonly ~4-6s).
	PollInterval time.Duration

	// WindowSize caps how many segments the served output window keeps.
	// Defaults to 10.
	WindowSize int

	// WorkDir is where downloaded creatives and encoded ad segments are
	// written, one subdirectory per break. Defaults to a temp dir.
	WorkDir string

	// EncodeParams is passed to transcode.EncodeHLS for each break's ad,
	// with MaxDuration overridden per-break to its signaled duration.
	EncodeParams transcode.Params

	// HTTPClient is used for VAST requests and creative downloads.
	// Defaults to a client with a 30s timeout.
	HTTPClient *http.Client

	// Filler covers the gap when the resolved ad runs shorter than the
	// break's signaled duration — see BreakFiller (fill.go). Defaults to
	// LoopFiller{}.
	Filler BreakFiller
}

func (c *Config) setDefaults() {
	if c.DefaultBreakDuration <= 0 {
		c.DefaultBreakDuration = 30 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 4 * time.Second
	}
	if c.WindowSize <= 0 {
		c.WindowSize = 10
	}
	if c.WorkDir == "" {
		c.WorkDir = filepath.Join(os.TempDir(), "stitchpoint-live")
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Filler == nil {
		c.Filler = LoopFiller{}
	}
	if c.EncodeParams.Width == 0 {
		c.EncodeParams = transcode.DefaultParams
	}
}

// breakState tracks the ad break currently in progress, if any. gen
// increments every time a new break starts, so a slow async ad-resolution
// goroutine from an earlier (already-ended) break can recognize it's
// stale and discard its result instead of splicing into the wrong break.
type breakState struct {
	active         bool
	gen            int
	targetDuration time.Duration

	adReady    bool
	adSpliced  bool
	adSegments []manifest.Segment // URIs already rewritten to this break's serving path
}

// Watcher polls UpstreamURL and maintains the live, ad-stitched output
// window served to viewers. Create with New; it starts polling
// immediately in the background.
type Watcher struct {
	cfg Config

	mu             sync.Mutex
	output         manifest.Playlist
	nextSeq        int  // absolute media sequence of the next upstream segment to process
	haveInitialSeq bool // false until the first poll establishes nextSeq
	br             breakState

	done chan struct{}
}

// New creates a Watcher and starts it polling UpstreamURL in the
// background. Call Close to stop.
func New(cfg Config) (*Watcher, error) {
	cfg.setDefaults()
	if cfg.UpstreamURL == "" {
		return nil, fmt.Errorf("live: UpstreamURL is required")
	}
	if _, err := url.Parse(cfg.UpstreamURL); err != nil {
		return nil, fmt.Errorf("live: invalid UpstreamURL: %w", err)
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o750); err != nil {
		return nil, err
	}

	// Fetch/parse once synchronously to fail fast on an obviously wrong
	// UpstreamURL — otherwise New "succeeds" and the same failure just
	// retries forever inside the background poll loop with no signal to
	// the caller. Result discarded either way; poll() re-fetches on tick 1.
	body, err := fetch(cfg.HTTPClient, cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("live: fetching upstream manifest: %w", err)
	}
	if _, err := manifest.Parse(strings.NewReader(body)); err != nil {
		return nil, fmt.Errorf("live: parsing upstream manifest: %w", err)
	}

	w := &Watcher{cfg: cfg, done: make(chan struct{})}
	w.output.Version = 3
	w.output.TargetDuration = 10

	go w.run()
	return w, nil
}

// Close stops the background poller. It does not stop an in-flight ad
// resolution goroutine; that goroutine notices via its captured
// generation number and discards its result once it finishes.
func (w *Watcher) Close() {
	close(w.done)
}

// CurrentManifest returns a snapshot of the live output window, safe to
// serialize with manifest.Write. Callers must not mutate it.
func (w *Watcher) CurrentManifest() *manifest.Playlist {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.output
	out.Segments = append([]manifest.Segment(nil), w.output.Segments...)
	return &out
}

// AdSegmentPath resolves a break generation + filename (as referenced by
// URIs CurrentManifest returns for ad segments — see adSegmentURI) to a
// local file path, for the HTTP handler to serve. ok is false if the
// break generation is unknown.
func (w *Watcher) AdSegmentPath(gen, filename string) (path string, ok bool) {
	g, err := strconv.Atoi(gen)
	if err != nil || g < 0 {
		return "", false
	}
	p := filepath.Join(w.cfg.WorkDir, fmt.Sprintf("b%d", g), filepath.Base(filename))
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

func (w *Watcher) run() {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	w.safePoll() // don't wait a full interval before the first fetch
	for {
		select {
		case <-ticker.C:
			w.safePoll()
		case <-w.done:
			return
		}
	}
}

// safePoll runs poll with panic recovery, so a bug tripped by one bad
// upstream response degrades that single tick, not the whole channel.
func (w *Watcher) safePoll() {
	defer safego.Recover("live.watcher.poll")
	w.poll()
}

// poll fetches the upstream manifest once, processes whatever segments
// weren't already seen, and updates the served output window.
func (w *Watcher) poll() {
	body, err := fetch(w.cfg.HTTPClient, w.cfg.UpstreamURL)
	if err != nil {
		slog.Error("live: fetching upstream manifest", "err", err)
		return
	}
	upstream, err := manifest.Parse(strings.NewReader(body))
	if err != nil {
		slog.Error("live: parsing upstream manifest", "err", err)
		return
	}
	base, err := url.Parse(w.cfg.UpstreamURL)
	if err != nil {
		return // already validated in New; unreachable in practice
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.haveInitialSeq {
		// Join at the live edge: skip everything already in the first
		// window we see rather than dumping a backlog into the output.
		w.nextSeq = upstream.MediaSequence + len(upstream.Segments)
		w.haveInitialSeq = true
		w.output.MediaSequence = w.nextSeq
		return
	}

	for i, s := range upstream.Segments {
		absSeq := upstream.MediaSequence + i
		if absSeq < w.nextSeq {
			continue // already processed in an earlier poll
		}
		w.processSegment(s, base)
		w.nextSeq = absSeq + 1
	}

	w.trimWindow()
}

// processSegment applies one newly-seen upstream segment to the break
// state machine and/or the output window. See the package doc for the
// overall fail-open design; this is that state machine.
func (w *Watcher) processSegment(s manifest.Segment, base *url.URL) {
	resolved := s
	resolved.URI = resolveURI(base, s.URI)

	switch {
	case s.CueOut && !w.br.active:
		target := time.Duration(s.CueOutDuration * float64(time.Second))
		if target <= 0 {
			target = w.cfg.DefaultBreakDuration
		}
		w.br = breakState{active: true, gen: w.br.gen + 1, targetDuration: target}
		slog.Info("live: break started, resolving ad in the background", "gen", w.br.gen, "segment", s.URI, "targetDuration", target)
		go w.resolveAd(w.br.gen, target)

		// Fail open: the ad can't possibly be ready this same tick (it
		// requires at least one network round trip), so this segment is
		// always forwarded as original content.
		resolved.CueOut, resolved.CueIn = false, false
		w.output.Segments = append(w.output.Segments, resolved)

	case w.br.active && !w.br.adSpliced:
		if w.br.adReady {
			slog.Info("live: ad is ready, splicing into the output window now", "gen", w.br.gen, "segment", s.URI)
			w.spliceAd()
			// This upstream segment is original break content being
			// replaced by the ad; don't forward it.
		} else {
			slog.Info("live: ad not ready yet, forwarding original content (fail open)", "gen", w.br.gen, "segment", s.URI)
			resolved.CueOut, resolved.CueIn = false, false
			w.output.Segments = append(w.output.Segments, resolved)
		}
		if s.CueIn {
			w.endBreak(resolved)
		}

	case w.br.active && w.br.adSpliced:
		// Ad already delivered for this break; suppress further original
		// break content until the upstream signals it's over.
		if s.CueIn {
			w.endBreak(resolved)
		}

	default:
		resolved.CueOut, resolved.CueIn = false, false
		w.output.Segments = append(w.output.Segments, resolved)
	}
}

// endBreak resumes normal passthrough at resumeSegment, marking a
// discontinuity only if an ad actually played (a decoder reset is only
// needed coming out of a different bitstream).
func (w *Watcher) endBreak(resumeSegment manifest.Segment) {
	resumeSegment.CueOut, resumeSegment.CueIn = false, false
	if w.br.adSpliced {
		resumeSegment.Discontinuity = true
		slog.Info("live: break ended — an ad was spliced in", "gen", w.br.gen, "resumeSegment", resumeSegment.URI)
	} else {
		slog.Info("live: break ended — no ad was ready in time; played fail-open the whole way through", "gen", w.br.gen, "resumeSegment", resumeSegment.URI)
	}
	w.output.Segments = append(w.output.Segments, resumeSegment)
	w.br = breakState{}
}

// spliceAd appends the current break's resolved ad segments to the output
// window, marking the transition with a discontinuity. Caller must hold
// w.mu and must have already checked w.br.adReady && !w.br.adSpliced.
func (w *Watcher) spliceAd() {
	segs := append([]manifest.Segment(nil), w.br.adSegments...)
	if len(segs) > 0 {
		segs[0].Discontinuity = true
	}
	w.output.Segments = append(w.output.Segments, segs...)
	w.br.adSpliced = true
}

// trimWindow drops segments from the front once the window exceeds
// WindowSize, advancing MediaSequence to match — the same sliding-window
// mechanics a real live playlist uses. Caller must hold w.mu.
func (w *Watcher) trimWindow() {
	over := len(w.output.Segments) - w.cfg.WindowSize
	if over <= 0 {
		return
	}
	w.output.Segments = w.output.Segments[over:]
	w.output.MediaSequence += over
}

// resolveAd runs the VAST → download → transcode pipeline for a break in
// the background (so it never blocks poll, which has to keep running
// regardless of how long ad resolution takes) and, if the result is still
// relevant (gen matches — the break hasn't already ended), stores it for
// the next poll to splice in.
func (w *Watcher) resolveAd(gen int, target time.Duration) {
	// A panic here just means this break never gets adReady=true — the
	// existing fail-open path in processSegment already handles that
	// exactly like a slow/failed resolution, so no extra cleanup needed.
	defer safego.Recover("live.watcher.resolveAd")

	vastURL := w.cfg.DefaultVASTURL
	if vastURL == "" {
		slog.Info("live: no VAST URL configured, skipping ad resolution (will fail open)", "gen", gen)
		return
	}

	slog.Info("live: requesting VAST tag", "gen", gen, "vastURL", vastURL)
	resolved, err := vast.Fetch(w.cfg.HTTPClient, vastURL)
	if err != nil {
		if errors.Is(err, vast.ErrNoFill) {
			slog.Info("live: VAST no-fill, giving up (will fail open)", "gen", gen, "err", err)
		} else {
			slog.Error("live: VAST request failed (not a no-fill — a real fetch/parse error)", "gen", gen, "err", err)
		}
		return
	}
	slog.Info("live: VAST filled", "gen", gen, "adTitle", resolved.AdTitle, "adSystem", resolved.AdSystem)

	mediaFile, ok := resolved.SelectMediaFile()
	if !ok {
		slog.Warn("live: VAST ad has no usable progressive MP4 MediaFile", "gen", gen)
		return
	}
	slog.Info("live: selected creative", "gen", gen, "url", mediaFile.URL, "width", mediaFile.Width, "height", mediaFile.Height, "bitrateKbps", mediaFile.Bitrate)

	breakDir := filepath.Join(w.cfg.WorkDir, fmt.Sprintf("b%d", gen))
	if err := os.MkdirAll(breakDir, 0o750); err != nil {
		slog.Error("live: creating work dir", "gen", gen, "err", err)
		return
	}
	creativePath := filepath.Join(breakDir, "creative.mp4")
	slog.Info("live: downloading creative...", "gen", gen)
	if err := transcode.DownloadFile(w.cfg.HTTPClient, mediaFile.URL, creativePath); err != nil {
		slog.Error("live: downloading creative FAILED", "gen", gen, "err", err)
		return
	}

	slog.Info("live: encoding creative...", "gen", gen, "targetDuration", target)
	params := w.cfg.EncodeParams
	params.MaxDuration = target
	ad, err := transcode.EncodeHLS(creativePath, breakDir, params)
	if err != nil {
		slog.Error("live: encoding creative FAILED", "gen", gen, "err", err)
		return
	}
	segs := ad.Segments
	if actual := time.Duration(ad.TotalDuration() * float64(time.Second)); actual < target {
		slog.Warn("live: ad underfilled the break — covering the gap with filler", "gen", gen, "actual", actual, "target", target, "filler", fmt.Sprintf("%T", w.cfg.Filler))
		segs = w.cfg.Filler.Fill(segs, actual, target)
	}

	for i := range segs {
		segs[i].URI = adSegmentURI(gen, segs[i].URI)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.br.gen != gen || !w.br.active {
		slog.Info("live: ad resolved after the break already ended (or a new one started); discarding", "gen", gen)
		return
	}
	w.br.adSegments = segs
	w.br.adReady = true
	slog.Info("live: ad ready", "gen", gen, "segments", len(segs), "durationSeconds", sumSegmentDuration(segs).Seconds())
}

func adSegmentURI(gen int, filename string) string {
	return fmt.Sprintf("/live/ads/%d/%s", gen, filename)
}

func resolveURI(base *url.URL, uri string) string {
	ref, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return base.ResolveReference(ref).String()
}

func fetch(client *http.Client, rawURL string) (string, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
