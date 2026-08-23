// Package server implements stitchpoint's dynamic SSAI mode: an HTTP
// server running the VAST → download → transcode → splice pipeline live,
// per playback session (unlike `stitchpoint stitch`, which produces one
// static manifest ahead of time). One content asset per server instance;
// the VAST tag varies per request (?vast=, falling back to a configured
// default). A VAST no-fill returns 204 rather than falling back to
// content-only playback, so the failure is visible during testing — point
// -vast at cmd/vastfixture (ADR 0004) for a no-fill-free local demo.
package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crypto/rand"
	"encoding/hex"

	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
	"github.com/izaacledererjunior/stitchpoint/internal/safego"
	"github.com/izaacledererjunior/stitchpoint/internal/ssrf"
	"github.com/izaacledererjunior/stitchpoint/internal/stitch"
	"github.com/izaacledererjunior/stitchpoint/internal/transcode"
	"github.com/izaacledererjunior/stitchpoint/internal/vast"
)

// Config configures a Server.
type Config struct {
	// ContentPath is the local HLS media playlist (with
	// #EXT-X-CUE-OUT/#EXT-X-CUE-IN already present) to splice ads into.
	// Required.
	ContentPath string

	// DefaultVASTURL is used when a request doesn't supply its own
	// ?vast= query parameter. May be empty if every request is expected
	// to supply one.
	DefaultVASTURL string

	// SessionDir is the base directory session output (downloaded
	// creative, encoded ad segments, stitched manifest) is written under,
	// one subdirectory per session. Defaults to
	// os.TempDir()/stitchpoint-sessions.
	SessionDir string

	// SessionTTL is how long a session's files are kept before the
	// janitor removes them. Defaults to 30 minutes.
	SessionTTL time.Duration

	// JanitorInterval is how often expired sessions are swept. Defaults
	// to 5 minutes. Exposed mainly so tests can use a short interval
	// instead of waiting on the production default.
	JanitorInterval time.Duration

	// HTTPClient is used for VAST requests and creative downloads.
	// Defaults to a client with a 30s timeout.
	HTTPClient *http.Client

	// MaxConcurrentSessions bounds how many VAST → download → transcode →
	// splice pipelines run at once; further requests block until a slot
	// frees up rather than piling up unbounded concurrent FFmpeg
	// processes. Defaults to 4.
	MaxConcurrentSessions int

	// RateLimitPerMinute caps how many /vod/manifest requests (the
	// endpoint that triggers the full pipeline) a single client IP may
	// make per minute; further ones get 429. Doesn't apply to segment or
	// content serving, which a real playback session hits repeatedly by
	// design. Defaults to 20.
	RateLimitPerMinute int
}

func (c *Config) setDefaults() {
	if c.SessionDir == "" {
		c.SessionDir = filepath.Join(os.TempDir(), "stitchpoint-sessions")
	}
	if c.SessionTTL <= 0 {
		c.SessionTTL = 30 * time.Minute
	}
	if c.JanitorInterval <= 0 {
		c.JanitorInterval = 5 * time.Minute
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.MaxConcurrentSessions <= 0 {
		c.MaxConcurrentSessions = 4
	}
	if c.RateLimitPerMinute <= 0 {
		c.RateLimitPerMinute = 20
	}
}

// Server is stitchpoint's dynamic SSAI HTTP handler. Create with New.
type Server struct {
	cfg        Config
	content    *manifest.Playlist
	contentDir string
	mux        *http.ServeMux

	sem         chan struct{} // bounds MaxConcurrentSessions
	rateLimiter *httpserve.RateLimiter

	mu       sync.Mutex
	sessions map[string]sessionInfo

	janitorDone chan struct{}
}

type sessionInfo struct {
	dir       string
	createdAt time.Time
}

// New builds a Server for the given Config, parsing ContentPath once up
// front and starting the background session janitor.
func New(cfg Config) (*Server, error) {
	cfg.setDefaults()

	f, err := os.Open(cfg.ContentPath)
	if err != nil {
		return nil, fmt.Errorf("server: opening content playlist: %w", err)
	}
	defer func() { _ = f.Close() }()
	content, err := manifest.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("server: parsing content playlist: %w", err)
	}

	if err := os.MkdirAll(cfg.SessionDir, 0o750); err != nil {
		return nil, fmt.Errorf("server: creating session dir: %w", err)
	}

	s := &Server{
		cfg:         cfg,
		content:     content,
		contentDir:  filepath.Dir(cfg.ContentPath),
		sem:         make(chan struct{}, cfg.MaxConcurrentSessions),
		rateLimiter: httpserve.NewRateLimiter(cfg.RateLimitPerMinute, time.Minute),
		sessions:    make(map[string]sessionInfo),
		janitorDone: make(chan struct{}),
	}

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /healthz", httpserve.Healthz)
	s.mux.Handle("GET /vod/manifest", s.rateLimiter.Middleware(http.HandlerFunc(s.handleStartSession)))
	s.mux.Handle("GET /content/", http.StripPrefix("/content/", http.FileServer(httpserve.NoDirListing(http.Dir(s.contentDir)))))
	s.mux.HandleFunc("GET /sessions/{id}/{file}", s.handleSessionFile)

	go s.runJanitor()

	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpserve.SecurityHeaders(s.mux).ServeHTTP(w, r)
}

