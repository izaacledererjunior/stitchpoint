package live

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestVASTFixture serves a VAST InLine ad whose MediaFile points at a
// real, tiny MP4 (re-muxed from checked-in test content) on the same
// httptest server — mirrors internal/server's test fixture, duplicated
// here since it's a private test helper there.
func newTestVASTFixture(t *testing.T) *httptest.Server {
	t.Helper()

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	srcSegment := filepath.Join("..", "..", "testdata", "vod", "ad", "seg_000.ts")
	if _, err := os.Stat(srcSegment); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}

	creativePath := filepath.Join(t.TempDir(), "creative.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", srcSegment, "-c:v", "libx264", "-c:a", "aac", creativePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building test creative: %v\n%s", err, out)
	}
	creativeBytes, err := os.ReadFile(creativePath)
	if err != nil {
		t.Fatalf("reading test creative: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/creative.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(creativeBytes)
	})
	srv := httptest.NewServer(nil)
	mux.HandleFunc("/vast.xml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<VAST version="3.0">
  <Ad id="1"><InLine>
    <AdSystem>Test</AdSystem><AdTitle>Test Ad</AdTitle>
    <Creatives><Creative><Linear>
      <Duration>00:00:10</Duration>
      <MediaFiles>
        <MediaFile delivery="progressive" type="video/mp4" width="640" height="360" bitrate="800"><![CDATA[%s/creative.mp4]]></MediaFile>
      </MediaFiles>
    </Linear></Creative></Creatives>
  </InLine></Ad>
</VAST>`, srv.URL)
	})
	srv.Config.Handler = mux
	return srv
}

// upstreamSim is a fake live upstream: an httptest server whose returned
// manifest depends on a test-controlled step counter, simulating a
// sliding live window advancing over time. Which segment indices (if any)
// carry #EXT-X-CUE-OUT/#EXT-X-CUE-IN is also test-controlled and can be
// set dynamically — tests that need to wait on a real background process
// (a real VAST fetch + real FFmpeg encode) before advancing to CUE-IN
// need that, rather than baking fixed indices in up front, or the
// simulated break can end before the real work behind it finishes.
type upstreamSim struct {
	mu          sync.Mutex
	step        int
	cueOutIndex int // -1 = none
	cueInIndex  int // -1 = none
	cueOutDur   float64
	srv         *httptest.Server
}

func newUpstreamSim(t *testing.T) *upstreamSim {
	u := &upstreamSim{cueOutIndex: -1, cueInIndex: -1}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		body := u.manifestFor(u.step)
		u.mu.Unlock()
		w.Write([]byte(body))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstreamSim) setStep(step int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.step = step
}

func (u *upstreamSim) setCueOut(index int, durationSeconds float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cueOutIndex = index
	u.cueOutDur = durationSeconds
}

func (u *upstreamSim) setCueIn(index int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cueInIndex = index
}

// manifestFor returns the live playlist window for a given step. Segment
// numbering is global (seg0, seg1, ...); each step's window is the last 4
// segments known at that step, with MEDIA-SEQUENCE set accordingly — real
// sliding-window behavior, not just an appending list. Caller must hold
// u.mu.
func (u *upstreamSim) manifestFor(step int) string {
	totalSegs := step + 4 // step 0 => segs 0..3, step N => segs 0..(N+3)
	windowStart := totalSegs - 4
	if windowStart < 0 {
		windowStart = 0
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n")
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", windowStart)
	for i := windowStart; i < totalSegs; i++ {
		if i == u.cueOutIndex {
			fmt.Fprintf(&b, "#EXT-X-CUE-OUT:%g\n", u.cueOutDur)
		}
		if i == u.cueInIndex {
			b.WriteString("#EXT-X-CUE-IN\n")
		}
		b.WriteString("#EXTINF:2.000000,\n")
		fmt.Fprintf(&b, "seg%d.ts\n", i)
	}
	return b.String()
}

func TestWatcher_JoinsAtLiveEdge(t *testing.T) {
	u := newUpstreamSim(t)
	u.setStep(0) // window: seg0..seg3

	w, err := New(Config{UpstreamURL: u.srv.URL, PollInterval: 50 * time.Millisecond, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer w.Close()

	time.Sleep(150 * time.Millisecond)
	out := w.CurrentManifest()
	if len(out.Segments) != 0 {
		t.Errorf("got %d segments after only the initial poll, want 0 (should join at the live edge, not replay the backlog): %+v", len(out.Segments), out.Segments)
	}
}

func TestWatcher_PassesThroughPlainSegments(t *testing.T) {
	u := newUpstreamSim(t)
	u.setStep(0)

	w, err := New(Config{UpstreamURL: u.srv.URL, PollInterval: 30 * time.Millisecond, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer w.Close()

	time.Sleep(100 * time.Millisecond) // establish initial sequence at seg0..3
	u.setStep(1)                       // adds seg4 (plain)
	time.Sleep(150 * time.Millisecond)

	out := w.CurrentManifest()
	if len(out.Segments) != 1 {
		t.Fatalf("got %d segments, want 1 (seg4 only): %+v", len(out.Segments), out.Segments)
	}
	if !strings.HasSuffix(out.Segments[0].URI, "/seg4.ts") {
		t.Errorf("Segments[0].URI = %q, want it to resolve to seg4.ts", out.Segments[0].URI)
	}
	if !strings.HasPrefix(out.Segments[0].URI, u.srv.URL) {
		t.Errorf("Segments[0].URI = %q, want it resolved to an absolute URL against the upstream", out.Segments[0].URI)
	}
}

// TestWatcher_SplicesRealAdIntoLiveBreak is the end-to-end proof: a real
// ad break appears in the simulated live upstream, a real VAST fixture
// resolves to a real creative, and the watcher's background goroutine
// downloads + transcodes it and splices it into the served window before
// the simulated #EXT-X-CUE-IN arrives — the core Phase 4 scenario: poll a
// live channel, catch the cue point, stitch in the ad while the manifest
// is live.
func TestWatcher_SplicesRealAdIntoLiveBreak(t *testing.T) {
	vastSrv := newTestVASTFixture(t)
	defer vastSrv.Close()

	u := newUpstreamSim(t)
	u.setStep(0)

	w, err := New(Config{
		UpstreamURL:    u.srv.URL,
		DefaultVASTURL: vastSrv.URL + "/vast.xml",
		PollInterval:   50 * time.Millisecond,
		WorkDir:        t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer w.Close()

	time.Sleep(120 * time.Millisecond) // establish initial sequence at seg0..3

	u.setCueOut(4, 2.0) // seg4 will carry CUE-OUT:2.0
	u.setStep(1)        // window now includes seg4 — break starts, ad resolution kicked off
	time.Sleep(100 * time.Millisecond)

	// Keep the live window advancing with plain filler segments (a real
	// live stream doesn't pause while an ad resolves) until the real
	// network fetch + real FFmpeg encode finish and the ad actually gets
	// spliced in. Only once that's confirmed do we introduce CUE-IN —
	// otherwise the simulated break could end before the real background
	// work behind it has had a chance to finish, which is exactly the
	// bug this structure exists to avoid (see upstreamSim's doc).
	deadline := time.Now().Add(5 * time.Second)
	step := 2
	adSpliced := false
	for time.Now().Before(deadline) {
		for _, s := range w.CurrentManifest().Segments {
			if strings.Contains(s.URI, "/live/ads/") {
				adSpliced = true
			}
		}
		if adSpliced {
			break
		}
		u.setStep(step)
		step++
		time.Sleep(80 * time.Millisecond)
	}
	if !adSpliced {
		t.Fatal("ad was never spliced into the live window within the deadline")
	}

	u.setCueIn(step + 3) // the next new segment the window will introduce
	u.setStep(step)
	time.Sleep(150 * time.Millisecond)

	out := w.CurrentManifest()
	if len(out.Segments) == 0 {
		t.Fatal("output window is empty")
	}

	var lastAdIdx = -1
	for i, s := range out.Segments {
		if strings.Contains(s.URI, "/live/ads/") {
			lastAdIdx = i
		}
	}
	if lastAdIdx == -1 {
		t.Fatalf("no ad segment found in final output: %+v", out.Segments)
	}

	// Exactly one segment should follow the ad: the CUE-IN resumption
	// segment, carrying a discontinuity (coming out of the ad's
	// bitstream) and referencing real upstream content, not another ad
	// segment or suppressed original break filler.
	if lastAdIdx != len(out.Segments)-2 {
		t.Fatalf("expected exactly one segment after the last ad segment (the resumption segment), got %d: %+v",
			len(out.Segments)-1-lastAdIdx, out.Segments)
	}
	resume := out.Segments[len(out.Segments)-1]
	if strings.Contains(resume.URI, "/live/ads/") {
		t.Errorf("segment after the ad is itself an ad segment, want real upstream content: %+v", resume)
	}
	if !resume.Discontinuity {
		t.Errorf("resumption segment should carry Discontinuity=true since a real ad was spliced: %+v", resume)
	}

	for _, s := range out.Segments {
		if s.CueOut || s.CueIn {
			t.Errorf("segment %+v still carries a cue marker in the served output — should be cleared once consumed", s)
		}
	}
}

func TestNew_RequiresUpstreamURL(t *testing.T) {
	_, err := New(Config{WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("New() error = nil, want error when UpstreamURL is empty")
	}
}

func TestNew_RejectsInvalidUpstreamURL(t *testing.T) {
	_, err := New(Config{UpstreamURL: "://not-a-url", WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("New() error = nil, want error for an invalid UpstreamURL")
	}
}
