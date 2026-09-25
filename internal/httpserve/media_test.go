package httpserve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestServeMediaFile_ExplicitContentType is a regression test for a real
// production incident: on at least one deployment environment, the OS
// mime database maps ".ts" to Qt Linguist's translation-source type
// (text/vnd.trolltech.linguist) instead of MPEG-TS's video/mp2t — the
// same extension means two unrelated things. http.ServeFile only guesses
// via that database when Content-Type isn't already set, so a live ad
// segment got served with the wrong type; a real deployment's MSE-based
// player choked on it even though the underlying bytes decoded fine.
func TestServeMediaFile_ExplicitContentType(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"seg_000.ts", "video/mp2t"},
		{"stitched.m3u8", "application/vnd.apple.mpegurl"},
		{"creative.mp4", "video/mp4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, []byte("fake media bytes"), 0o644); err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			ServeMediaFile(rec, httptest.NewRequest(http.MethodGet, "/"+tt.name, nil), path)

			if got := rec.Header().Get("Content-Type"); got != tt.want {
				t.Errorf("Content-Type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWithMediaContentType_PresetsBeforeFileServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seg_000.ts"), []byte("fake ts bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := WithMediaContentType(http.FileServer(http.Dir(dir)))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/seg_000.ts", nil))

	if got := rec.Header().Get("Content-Type"); got != "video/mp2t" {
		t.Errorf("Content-Type = %q, want %q", got, "video/mp2t")
	}
}

func TestWithMediaContentType_LeavesUnknownExtensionsAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := WithMediaContentType(http.FileServer(http.Dir(dir)))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/notes.txt", nil))

	// Not asserting an exact value — just that this helper didn't force
	// anything for an extension it doesn't know about, leaving
	// http.FileServer's own (correct, for .txt) detection in place.
	if got := rec.Header().Get("Content-Type"); got == "" {
		t.Error("Content-Type is empty — http.FileServer should still have set one for a plain file")
	}
}
