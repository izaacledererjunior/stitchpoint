package abrbench

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRun_RealEncode is a real (not mocked) multi-rung FFmpeg run against
// checked-in test media, using a two-rung ladder to keep test time
// reasonable. Skipped if ffmpeg isn't in PATH.
func TestRun_RealEncode(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	input := filepath.Join("..", "..", "testdata", "vod", "ad", "seg_000.ts")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}

	ladder := []Rung{
		{Name: "240p", Width: 426, Height: 240, VideoBitrateKbps: 300, AudioBitrateKbps: 64},
		{Name: "480p", Width: 854, Height: 480, VideoBitrateKbps: 1000, AudioBitrateKbps: 96},
	}

	results, err := Run(input, t.TempDir(), ladder)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != len(ladder) {
		t.Fatalf("got %d results, want %d", len(results), len(ladder))
	}

	for i, r := range results {
		if r.Rung.Name != ladder[i].Name {
			t.Errorf("results[%d].Rung.Name = %q, want %q", i, r.Rung.Name, ladder[i].Name)
		}
		if _, err := os.Stat(r.OutputPath); err != nil {
			t.Errorf("results[%d].OutputPath %q does not exist: %v", i, r.OutputPath, err)
		}
		if r.FileSizeBytes <= 0 {
			t.Errorf("results[%d].FileSizeBytes = %d, want > 0", i, r.FileSizeBytes)
		}
		if r.Duration <= 0 {
			t.Errorf("results[%d].Duration = %v, want > 0", i, r.Duration)
		}
		if r.ActualBitrateKbps <= 0 {
			t.Errorf("results[%d].ActualBitrateKbps = %v, want > 0", i, r.ActualBitrateKbps)
		}
		// A sanity bound, not a precise assertion: libx264's -maxrate/-bufsize
		// keeps actual output within a reasonable band of the target, but
		// exact tracking isn't guaranteed for a ~10s clip (rate control
		// needs some runway) — this just catches "wildly wrong", e.g. a
		// units bug (kbps vs bps) that would be off by 1000x.
		target := float64(r.TargetBitrateKbps())
		if r.ActualBitrateKbps < target*0.3 || r.ActualBitrateKbps > target*3 {
			t.Errorf("results[%d].ActualBitrateKbps = %v, target = %v — implausibly far off (unit bug?)", i, r.ActualBitrateKbps, target)
		}
	}
}

func TestResult_DeltaPercent(t *testing.T) {
	r := Result{Rung: Rung{VideoBitrateKbps: 800, AudioBitrateKbps: 96}, ActualBitrateKbps: 896}
	if got, want := r.DeltaPercent(), 0.0; got != want {
		t.Errorf("DeltaPercent() = %v, want %v (exact match)", got, want)
	}

	over := Result{Rung: Rung{VideoBitrateKbps: 800, AudioBitrateKbps: 96}, ActualBitrateKbps: 1075.2}
	if got, want := over.DeltaPercent(), 20.0; got < want-0.01 || got > want+0.01 {
		t.Errorf("DeltaPercent() = %v, want ~%v", got, want)
	}
}

func TestRun_FFmpegNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty PATH: ffmpeg definitely not found
	_, err := Run("irrelevant.mp4", t.TempDir(), DefaultLadder)
	if err == nil {
		t.Fatal("Run() error = nil, want error when ffmpeg is not in PATH")
	}
}
