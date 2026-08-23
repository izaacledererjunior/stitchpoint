package server

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

// newTestVASTFixture serves a VAST InLine ad whose MediaFile points back
// at a real, tiny MP4 (re-muxed from checked-in test content) on the same
// httptest server, so the full download+encode pipeline runs against real
// bytes rather than a mock.
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

func newTestServer(t *testing.T, defaultVAST string) *Server {
	t.Helper()
	contentPath := filepath.Join("..", "..", "testdata", "vod", "content", "content.m3u8")
	if _, err := os.Stat(contentPath); err != nil {
		t.Skipf("test content not found: %v (see README Test assets)", err)
	}

	srv, err := New(Config{
		ContentPath:     contentPath,
		DefaultVASTURL:  defaultVAST,
		SessionDir:      t.TempDir(),
		JanitorInterval: time.Hour, // tests control sweeping explicitly where needed
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestServer_DynamicSession_EndToEnd(t *testing.T) {
	withPermissiveVASTURLCheck(t)
	vastSrv := newTestVASTFixture(t)
	defer vastSrv.Close()

	srv := newTestServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/vod/manifest?vast="+vastSrv.URL+"/vast.xml", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("GET /vod/manifest status = %d, want %d; body=%s", rec.Code, http.StatusFound, rec.Body)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/sessions/") || !strings.HasSuffix(location, "/stitched.m3u8") {
		t.Fatalf("Location = %q, want /sessions/<id>/stitched.m3u8", location)
	}

	// Fetch the session manifest.
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, location, nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", location, rec2.Code)
	}
	body := rec2.Body.String()

	if !strings.Contains(body, "#EXT-X-DISCONTINUITY") {
		t.Errorf("stitched manifest missing #EXT-X-DISCONTINUITY:\n%s", body)
	}
	if !strings.Contains(body, "/content/seg_000.ts") {
		t.Errorf("stitched manifest doesn't reference shared /content/ path:\n%s", body)
	}
	if strings.Contains(body, "/content/seg_003.ts") {
		t.Errorf("stitched manifest still references the replaced break segment:\n%s", body)
	}
	// Regression check: the transcoder names ad segments "seg_000.ts" etc,
	// which is exactly what this project's own test content also uses —
	// a real collision that once caused an ad segment to be misclassified
	// as content and served from the wrong (shared, non-session) path.
	if wantContentRefs := len(srv.content.Segments) - 1; strings.Count(body, "/content/") != wantContentRefs {
		t.Errorf("found %d /content/ references, want %d (one per surviving content segment): %s",
			strings.Count(body, "/content/"), wantContentRefs, body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "seg_") {
			t.Errorf("ad segment %q referenced by its unprefixed transcoder name — should be renamed (ad_ prefix) to avoid colliding with a same-named content segment:\n%s", line, body)
		}
	}

	// The shared content path serves the original segment...
	rec3 := httptest.NewRecorder()
	srv.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/content/seg_000.ts", nil))
	if rec3.Code != http.StatusOK || rec3.Body.Len() == 0 {
		t.Errorf("GET /content/seg_000.ts status = %d, len = %d, want 200 and non-empty", rec3.Code, rec3.Body.Len())
	}

	// ...and the session directory serves the ad's own segment(s), under
	// whatever renamed filename actually appears in the manifest.
	var adSegment string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "ad_") {
			adSegment = line
			break
		}
	}
	if adSegment == "" {
		t.Fatalf("no ad_-prefixed segment found in stitched manifest:\n%s", body)
	}
	sessionPath := strings.TrimSuffix(location, "stitched.m3u8")
	rec4 := httptest.NewRecorder()
	srv.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, sessionPath+adSegment, nil))
	if rec4.Code != http.StatusOK || rec4.Body.Len() == 0 {
		t.Errorf("GET %s%s status = %d, len = %d, want 200 and non-empty", sessionPath, adSegment, rec4.Code, rec4.Body.Len())
	}
}