// Close stops the background janitor and the rate limiter's sweep. It
// does not remove existing session directories or close in-flight
// requests.
func (s *Server) Close() {
	close(s.janitorDone)
	s.rateLimiter.Close()
}

// removeSessionDir cleans up a session's directory on an error path,
// logging rather than swallowing a failure to actually remove it.
func removeSessionDir(sessionDir string) {
	if err := os.RemoveAll(sessionDir); err != nil {
		slog.Error("server: removing session dir", "dir", sessionDir, "err", err)
	}
}

// validateVASTURL is a package-level var (not a direct call) so this
// package's own tests can swap in a permissive stand-in to point
// ?vast= at an httptest.Server (127.0.0.1, which the real check
// correctly rejects) — no production config can disable it.
var validateVASTURL = ssrf.ValidatePublicHTTPURL

// handleStartSession runs the full VAST → download → transcode → splice
// pipeline for a brand new session — a fresh ad decision on every call.
func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	vastURL := r.URL.Query().Get("vast")
	if vastURL != "" {
		// Caller-supplied: this server fetches it (and any VAST Wrapper
		// redirect chain it points to), so it's validated against SSRF
		// before ever being used. DefaultVASTURL below is
		// operator-configured at startup, not caller-reachable, so it's
		// trusted as-is — same split internal/playground's -upstream vs.
		// fixed VASTURL makes.
		if err := validateVASTURL(vastURL); err != nil {
			http.Error(w, "invalid vast: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		vastURL = s.cfg.DefaultVASTURL
	}
	if vastURL == "" {
		http.Error(w, "no VAST tag: pass ?vast=<url> or configure -vast at startup", http.StatusBadRequest)
		return
	}

	// Bound concurrent VAST->download->transcode->splice pipelines rather
	// than letting each request spawn its own unbounded FFmpeg process;
	// excess requests queue here instead of piling onto the host.
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	id, err := newSessionID()
	if err != nil {
		http.Error(w, "generating session id: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sessionDir := filepath.Join(s.cfg.SessionDir, id)
	if err := os.MkdirAll(sessionDir, 0o750); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ad, err := s.resolveAndEncodeAd(vastURL, sessionDir)
	if err != nil {
		removeSessionDir(sessionDir)
		if errors.Is(err, vast.ErrNoFill) {
			// 204 has no body; the reason goes in a header instead.
			w.Header().Set("X-Stitchpoint-Ad-Decision", "no-fill")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		slog.Error("server: ad pipeline failed", "session", id, "err", err)
		http.Error(w, "ad pipeline failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	out, err := s.spliceForSession(ad)
	if err != nil {
		removeSessionDir(sessionDir)
		slog.Error("server: splice failed", "session", id, "err", err)
		http.Error(w, "splice failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	manifestPath := filepath.Join(sessionDir, "stitched.m3u8")
	mf, err := os.Create(manifestPath)
	if err != nil {
		removeSessionDir(sessionDir)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeErr := manifest.Write(mf, out)
	closeErr := mf.Close()
	if writeErr != nil {
		removeSessionDir(sessionDir)
		http.Error(w, writeErr.Error(), http.StatusInternalServerError)
		return
	}
	if closeErr != nil {
		removeSessionDir(sessionDir)
		http.Error(w, closeErr.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.sessions[id] = sessionInfo{dir: sessionDir, createdAt: time.Now()}
	s.mu.Unlock()

	http.Redirect(w, r, "/sessions/"+id+"/stitched.m3u8", http.StatusFound)
}

// resolveAndEncodeAd fetches the VAST tag, downloads the selected
// creative, and encodes it to match the content — writing everything
// directly into sessionDir so the ad's segments need no further copying.
func (s *Server) resolveAndEncodeAd(vastURL, sessionDir string) (ad *manifest.Playlist, err error) {
	resolved, ferr := vast.Fetch(s.cfg.HTTPClient, vastURL)
	if ferr != nil {
		return nil, ferr
	}

	mediaFile, ok := resolved.SelectMediaFile()
	if !ok {
		return nil, fmt.Errorf("VAST ad %q (%s) has no usable progressive MP4 MediaFile", resolved.AdTitle, resolved.AdSystem)
	}

	creativePath := filepath.Join(sessionDir, "creative.mp4")
	if err := transcode.DownloadFile(s.cfg.HTTPClient, mediaFile.URL, creativePath); err != nil {
		return nil, fmt.Errorf("downloading creative: %w", err)
	}

	ad, err = transcode.EncodeHLS(creativePath, sessionDir, transcode.DefaultParams)
	if err != nil {
		return nil, err
	}

	// transcode.EncodeHLS names segments generically ("seg_000.ts", ...),
	// which can collide with the content's own names; spliceForSession
	// tells them apart by URI, so rename to remove the ambiguity.
	if err := renamePrefixed(sessionDir, ad, "ad_"); err != nil {
		return nil, fmt.Errorf("renaming ad segments: %w", err)
	}
	return ad, nil
}

// renamePrefixed renames every segment file pl references, in dir, to
// carry prefix, updating pl's URIs to match.
func renamePrefixed(dir string, pl *manifest.Playlist, prefix string) error {
	for i, seg := range pl.Segments {
		if strings.HasPrefix(seg.URI, prefix) {
			continue
		}
		newURI := prefix + seg.URI
		if err := os.Rename(filepath.Join(dir, seg.URI), filepath.Join(dir, newURI)); err != nil {
			return err
		}
		pl.Segments[i].URI = newURI
	}
	return nil
}

// spliceForSession splices ad into the server's content and rewrites
// content-origin segment URIs to the shared /content/ path; ad-origin
// URIs are left as bare filenames (already written into sessionDir).
func (s *Server) spliceForSession(ad *manifest.Playlist) (*manifest.Playlist, error) {
	out, err := stitch.SpliceWithOptions(s.content, ad, stitch.Options{AllowDurationMismatch: true})
	if err != nil {
		return nil, err
	}

	contentURIs := make(map[string]bool, len(s.content.Segments))
	for _, seg := range s.content.Segments {
		contentURIs[seg.URI] = true
	}
	for i, seg := range out.Segments {
		if contentURIs[seg.URI] {
			out.Segments[i].URI = "/content/" + seg.URI
		}
	}
	return out, nil
}

// handleSessionFile serves a session's manifest or ad segment files.
// filepath.Base strips any directory components from the requested file
// name, so this can't be used to read outside the session's own
// (intentionally flat) directory.
func (s *Server) handleSessionFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	file := r.PathValue("file")

	s.mu.Lock()
	info, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, filepath.Join(info.dir, filepath.Base(file)))
}

// runJanitor periodically removes session directories older than
// SessionTTL, so a long-running server doesn't accumulate downloaded
// creatives and encoded ad segments forever.
func (s *Server) runJanitor() {
	ticker := time.NewTicker(s.cfg.JanitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.safeSweepExpiredSessions()
		case <-s.janitorDone:
			return
		}
	}
}

// safeSweepExpiredSessions runs sweepExpiredSessions with panic recovery,
// so a bug tripped by one sweep degrades that tick, not the janitor loop.
func (s *Server) safeSweepExpiredSessions() {
	defer safego.Recover("server.janitor.sweep")
	s.sweepExpiredSessions()
}

func (s *Server) sweepExpiredSessions() {
	cutoff := time.Now().Add(-s.cfg.SessionTTL)

	s.mu.Lock()
	var expired []string
	for id, info := range s.sessions {
		if info.createdAt.Before(cutoff) {
			expired = append(expired, id)
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()

	for _, id := range expired {
		removeSessionDir(filepath.Join(s.cfg.SessionDir, id))
	}
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
