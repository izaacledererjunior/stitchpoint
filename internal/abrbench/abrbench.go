// Package abrbench encodes one input video at each rung of an ABR ladder
// and reports how closely FFmpeg's actual output bitrate tracked the
// target. Scope is bitrate/size/time only — no perceptual quality metric
// (VMAF/PSNR/SSIM); see README "Future ideas".
package abrbench

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/probe"
)

// Rung is one step of an ABR ladder: a target resolution and bitrate.
type Rung struct {
	Name             string
	Width, Height    int
	VideoBitrateKbps int
	AudioBitrateKbps int
}

// DefaultLadder is a small, typical ABR ladder (four rungs, loosely
// modeled on Apple's HLS Authoring Specification).
var DefaultLadder = []Rung{
	{Name: "240p", Width: 426, Height: 240, VideoBitrateKbps: 400, AudioBitrateKbps: 64},
	{Name: "360p", Width: 640, Height: 360, VideoBitrateKbps: 800, AudioBitrateKbps: 96},
	{Name: "480p", Width: 854, Height: 480, VideoBitrateKbps: 1400, AudioBitrateKbps: 128},
	{Name: "720p", Width: 1280, Height: 720, VideoBitrateKbps: 2800, AudioBitrateKbps: 128},
}

// Result is one rung's benchmark outcome.
type Result struct {
	Rung Rung

	OutputPath        string
	FileSizeBytes     int64
	Duration          time.Duration // actual encoded duration, via internal/probe
	EncodeWallTime    time.Duration
	ActualBitrateKbps float64 // (FileSizeBytes*8/1000) / Duration.Seconds()
}

// TargetBitrateKbps is Rung.VideoBitrateKbps + Rung.AudioBitrateKbps —
// what ActualBitrateKbps is compared against.
func (r Result) TargetBitrateKbps() int {
	return r.Rung.VideoBitrateKbps + r.Rung.AudioBitrateKbps
}

// DeltaPercent is how far ActualBitrateKbps came in from
// TargetBitrateKbps, signed (positive = encoder overshot the target).
func (r Result) DeltaPercent() float64 {
	target := float64(r.TargetBitrateKbps())
	if target == 0 {
		return 0
	}
	return (r.ActualBitrateKbps - target) / target * 100
}

// Run encodes inputPath at every rung in ladder, writing outputs into
// outDir, and returns each rung's measured result. A per-rung failure
// aborts the whole run rather than silently skipping it.
func Run(inputPath, outDir string, ladder []Rung) ([]Result, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("abrbench: ffmpeg not found in PATH: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(ladder))
	for _, rung := range ladder {
		outputPath := filepath.Join(outDir, rung.Name+".mp4")

		start := time.Now()
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
			"-i", inputPath,
			"-c:v", "libx264", "-profile:v", "main", "-pix_fmt", "yuv420p",
			"-s", fmt.Sprintf("%dx%d", rung.Width, rung.Height),
			"-b:v", fmt.Sprintf("%dk", rung.VideoBitrateKbps),
			"-maxrate", fmt.Sprintf("%dk", rung.VideoBitrateKbps),
			"-bufsize", fmt.Sprintf("%dk", rung.VideoBitrateKbps*2),
			"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", rung.AudioBitrateKbps),
			outputPath,
		)
		out, err := cmd.CombinedOutput()
		wallTime := time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("abrbench: encoding rung %s: %w\n%s", rung.Name, err, out)
		}

		info, err := os.Stat(outputPath)
		if err != nil {
			return nil, fmt.Errorf("abrbench: statting rung %s output: %w", rung.Name, err)
		}
		duration, err := probe.Duration(outputPath)
		if err != nil {
			return nil, fmt.Errorf("abrbench: probing rung %s output: %w", rung.Name, err)
		}

		var actualKbps float64
		if duration > 0 {
			actualKbps = float64(info.Size()) * 8 / 1000 / duration.Seconds()
		}

		results = append(results, Result{
			Rung:              rung,
			OutputPath:        outputPath,
			FileSizeBytes:     info.Size(),
			Duration:          duration,
			EncodeWallTime:    wallTime,
			ActualBitrateKbps: actualKbps,
		})
	}
	return results, nil
}