func TestServer_NoFillReturns204(t *testing.T) {
	withPermissiveVASTURLCheck(t)
	noFillSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><VAST version="4.0"/>`)
	}))
	defer noFillSrv.Close()

	srv := newTestServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/vod/manifest?vast="+noFillSrv.URL, nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNoContent, rec.Body)
	}
	if got := rec.Header().Get("X-Stitchpoint-Ad-Decision"); got != "no-fill" {
		t.Errorf("X-Stitchpoint-Ad-Decision = %q, want %q", got, "no-fill")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 response has a body (%d bytes); a 204 must not", rec.Body.Len())
	}
}

func TestServer_MissingVASTIsBadRequest(t *testing.T) {
	srv := newTestServer(t, "") // no default configured, no ?vast= given

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vod/manifest", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestServer_Healthz(t *testing.T) {
	srv := newTestServer(t, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestServer_UnknownSessionIs404(t *testing.T) {
	srv := newTestServer(t, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sessions/does-not-exist/stitched.m3u8", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestServer_MaxConcurrentSessions_Blocks proves handleStartSession
// actually blocks on the concurrency semaphore rather than just
// configuring one that's never checked: fill the one slot manually, fire
// a real request, confirm it doesn't complete, then free the slot and
// confirm it does.
func TestServer_MaxConcurrentSessions_Blocks(t *testing.T) {
	contentPath := filepath.Join("..", "..", "testdata", "vod", "content", "content.m3u8")
	if _, err := os.Stat(contentPath); err != nil {
		t.Skipf("test content not found: %v (see README Test assets)", err)
	}

	srv, err := New(Config{
		ContentPath:           contentPath,
		DefaultVASTURL:        "http://example.invalid/vast.xml", // never actually reached while blocked
		SessionDir:            t.TempDir(),
		JanitorInterval:       time.Hour,
		MaxConcurrentSessions: 1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(srv.Close)

	srv.sem <- struct{}{} // occupy the only slot

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vod/manifest", nil))
		done <- rec.Code
	}()

	select {
	case <-done:
		t.Fatal("request completed while the concurrency slot was held — semaphore isn't blocking")
	case <-time.After(200 * time.Millisecond):
		// expected: still blocked
	}

	<-srv.sem // free the slot

	select {
	case <-done:
		// expected: proceeds now (fails downstream on the fake VAST URL,
		// which is fine — this test only asserts on blocking, not outcome)
	case <-time.After(5 * time.Second):
		t.Fatal("request never completed after the concurrency slot was freed")
	}
}

// withPermissiveVASTURLCheck swaps validateVASTURL for a no-op for the
// duration of t, so tests can point ?vast= at a local httptest.Server
// (127.0.0.1, which the real SSRF check correctly rejects) — see
// TestServer_RejectsNonPublicVASTURL for proof the real check is active
// by default.
func withPermissiveVASTURLCheck(t *testing.T) {
	t.Helper()
	prev := validateVASTURL
	validateVASTURL = func(string) error { return nil }
	t.Cleanup(func() { validateVASTURL = prev })
}

// TestServer_RejectsNonPublicVASTURL proves the real (non-overridden)
// SSRF check is active by default: a caller-supplied ?vast= pointing at
// a loopback address must be rejected before this server ever fetches it.
func TestServer_RejectsNonPublicVASTURL(t *testing.T) {
	srv := newTestServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/vod/manifest?vast=http://127.0.0.1:1/vast.xml", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body)
	}
}

func TestServer_ConcurrentSessionsGetDistinctIDs(t *testing.T) {
	vastSrv := newTestVASTFixture(t)
	defer vastSrv.Close()

	srv := newTestServer(t, vastSrv.URL+"/vast.xml")

	const n = 5
	var wg sync.WaitGroup
	locations := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vod/manifest", nil))
			if rec.Code != http.StatusFound {
				t.Errorf("request %d: status = %d, want %d; body=%s", i, rec.Code, http.StatusFound, rec.Body)
				return
			}
			locations[i] = rec.Header().Get("Location")
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i, loc := range locations {
		if loc == "" {
			t.Fatalf("request %d got no Location", i)
		}
		if seen[loc] {
			t.Errorf("duplicate session Location %q across concurrent requests", loc)
		}
		seen[loc] = true
	}
}

func TestServer_JanitorExpiresOldSessions(t *testing.T) {
	withPermissiveVASTURLCheck(t)
	vastSrv := newTestVASTFixture(t)
	defer vastSrv.Close()

	contentPath := filepath.Join("..", "..", "testdata", "vod", "content", "content.m3u8")
	if _, err := os.Stat(contentPath); err != nil {
		t.Skipf("test content not found: %v", err)
	}

	srv, err := New(Config{
		ContentPath:     contentPath,
		SessionDir:      t.TempDir(),
		SessionTTL:      50 * time.Millisecond,
		JanitorInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer srv.Close()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vod/manifest?vast="+vastSrv.URL+"/vast.xml", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusFound, rec.Body)
	}
	location := rec.Header().Get("Location")

	// Confirm it exists before expiry.
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, location, nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("session not immediately available: status = %d", rec2.Code)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec3 := httptest.NewRecorder()
		srv.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, location, nil))
		if rec3.Code == http.StatusNotFound {
			return // swept, as expected
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session was not expired by the janitor within the deadline")
}
