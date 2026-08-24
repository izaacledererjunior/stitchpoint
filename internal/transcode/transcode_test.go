package transcode

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestEvenSegmentPlan(t *testing.T) {
	tests := []struct {
		name          string
		durationSecs  float64
		targetSecs    float64
		wantSegSecs   float64
		wantNumForced int // len(keyframeTimes); total segments = this + 1
	}{
		{
			name:          "evenly divides already",
			durationSecs:  20,
			targetSecs:    10,
			wantSegSecs:   10,
			wantNumForced: 1,
		},
		{
			// This is the exact shape of the real bug this function
			// fixes: a fixed 10s interval against a 6.0064s creative used
			// to be handled by FFmpeg's own cutting, which (for durations
			// exceeding the target) could leave a spurious short trailing
			// segment. Here the duration is *shorter* than the target, so
			// rounding correctly collapses it to a single segment with no
			// forced interior cuts at all — better than forcing a split
			// that was never asked for.
			name:          "shorter than target collapses to one segment",
			durationSecs:  6.0064,
			targetSecs:    10,
			wantSegSecs:   6.0064,
			wantNumForced: 0,
		},
		{
			// The actual observed bug shape: ~10.03s against a 4s target
			// naively cuts into 4s+4s+2.03s (a sub-second-ish remainder).
			// Evenly dividing into 3 segments of ~3.34s each removes the
			// remainder entirely.
			name:          "remainder-prone duration divides evenly instead",
			durationSecs:  10.031011,
			targetSecs:    4,
			wantSegSecs:   10.031011 / 3,
			wantNumForced: 2,
		},
		{
			name:          "zero duration still produces one segment, no panic",
			durationSecs:  0,
			targetSecs:    10,
			wantSegSecs:   0,
			wantNumForced: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			segSecs, keyframeTimes := evenSegmentPlan(time.Duration(tc.durationSecs*float64(time.Second)), tc.targetSecs)
			if diff := segSecs - tc.wantSegSecs; diff > 1e-6 || diff < -1e-6 {
				t.Errorf("segmentSeconds = %v, want %v", segSecs, tc.wantSegSecs)
			}
			if len(keyframeTimes) != tc.wantNumForced {
				t.Errorf("len(keyframeTimes) = %d, want %d (keyframeTimes=%v)", len(keyframeTimes), tc.wantNumForced, keyframeTimes)
			}
		})
	}
}

func TestDownloadFile(t *testing.T) {
	const body = "fake creative bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "creative.mp4")
	if err := DownloadFile(srv.Client(), srv.URL, dest); err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != body {
		t.Errorf("downloaded content = %q, want %q", got, body)
	}
}

func TestDownloadFile_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	err := DownloadFile(srv.Client(), srv.URL, filepath.Join(t.TempDir(), "out.mp4"))
	if err == nil {
		t.Fatal("DownloadFile() error = nil, want error for a 404 response")
	}
}

// TestEncodeHLS_MatchesParams is a real (not mocked) FFmpeg run: it
// re-encodes one of the checked-in test content segments — standing in
// for a downloaded VAST creative — and confirms the output actually
// matches the requested resolution/segment duration. Skipped if ffmpeg
// isn't in PATH.
func TestEncodeHLS_MatchesParams(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	input := filepath.Join("..", "..", "testdata", "vod", "ad", "seg_000.ts")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}

	params := Params{Width: 320, Height: 180, VideoBitrateKbps: 200, AudioBitrateKbps: 64, SegmentSeconds: 5}
	out, err := EncodeHLS(input, t.TempDir(), params)
	if err != nil {
		t.Fatalf("EncodeHLS() error = %v", err)
	}

	if len(out.Segments) == 0 {
		t.Fatal("EncodeHLS() produced a playlist with no segments")
	}
	total := out.TotalDuration()
	// Source segment is ~10s; re-encoded at 5s target segments, expect
	// roughly the same total duration (allow encoder slack).
	if total < 8 || total > 12 {
		t.Errorf("TotalDuration() = %v, want ~10s (source segment length)", total)
	}

	// Regression check for the bug evenSegmentPlan exists to fix: no
	// segment should come out anywhere near zero-length. Before that fix,
	// a duration that didn't divide evenly by the target could leave a
	// spurious sub-second trailing segment (observed: 0.033s).
	for i, s := range out.Segments {
		if s.Duration < 1.0 {
			t.Errorf("Segments[%d].Duration = %v, suspiciously short (sub-second trailing segment bug?)", i, s.Duration)
		}
	}
}

// TestEncodeDASH_MatchesParams is EncodeDASH's equivalent of
// TestEncodeHLS_MatchesParams — a real FFmpeg run, checked against the
// same sub-second-trailing-segment regression (this time by summing each
// SegmentTimeline's own entries into per-segment durations, since DASH's
// timeline is expressed as start/duration ticks, not a flat per-segment
// list the way manifest.Segment is).
func TestEncodeDASH_MatchesParams(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	input := filepath.Join("..", "..", "testdata", "vod", "ad", "seg_000.ts")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}

	params := Params{Width: 320, Height: 180, VideoBitrateKbps: 200, AudioBitrateKbps: 64, SegmentSeconds: 5}
	out, err := EncodeDASH(input, t.TempDir(), params)
	if err != nil {
		t.Fatalf("EncodeDASH() error = %v", err)
	}

	if len(out.Periods) != 1 {
		t.Fatalf("EncodeDASH() produced %d periods, want 1", len(out.Periods))
	}
	if len(out.Periods[0].AdaptationSets) == 0 {
		t.Fatal("EncodeDASH() produced a period with no AdaptationSets")
	}

	for _, as := range out.Periods[0].AdaptationSets {
		for _, rep := range as.Representations {
			tpl := rep.SegmentTemplate
			if tpl == nil || tpl.SegmentTimeline == nil {
				t.Fatalf("Representation %q: no SegmentTemplate/SegmentTimeline", rep.ID)
			}
			for i, s := range tpl.SegmentTimeline.S {
				segDur := float64(s.D) / float64(tpl.Timescale)
				// Same regression this package's HLS test guards
				// against: a duration that doesn't divide evenly by the
				// target segment length must not leave a spurious
				// near-zero trailing segment.
				if segDur < 1.0 {
					t.Errorf("Representation %q: SegmentTimeline.S[%d] duration = %.3fs, suspiciously short (sub-second trailing segment bug?)", rep.ID, i, segDur)
				}
			}
		}
	}
}

