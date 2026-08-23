// Package transcode wraps the FFmpeg CLI (ADR 0001) to turn a downloaded
// ad creative into an HLS/DASH-segmented asset matching the content it
// will be spliced into (codec, resolution, bitrate, segment duration).
// EncodeHLS/EncodeDASH probe the input's real duration (internal/probe,
// ADR 0002) and compute evenly-divided segment boundaries up front —
// see evenSegmentPlan for the bug this replaced.
package transcode

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
	"github.com/izaacledererjunior/stitchpoint/internal/probe"
)

// Params describes the target encode: what the ad must match to splice
// cleanly into the content asset.
type Params struct {
	Width, Height    int
	VideoBitrateKbps int
	AudioBitrateKbps int
	SegmentSeconds   float64

	// MaxDuration, if positive, caps the encoded output (via FFmpeg's -t),
	// trimming a longer creative down to fit. Zero means unbounded. It
	// only trims; it can't pad an under-length creative (internal/live
	// callers should compare TotalDuration() against MaxDuration to
	// detect underfill and cover the gap themselves).
	MaxDuration time.Duration

	// StartOffset, if positive, seeks this far into inputPath before
	// encoding (via FFmpeg's -ss). Used by internal/contentprep to encode
	// one source file's [0, breakStart)/[breakStart, breakEnd)/[breakEnd,
	// end) sub-ranges independently, without a separate trimmed copy.
	StartOffset time.Duration
}

// DefaultParams mirrors testdata/vod/content's known encode.
var DefaultParams = Params{
	Width: 640, Height: 360,
	VideoBitrateKbps: 400,
	AudioBitrateKbps: 96,
	SegmentSeconds:   10,
}

// DownloadFile fetches url and writes it to destPath, for pulling down a
// VAST MediaFile before encoding it.
func DownloadFile(client *http.Client, url, destPath string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("transcode: downloading %s: unexpected status %s", url, resp.Status)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	// Close's own error matters on the success path — a filesystem's
	// delayed write/flush failure only surfaces there.
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// EncodeHLS runs FFmpeg on inputPath, producing an HLS VOD playlist (named
// "ad.m3u8") plus its segments in outDir, encoded to match params. It
// returns the resulting playlist, already parsed, ready to hand to
// stitch.Splice.
func EncodeHLS(inputPath, outDir string, params Params) (*manifest.Playlist, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("transcode: ffmpeg not found in PATH: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, err
	}

	fullDuration, err := probe.Duration(inputPath)
	if err != nil {
		return nil, fmt.Errorf("transcode: probing input duration: %w", err)
	}
	if params.StartOffset >= fullDuration {
		return nil, fmt.Errorf("transcode: StartOffset %v is at or past input duration %v", params.StartOffset, fullDuration)
	}
	duration := fullDuration - params.StartOffset
	if params.MaxDuration > 0 && duration > params.MaxDuration {
		duration = params.MaxDuration
	}
	segmentSeconds, keyframeTimes := evenSegmentPlan(duration, params.SegmentSeconds)

	manifestPath := filepath.Join(outDir, "ad.m3u8")
	segmentPattern := filepath.Join(outDir, "seg_%03d.ts")

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if params.StartOffset > 0 {
		// -ss before -i: fast, keyframe-granular seeking — good enough
		// since break boundaries only need the nearest keyframe.
		args = append(args, "-ss", fmt.Sprintf("%g", params.StartOffset.Seconds()))
	}
	args = append(args, "-i", inputPath)
	if params.MaxDuration > 0 {
		args = append(args, "-t", fmt.Sprintf("%g", params.MaxDuration.Seconds()))
	}
	args = append(args,
		"-c:v", "libx264", "-profile:v", "main", "-pix_fmt", "yuv420p",
		"-s", fmt.Sprintf("%dx%d", params.Width, params.Height),
		"-b:v", fmt.Sprintf("%dk", params.VideoBitrateKbps),
		"-sc_threshold", "0",
	)
	if len(keyframeTimes) > 0 {
		args = append(args, "-force_key_frames", strings.Join(keyframeTimes, ","))
	}
	args = append(args,
		"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", params.AudioBitrateKbps),
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%g", segmentSeconds),
		"-hls_playlist_type", "vod",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", segmentPattern,
		manifestPath,
	)

	cmd := exec.Command(ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("transcode: ffmpeg failed: %w\n%s", err, out)
	}

	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return manifest.Parse(f)
}

