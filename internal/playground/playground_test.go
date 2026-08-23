package playground

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/adfixture"
)

// newTestVASTServer starts a real internal/adfixture server (see that
// package's tests for why this is preferred over a hand-rolled VAST
// fixture: it proves this package interoperates with the project's own
// realistic, spec-shaped VAST responses, not a minimal stub).
func newTestVASTServer(t *testing.T) *httptest.Server {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	creative := filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4")
	if _, err := os.Stat(creative); err != nil {
		t.Skipf("test creative not found: %v (see README Test assets)", err)
	}
	srv, err := adfixture.New(adfixture.Config{CreativePath: creative})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(srv.Handler())
}

func waitForJob(t *testing.T, ts *httptest.Server, id string) Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/jobs/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var job Job
		if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if job.Status == StatusReady || job.Status == StatusFailed {
			return job
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("job did not finish within the test deadline")
	return Job{}
}

func TestNew_RequiresVASTURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New() error = nil, want error for missing VASTURL")
	}
}

// TestServer_Demo_EndToEnd is the no-upload path: POST /api/demo against
// the checked-in, already-cued test content, resolved against a real
// (test) VAST server, all the way to a fetchable, correctly-structured
// result.
func TestServer_Healthz(t *testing.T) {
	srv, err := New(Config{VASTURL: "http://example.invalid/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServer_Demo_EndToEnd(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	demoContent := filepath.Join("..", "..", "testdata", "vod", "content", "content.m3u8")
	if _, err := os.Stat(demoContent); err != nil {
		t.Skipf("demo content not found: %v", err)
	}

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", DemoContentPath: demoContent, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/demo status = %d, want 202", resp.StatusCode)
	}
	var created Job
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	job := waitForJob(t, ts, created.ID)
	if job.Status != StatusReady {
		t.Fatalf("job status = %s, want ready (error: %s)", job.Status, job.Error)
	}

	mResp, err := http.Get(ts.URL + "/api/jobs/" + created.ID + "/stitched.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer mResp.Body.Close()
	body, _ := io.ReadAll(mResp.Body)
	if !strings.Contains(string(body), "#EXT-X-DISCONTINUITY") {
		t.Errorf("stitched manifest missing #EXT-X-DISCONTINUITY (ad wasn't spliced in?):\n%s", body)
	}
}

// TestServer_Upload_EndToEnd is the real feature this package exists
// for: an arbitrary uploaded video plus a caller-chosen ad-break time,
// through contentprep.InjectBreak and the same splice pipeline the demo
// path uses.
func TestServer_Upload_EndToEnd(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created := postUpload(t, ts, filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4"), "2", "2")
	job := waitForJob(t, ts, created.ID)
	if job.Status != StatusReady {
		t.Fatalf("job status = %s, want ready (error: %s)", job.Status, job.Error)
	}

	resp, err := http.Get(ts.URL + "/api/jobs/" + created.ID + "/stitched.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "c0_seg_000.ts") || !strings.Contains(string(body), "post_seg_000.ts") {
		t.Errorf("stitched manifest missing expected before/after segments:\n%s", body)
	}
	if !strings.Contains(string(body), "ad_seg_000.ts") {
		t.Errorf("stitched manifest missing the spliced-in ad segment:\n%s", body)
	}

	segResp, err := http.Get(ts.URL + "/api/jobs/" + created.ID + "/ad_seg_000.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = segResp.Body.Close() }()
	if segResp.StatusCode != http.StatusOK {
		t.Errorf("fetching a real segment file: status = %d, want 200", segResp.StatusCode)
	}
}

func TestServer_Upload_RejectsTooLongVideo(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	// The checked-in creative is 6s; a 1s cap guarantees rejection.
	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir(), MaxUploadDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created := postUpload(t, ts, filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4"), "0", "1")
	job := waitForJob(t, ts, created.ID)
	if job.Status != StatusFailed {
		t.Fatalf("job status = %s, want failed (upload exceeds MaxUploadDuration)", job.Status)
	}
	if !strings.Contains(job.Error, "limit") {
		t.Errorf("job.Error = %q, want it to mention the duration limit", job.Error)
	}
}

func TestServer_CreateJob_MissingVideoField(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/jobs", "application/x-www-form-urlencoded", strings.NewReader("break_start=0&break_duration=1"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a request with no video file", resp.StatusCode)
	}
}

func TestServer_CORSHeaders(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/jobs/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q (default)", got, "*")
	}

	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/api/jobs", nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = preflight.Body.Close() }()
	if preflight.StatusCode != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want 204", preflight.StatusCode)
	}
}

func TestServer_CORSHeaders_CustomOrigin(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir(), AllowedOrigin: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/jobs/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the configured origin", got)
	}
}

func TestServer_GetJob_NotFound(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/jobs/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestServer_Demo_DisabledWithoutConfig(t *testing.T) {
	vastSrv := newTestVASTServer(t)
	defer vastSrv.Close()

	srv, err := New(Config{VASTURL: vastSrv.URL + "/vast", WorkDir: t.TempDir(), DemoContentPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when DemoContentPath is unset", resp.StatusCode)
	}
}

// postUpload builds and sends a real multipart upload, the same shape a
// browser's FormData would produce.
func postUpload(t *testing.T, ts *httptest.Server, videoPath, breakStart, breakDuration string) Job {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	f, err := os.Open(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	part, err := w.CreateFormFile("video", filepath.Base(videoPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, f); err != nil {
		t.Fatal(err)
	}
	w.WriteField("break_start", breakStart)
	w.WriteField("break_duration", breakDuration)
	w.Close()

	resp, err := http.Post(ts.URL+"/api/jobs", w.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/jobs status = %d, want 202: %s", resp.StatusCode, body)
	}
	var job Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	return job
}