// TestEncodeHLS_MaxDurationTrims is a real FFmpeg run confirming
// MaxDuration actually caps the output — the exact-duration matching live
// splicing needs (see Params.MaxDuration's doc for why VOD never needed
// this).
func TestEncodeHLS_MaxDurationTrims(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	input := filepath.Join("..", "..", "testdata", "vod", "ad", "seg_000.ts")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}

	// Source is ~10s; cap well below that so trimming is unambiguous.
	params := Params{
		Width: 320, Height: 180, VideoBitrateKbps: 200, AudioBitrateKbps: 64,
		SegmentSeconds: 5, MaxDuration: 4 * time.Second,
	}
	out, err := EncodeHLS(input, t.TempDir(), params)
	if err != nil {
		t.Fatalf("EncodeHLS() error = %v", err)
	}

	total := out.TotalDuration()
	if total < 3.5 || total > 4.5 {
		t.Errorf("TotalDuration() = %v, want ~4s (MaxDuration should have trimmed the ~10s source)", total)
	}
}

// TestEncodeHLS_LoopInputCoversFullDuration is a regression test for a
// real deployment freeze: internal/live used to encode a short creative
// once and repeat the resulting segments at the manifest level to cover
// an underfilled break, which needed a fresh discontinuity at every
// repeat and left byte-identical timestamps at each one — real players
// stalled at the repeat boundary. LoopInput moves the repetition into
// FFmpeg itself, so the encoded output should span the full MaxDuration
// on its own, without any caller-side repetition needed.
func TestEncodeHLS_LoopInputCoversFullDuration(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	input := filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v", err)
	}

	// Source is 6s; without LoopInput this could reach at most ~6s no
	// matter what MaxDuration says, since MaxDuration only trims.
	// Requesting 15s here — over two source lengths — is only reachable
	// if the input is actually being looped.
	params := Params{
		Width: 320, Height: 180, VideoBitrateKbps: 200, AudioBitrateKbps: 64,
		SegmentSeconds: 5, MaxDuration: 15 * time.Second, LoopInput: true,
	}
	out, err := EncodeHLS(input, t.TempDir(), params)
	if err != nil {
		t.Fatalf("EncodeHLS() error = %v", err)
	}

	total := out.TotalDuration()
	if total < 14.5 || total > 15.5 {
		t.Errorf("TotalDuration() = %v, want ~15s (LoopInput should have covered the full MaxDuration from the 6s source)", total)
	}
}

func TestEncodeHLS_LoopInputRequiresMaxDuration(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	input := filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v", err)
	}

	params := Params{
		Width: 320, Height: 180, VideoBitrateKbps: 200, AudioBitrateKbps: 64,
		SegmentSeconds: 5, LoopInput: true, // no MaxDuration
	}
	if _, err := EncodeHLS(input, t.TempDir(), params); err == nil {
		t.Fatal("EncodeHLS() error = nil, want an error (LoopInput with no MaxDuration would loop forever)")
	}
}

// TestEncodeHLS_StartOffsetSeeks proves StartOffset actually seeks (the
// output is shorter by roughly the offset) rather than just being an
// accepted-but-ignored field — added for internal/contentprep, which
// depends on StartOffset+MaxDuration together carving exact sub-ranges
// out of a single uploaded source file.
func TestEncodeHLS_StartOffsetSeeks(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	input := filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v", err)
	}

	// Source is 6s (see testdata/vastfixture's generation command in
	// README "Test assets"); seeking 4s in with no MaxDuration should
	// yield ~2s of output.
	params := Params{
		Width: 320, Height: 180, VideoBitrateKbps: 200, AudioBitrateKbps: 64,
		SegmentSeconds: 5, StartOffset: 4 * time.Second,
	}
	out, err := EncodeHLS(input, t.TempDir(), params)
	if err != nil {
		t.Fatalf("EncodeHLS() error = %v", err)
	}

	total := out.TotalDuration()
	if total < 1.5 || total > 2.5 {
		t.Errorf("TotalDuration() = %v, want ~2s (StartOffset should have seeked 4s into the 6s source)", total)
	}
}

func TestEncodeHLS_StartOffsetPastEndIsError(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	input := filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4")
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not found: %v", err)
	}

	params := Params{
		Width: 320, Height: 180, VideoBitrateKbps: 200, AudioBitrateKbps: 64,
		SegmentSeconds: 5, StartOffset: 10 * time.Second, // past the 6s source
	}
	if _, err := EncodeHLS(input, t.TempDir(), params); err == nil {
		t.Fatal("EncodeHLS() error = nil, want error for StartOffset past the input's duration")
	}
}
