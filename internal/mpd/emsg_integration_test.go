package mpd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractEmsgCues_RealSegment reads the checked-in
// testdata/dash/content/chunk-stream0-00001-with-emsg.m4s — a real,
// hand-packed 'emsg' box (README's own -segment CLI example uses this
// exact file) prepended to a genuine FFmpeg-produced fMP4 media segment
// — and confirms ExtractEmsgCues finds the cue and walks straight past
// the segment's real moof/mdat boxes without needing to understand
// their contents. This exercises real ISOBMFF structure end to end, not
// just the synthetic single-box streams emsg_test.go otherwise uses,
// and doubles as a regression check on the checked-in fixture itself
// (see README "Test assets" for how it was generated).
func TestExtractEmsgCues_RealSegment(t *testing.T) {
	segPath := filepath.Join("..", "..", "testdata", "dash", "content", "chunk-stream0-00001-with-emsg.m4s")
	f, err := os.Open(segPath)
	if err != nil {
		t.Skipf("test segment not found: %v (see README Test assets)", err)
	}
	defer f.Close()

	cues, err := ExtractEmsgCues(f)
	if err != nil {
		t.Fatalf("ExtractEmsgCues() error = %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	if cues[0].ID != 100 || cues[0].Version != 1 || cues[0].SpliceInfoSection == nil {
		t.Errorf("cue = %+v, want ID=100 Version=1 with a decoded SpliceInfoSection", cues[0])
	}
}
