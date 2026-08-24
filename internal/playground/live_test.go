package playground

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLive_CreateGetStop(t *testing.T) {
	allowLocalUpstream(t)
	upstream := newStaticUpstream(t, []string{"seg_000.ts", "seg_001.ts"})
	defer upstream.Close()
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created := postLive(t, ts, upstream.URL+"/live.m3u8")
	if created.Status != LiveStatusRunning {
		t.Fatalf("initial status = %s, want running", created.Status)
	}

	resp, err := http.Get(ts.URL + "/api/live/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got LiveSession
	json.NewDecoder(resp.Body).Decode(&got)
	if got.UpstreamURL != upstream.URL+"/live.m3u8" {
		t.Errorf("UpstreamURL = %q, want %q", got.UpstreamURL, upstream.URL+"/live.m3u8")
	}

	// The manifest endpoint must actually work while running.
	mResp, err := http.Get(ts.URL + "/api/live/" + created.ID + "/stitched.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	mResp.Body.Close()
	if mResp.StatusCode != http.StatusOK {
		t.Errorf("GET stitched.m3u8 while running: status = %d, want 200", mResp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/live/"+created.ID, nil)
	delResp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", delResp.StatusCode)
	}

	after := getLive(t, ts, created.ID)
	if after.Status != LiveStatusStopped {
		t.Errorf("status after DELETE = %s, want stopped", after.Status)
	}

	// A stopped session's manifest endpoint should 404, not serve a
	// stale/frozen window from a watcher that no longer exists.
	mResp2, err := http.Get(ts.URL + "/api/live/" + created.ID + "/stitched.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	mResp2.Body.Close()
	if mResp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET stitched.m3u8 after stop: status = %d, want 404", mResp2.StatusCode)
	}
}

// TestLive_SplicesRealAdAndRewritesSegmentURIs is the important test:
// forces a real ad break through a real live.Watcher (real VAST fetch,
// real FFmpeg encode, via a simulated upstream that advances the same
// way internal/live's own integration test does), then confirms this
// package's manifest rewrite actually produces a fetchable,
// session-scoped ad segment URL — not just that internal/live spliced
// something internally.
func TestLive_SplicesRealAdAndRewritesSegmentURIs(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	allowLocalUpstream(t)

	upstream := newSequencedUpstream(t)
	defer upstream.Close()
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created := postLive(t, ts, upstream.URL+"/live.m3u8")

	deadline := time.Now().Add(30 * time.Second)
	var manifestBody string
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/live/" + created.ID + "/stitched.m3u8")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		manifestBody = string(body)
		if strings.Contains(manifestBody, "/api/live/"+created.ID+"/live/ads/") {
			break
		}
		upstream.advance()
		time.Sleep(150 * time.Millisecond)
	}

	if !strings.Contains(manifestBody, "/api/live/"+created.ID+"/live/ads/") {
		t.Fatalf("manifest never contained a session-scoped ad segment URL within the deadline:\n%s", manifestBody)
	}
	if strings.Contains(manifestBody, `"/live/ads/`) || strings.Contains(manifestBody, "\n/live/ads/") {
		t.Errorf("manifest leaked live.Watcher's unscoped /live/ads/ path instead of the rewritten one:\n%s", manifestBody)
	}

	// Extract the rewritten URI and confirm it's actually fetchable —
	// the whole point of the rewrite is that a player can follow it.
	var adPath string
	for _, line := range strings.Split(manifestBody, "\n") {
		if strings.HasPrefix(line, "/api/live/"+created.ID+"/live/ads/") {
			adPath = line
			break
		}
	}
	if adPath == "" {
		t.Fatal("could not find the rewritten ad segment line in the manifest")
	}
	segResp, err := http.Get(ts.URL + adPath)
	if err != nil {
		t.Fatal(err)
	}
	defer segResp.Body.Close()
	if segResp.StatusCode != http.StatusOK {
		t.Errorf("fetching rewritten ad segment %q: status = %d, want 200", adPath, segResp.StatusCode)
	}
}

func TestLive_RejectsNonPublicUpstream(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()
	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, upstream := range []string{
		"http://127.0.0.1:9/live.m3u8",
		"http://localhost/live.m3u8",
		"ftp://example.com/live.m3u8",
		"not-a-url",
	} {
		resp, err := http.PostForm(ts.URL+"/api/live", map[string][]string{"upstream": {upstream}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("upstream=%q: status = %d, want 400", upstream, resp.StatusCode)
		}
	}
}

func TestLive_MaxConcurrentLive(t *testing.T) {
	allowLocalUpstream(t)
	up1 := newStaticUpstream(t, []string{"seg_000.ts"})
	defer up1.Close()
	up2 := newStaticUpstream(t, []string{"seg_000.ts"})
	defer up2.Close()
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir(), MaxConcurrentLive: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	postLive(t, ts, up1.URL+"/live.m3u8")

	resp, err := http.PostForm(ts.URL+"/api/live", map[string][]string{"upstream": {up2.URL + "/live.m3u8"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second live session status = %d, want 429 (MaxConcurrentLive=1)", resp.StatusCode)
	}
}

// TestLive_MaxConcurrentLive_StoppedSlotReusable is a regression test: the
// concurrency check must count only running sessions. stopLiveSession
// deliberately leaves the stopped entry in s.liveSessions (so a client
// asking later still sees "stopped" — see its doc comment), so counting
// every map entry instead of just LiveStatusRunning ones would make this
// a lifetime-sessions-ever-created cap, permanently refusing new sessions
// in production once that many had ever been created, even with zero
// actually running.
func TestLive_MaxConcurrentLive_StoppedSlotReusable(t *testing.T) {
	allowLocalUpstream(t)
	up1 := newStaticUpstream(t, []string{"seg_000.ts"})
	defer up1.Close()
	up2 := newStaticUpstream(t, []string{"seg_000.ts"})
	defer up2.Close()
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir(), MaxConcurrentLive: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	first := postLive(t, ts, up1.URL+"/live.m3u8")

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/live/"+first.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The first slot is stopped, not removed — a second session must
	// still be allowed to start, since only one is actually running.
	postLive(t, ts, up2.URL+"/live.m3u8")
}

func TestLive_GetUnknown(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()
	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/live/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// --- test helpers ---

func postLive(t *testing.T, ts *httptest.Server, upstream string) LiveSession {
	t.Helper()
	resp, err := http.PostForm(ts.URL+"/api/live", map[string][]string{"upstream": {upstream}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/live status = %d, want 202: %s", resp.StatusCode, body)
	}
	var session LiveSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	return session
}

func getLive(t *testing.T, ts *httptest.Server, id string) LiveSession {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/live/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var session LiveSession
	json.NewDecoder(resp.Body).Decode(&session)
	return session
}

// newStaticUpstream serves a fixed live-style HLS media playlist (no
// cue, plain passthrough) plus tiny real segment files, for tests that
// only need a valid, pollable upstream and don't need to exercise an ad
// break.
func newStaticUpstream(t *testing.T, segments []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "#EXTM3U")
		fmt.Fprintln(w, "#EXT-X-VERSION:3")
		fmt.Fprintln(w, "#EXT-X-TARGETDURATION:2")
		fmt.Fprintln(w, "#EXT-X-MEDIA-SEQUENCE:0")
		for _, s := range segments {
			fmt.Fprintln(w, "#EXTINF:2.000000,")
			fmt.Fprintln(w, s)
		}
	})
	for _, s := range segments {
		mux.HandleFunc("/"+s, func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("fake-segment-data"))
		})
	}
	return httptest.NewServer(mux)
}

// sequencedUpstream is a minimal live-upstream simulator: starts with a
// CUE-OUT segment already present (so a break begins on the watcher's
// first poll), and each call to advance() appends one more plain filler
// segment — the same "keep advancing with filler until the real ad is
// confirmed spliced" technique internal/live's own integration test
// uses, reimplemented minimally here since test helpers aren't
// importable across packages.
type sequencedUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	extra int
}

func (u *sequencedUpstream) advance() {
	u.mu.Lock()
	u.extra++
	u.mu.Unlock()
}

func newSequencedUpstream(t *testing.T) *sequencedUpstream {
	t.Helper()
	u := &sequencedUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		extra := u.extra
		u.mu.Unlock()

		// internal/live.Watcher joins at the live edge on its very first
		// poll — whatever's already in that response is treated as
		// historical backlog, never replayed (see internal/live's
		// package doc). So the CUE-OUT segment can't be present on poll
		// #1 (extra==0) or the watcher would skip right past it the same
		// way; it only appears once advance() has been called at least
		// once, exactly mirroring internal/live's own integration test
		// technique for the same reason.
		fmt.Fprintln(w, "#EXTM3U")
		fmt.Fprintln(w, "#EXT-X-VERSION:3")
		fmt.Fprintln(w, "#EXT-X-TARGETDURATION:2")
		fmt.Fprintln(w, "#EXT-X-MEDIA-SEQUENCE:0")
		fmt.Fprintln(w, "#EXTINF:2.000000,")
		fmt.Fprintln(w, "seg_000.ts")
		if extra >= 1 {
			fmt.Fprintln(w, "#EXT-X-CUE-OUT:2")
			fmt.Fprintln(w, "#EXTINF:2.000000,")
			fmt.Fprintln(w, "seg_001.ts")
		}
		for i := 0; i < extra-1; i++ {
			fmt.Fprintln(w, "#EXTINF:2.000000,")
			fmt.Fprintf(w, "seg_filler_%03d.ts\n", i)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".ts") {
			w.Write([]byte("fake-segment-data"))
			return
		}
		http.NotFound(w, r)
	})
	u.Server = httptest.NewServer(mux)
	return u
}

// allowLocalUpstream swaps in a permissive upstream validator for the
// duration of one test — see validateUpstreamURL's doc comment for why
// this is a package-level var swap rather than a config flag.
func allowLocalUpstream(t *testing.T) {
	t.Helper()
	prev := validateUpstreamURL
	validateUpstreamURL = func(string) error { return nil }
	t.Cleanup(func() { validateUpstreamURL = prev })
}
