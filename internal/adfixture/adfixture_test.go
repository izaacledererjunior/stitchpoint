package adfixture

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izaacledererjunior/stitchpoint/internal/vast"
)

func testCreative(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("checked-in fixture creative not found (%v); run from repo root", err)
	}
	return path
}

func TestNew_RequiresCreativePath(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error for missing CreativePath, got nil")
	}
}

func TestNew_RejectsMissingCreativeFile(t *testing.T) {
	if _, err := New(Config{CreativePath: "/does/not/exist.mp4"}); err == nil {
		t.Fatal("expected error for nonexistent creative file, got nil")
	}
}

func TestHandleHealthz(t *testing.T) {
	srv, err := New(Config{CreativePath: testCreative(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
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

func TestHandleVAST_WellFormedAndCorrectShape(t *testing.T) {
	srv, err := New(Config{CreativePath: testCreative(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/vast")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("Content-Type = %q, want an xml type", ct)
	}
	if got := resp.Header.Get("X-Stitchpoint-Ad-Decision"); got != "fixture" {
		t.Errorf("X-Stitchpoint-Ad-Decision = %q, want %q", got, "fixture")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	// The response must root every URL at this test server's own host —
	// proves baseURLFor's request-derived reflection works, which is what
	// makes the same binary correct behind an arbitrary cloud domain
	// without a rebuild.
	if !strings.Contains(string(body), ts.URL+"/creative.mp4") {
		t.Errorf("VAST response doesn't reference %s/creative.mp4:\n%s", ts.URL, body)
	}
	if !strings.Contains(string(body), "omid") {
		t.Error("VAST response missing AdVerifications/OMID block")
	}
	if !strings.Contains(string(body), "Prebid Server") {
		t.Error("VAST response missing the fixture disclosure explaining where a real ad decision would go")
	}
}

// TestHandleVAST_ParsesWithProjectsOwnClient is the important test: it
// feeds this fixture's own response through internal/vast — the actual
// client stitchpoint uses in production — rather than just asserting the
// XML is well-formed in isolation. This is what proves the fixture is a
// genuine stand-in for a real VAST endpoint, not just XML that happens to
// parse.
func TestHandleVAST_ParsesWithProjectsOwnClient(t *testing.T) {
	srv, err := New(Config{CreativePath: testCreative(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resolved, err := vast.Fetch(ts.Client(), ts.URL+"/vast")
	if err != nil {
		t.Fatalf("internal/vast.Fetch failed against adfixture's own response: %v", err)
	}

	mf, ok := resolved.SelectMediaFile()
	if !ok {
		t.Fatal("SelectMediaFile found nothing usable in the fixture's own VAST response")
	}
	if mf.URL != ts.URL+"/creative.mp4" {
		t.Errorf("MediaFile.URL = %q, want %q", mf.URL, ts.URL+"/creative.mp4")
	}
	if mf.Width != DefaultConfig.CreativeWidth || mf.Height != DefaultConfig.CreativeHeight {
		t.Errorf("MediaFile dimensions = %dx%d, want %dx%d", mf.Width, mf.Height, DefaultConfig.CreativeWidth, DefaultConfig.CreativeHeight)
	}
	if resolved.Duration != DefaultConfig.CreativeDuration {
		t.Errorf("Duration = %v, want %v", resolved.Duration, DefaultConfig.CreativeDuration)
	}
}

func TestHandleCreative_ServesRealFile(t *testing.T) {
	creativePath := testCreative(t)
	want, err := os.ReadFile(creativePath)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{CreativePath: creativePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/creative.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Errorf("served creative is %d bytes, want %d", len(got), len(want))
	}
}

func TestHandleTrack_ReturnsNoContent(t *testing.T) {
	srv, err := New(Config{CreativePath: testCreative(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/track?event=complete")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestBaseURLFor_ConfigOverrideWins(t *testing.T) {
	srv, err := New(Config{CreativePath: testCreative(t), BaseURL: "https://fixture.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/vast")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "https://fixture.example.com/creative.mp4") {
		t.Errorf("configured BaseURL was not used; body:\n%s", body)
	}
	if strings.Contains(string(body), ts.URL) {
		t.Errorf("response leaked the test server's actual host despite a configured BaseURL:\n%s", body)
	}
}