// EncodeDASH is EncodeHLS's DASH equivalent, for internal/dashsplice's ad
// input — FFmpeg writes the MPD itself (-use_template/-use_timeline),
// mpd.Parse reads it back in.
func EncodeDASH(inputPath, outDir string, params Params) (*mpd.MPD, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("transcode: ffmpeg not found in PATH: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, err
	}

	fullDuration, err := probe.Duration(inputPath)
	if err != nil {
		return nil, fmt.Errorf("transcode: probing input duration: %w", err)
	}
	if params.StartOffset >= fullDuration {
		return nil, fmt.Errorf("transcode: StartOffset %v is at or past input duration %v", params.StartOffset, fullDuration)
	}
	duration := fullDuration - params.StartOffset
	if params.MaxDuration > 0 && duration > params.MaxDuration {
		duration = params.MaxDuration
	}
	segmentSeconds, keyframeTimes := evenSegmentPlan(duration, params.SegmentSeconds)

	manifestPath := filepath.Join(outDir, "ad.mpd")

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if params.StartOffset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%g", params.StartOffset.Seconds()))
	}
	args = append(args, "-i", inputPath)
	if params.MaxDuration > 0 {
		args = append(args, "-t", fmt.Sprintf("%g", params.MaxDuration.Seconds()))
	}
	args = append(args,
		"-c:v", "libx264", "-profile:v", "main", "-pix_fmt", "yuv420p",
		"-s", fmt.Sprintf("%dx%d", params.Width, params.Height),
		"-b:v", fmt.Sprintf("%dk", params.VideoBitrateKbps),
		"-sc_threshold", "0",
	)
	if len(keyframeTimes) > 0 {
		args = append(args, "-force_key_frames", strings.Join(keyframeTimes, ","))
	}
	// AudioBitrateKbps <= 0 drops audio (-an) rather than encoding
	// silence — mainly for dashsplice's own tests, which need an exact
	// split point AAC's fixed frame size wouldn't otherwise land on.
	adaptationSets := "id=0,streams=v id=1,streams=a"
	if params.AudioBitrateKbps <= 0 {
		args = append(args, "-an")
		adaptationSets = "id=0,streams=v"
	} else {
		args = append(args, "-c:a", "aac", "-b:a", fmt.Sprintf("%dk", params.AudioBitrateKbps))
	}
	args = append(args,
		"-seg_duration", fmt.Sprintf("%g", segmentSeconds),
		"-use_template", "1", "-use_timeline", "1",
		"-adaptation_sets", adaptationSets,
		"-f", "dash",
		manifestPath,
	)

	cmd := exec.Command(ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("transcode: ffmpeg failed: %w\n%s", err, out)
	}

	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out, err := mpd.Parse(f)
	if err != nil {
		return nil, err
	}

	// FFmpeg's dash muxer segments audio/video independently, so a real
	// trailing short audio segment is expected output — see
	// mpd.MergeTrailingShortSegment.
	for _, period := range out.Periods {
		for _, as := range period.AdaptationSets {
			for _, rep := range as.Representations {
				rep.SegmentTemplate.MergeTrailingShortSegment()
			}
		}
	}

	return out, nil
}

// evenSegmentPlan divides duration into whole-number segments as close to
// targetSeconds as possible, rather than cutting at a fixed interval and
// leaving a spurious sub-second trailing segment. Returns the per-segment
// length and the interior keyframe timestamps to force (t=0 and the final
// boundary are never included — nothing to cut there).
func evenSegmentPlan(duration time.Duration, targetSeconds float64) (segmentSeconds float64, keyframeTimes []string) {
	totalSeconds := duration.Seconds()
	n := int(math.Round(totalSeconds / targetSeconds))
	if n < 1 {
		n = 1
	}
	segmentSeconds = totalSeconds / float64(n)
	for i := 1; i < n; i++ {
		keyframeTimes = append(keyframeTimes, fmt.Sprintf("%g", float64(i)*segmentSeconds))
	}
	return segmentSeconds, keyframeTimes
}
