package probe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testInput(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "vod", "ad", "seg_000.ts")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}
	return path
}

// TestDuration_RealFile cross-checks against ffprobe's own reported
// duration (10.031011s, verified independently via
// `ffprobe -show_entries format=duration` on this exact checked-in file)
// so this isn't just testing that libavformat agrees with itself.
func TestDuration_RealFile(t *testing.T) {
	got, err := Duration(testInput(t))
	if err != nil {
		t.Fatalf("Duration() error = %v", err)
	}
	want := 10031011 * time.Microsecond
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 5*time.Millisecond {
		t.Errorf("Duration() = %v, want ~%v (within 5ms)", got, want)
	}
}

func TestDuration_NonexistentFile(t *testing.T) {
	_, err := Duration(filepath.Join(t.TempDir(), "does-not-exist.ts"))
	if err == nil {
		t.Fatal("Duration() error = nil, want error for a nonexistent file")
	}
}

func TestDuration_NotAMediaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-media.txt")
	if err := os.WriteFile(path, []byte("this is not a video file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Duration(path)
	if err == nil {
		t.Fatal("Duration() error = nil, want error for a non-media file")
	}
}

func TestKeyframes_RealFile(t *testing.T) {
	keyframes, err := Keyframes(testInput(t))
	if err != nil {
		t.Fatalf("Keyframes() error = %v", err)
	}
	if len(keyframes) == 0 {
		t.Fatal("Keyframes() returned none, want at least one")
	}

	// This checked-in MPEG-TS segment's video PTS genuinely does not start
	// at 0 — independently confirmed via `ffprobe -show_entries
	// stream=start_time` on this exact file, which reports the same
	// 1.466667s. That's a real, unremarkable property of MPEG-TS muxing
	// (a PCR/base-offset, not a defect in this file or this package), and
	// asserting "near 0" here would have been the test's bug, not the
	// probe's — this was caught by running Keyframes against a real file
	// rather than a synthetic one with a convenient zero start.
	want := 1466667 * time.Microsecond
	diff := keyframes[0].PTS - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 2*time.Millisecond {
		t.Errorf("first keyframe PTS = %v, want ~%v (within 2ms, per ffprobe)", keyframes[0].PTS, want)
	}

	// PTS values must be non-decreasing.
	for i := 1; i < len(keyframes); i++ {
		if keyframes[i].PTS < keyframes[i-1].PTS {
			t.Errorf("keyframe %d PTS %v is before keyframe %d PTS %v", i, keyframes[i].PTS, i-1, keyframes[i-1].PTS)
		}
	}
}

// TestVideo_RealFile cross-checks against ffprobe's own reported values
// (width=640, height=360, format bit_rate=537517 — verified independently
// via `ffprobe -show_entries stream=width,height,bit_rate` and
// `-show_entries format=bit_rate` on this exact checked-in file). This
// file's per-stream bit_rate is itself unset ("N/A" per ffprobe) — real
// MPEG-TS segments commonly don't carry one — which is exactly why Video
// falls back to the container-level bitrate rather than reporting 0.
func TestVideo_RealFile(t *testing.T) {
	got, err := Video(testInput(t))
	if err != nil {
		t.Fatalf("Video() error = %v", err)
	}
	if got.Width != 640 || got.Height != 360 {
		t.Errorf("Video() dimensions = %dx%d, want 640x360", got.Width, got.Height)
	}
	wantKbps := 537517 / 1000
	diff := got.BitrateKbps - wantKbps
	if diff < 0 {
		diff = -diff
	}
	if diff > 2 {
		t.Errorf("Video().BitrateKbps = %d, want ~%d (within 2kbps, per ffprobe's container-level bit_rate)", got.BitrateKbps, wantKbps)
	}
}

func TestVideo_NonexistentFile(t *testing.T) {
	_, err := Video(filepath.Join(t.TempDir(), "does-not-exist.ts"))
	if err == nil {
		t.Fatal("Video() error = nil, want error for a nonexistent file")
	}
}

func TestKeyframes_NonexistentFile(t *testing.T) {
	_, err := Keyframes(filepath.Join(t.TempDir(), "does-not-exist.ts"))
	if err == nil {
		t.Fatal("Keyframes() error = nil, want error for a nonexistent file")
	}
}
