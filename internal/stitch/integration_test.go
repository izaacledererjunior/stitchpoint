package stitch

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
)

// TestSplice_RealTestStream is Phase 2's end-to-end proof: it runs Splice
// against the checked-in, self-authored VOD test assets
// (testdata/vod/content, testdata/vod/ad — see README "Test assets"),
// materializes the result to a real directory of files, and confirms
// FFmpeg can decode the stitched stream start to finish.
//
// FFmpeg completing with exit 0 confirms the manifest and segments are
// structurally valid and fully decodable — it does not by itself confirm
// a *visually clean* splice (no frame tear, no A/V desync at the
// boundary); that's what the README's required proof-artifact recording
// in a real player is for. FFmpeg's stderr at the splice point does
// include "non monotonically increasing dts" warnings, which is expected:
// the content and ad clips were encoded in separate FFmpeg sessions, so
// their internal timestamps don't share a base. That's exactly what
// #EXT-X-DISCONTINUITY exists to tell a real player — reset your
// timeline here rather than expecting continuity — so this test doesn't
// treat that specific warning as a failure, only a hard decode error
// (non-zero exit) is.
func TestSplice_RealTestStream(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH, skipping decode verification")
	}

	contentPath := filepath.Join("..", "..", "testdata", "vod", "content", "content.m3u8")
	adPath := filepath.Join("..", "..", "testdata", "vod", "ad", "ad.m3u8")

	content := mustParsePlaylist(t, contentPath)
	ad := mustParsePlaylist(t, adPath)

	out, err := Splice(content, ad)
	if err != nil {
		t.Fatalf("Splice() error = %v", err)
	}
	if len(out.Segments) != len(content.Segments) {
		t.Fatalf("got %d segments, want %d (ad duration matches break exactly, segment count should be unchanged)", len(out.Segments), len(content.Segments))
	}

	dir := t.TempDir()
	contentDir := filepath.Dir(contentPath)
	adDir := filepath.Dir(adPath)
	contentURIs := make(map[string]bool, len(content.Segments))
	for _, s := range content.Segments {
		contentURIs[s.URI] = true
	}
	for _, s := range out.Segments {
		src := filepath.Join(adDir, s.URI)
		if contentURIs[s.URI] {
			src = filepath.Join(contentDir, s.URI)
		}
		if err := copyFileForTest(src, filepath.Join(dir, s.URI)); err != nil {
			t.Fatalf("copying %s: %v", s.URI, err)
		}
	}

	manifestPath := filepath.Join(dir, "stitched.m3u8")
	mf, err := os.Create(manifestPath)
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	if err := manifest.Write(mf, out); err != nil {
		mf.Close()
		t.Fatalf("write manifest: %v", err)
	}
	mf.Close()

	cmd := exec.Command(ffmpeg, "-v", "error", "-i", manifestPath, "-f", "null", "-")
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg failed to decode stitched stream: %v\n%s", err, stderr)
	}
	t.Logf("ffmpeg stderr (benign DTS-ordering warnings at the discontinuity are expected):\n%s", stderr)
}

func mustParsePlaylist(t *testing.T, path string) *manifest.Playlist {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v (see README Test assets to regenerate)", path, err)
	}
	defer f.Close()
	p, err := manifest.Parse(f)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return p
}

func copyFileForTest(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
