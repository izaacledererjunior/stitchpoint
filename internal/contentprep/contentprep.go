// Package contentprep turns an arbitrary uploaded video into an HLS VOD
// playlist carrying one or more ad-break insertion points, for playground
// uploads that don't already have #EXT-X-CUE-OUT/#EXT-X-CUE-IN authored
// in. Output targets stitch.Options.PreserveAllContent (ADR 0009): a
// break is marked, not carved out. Each span between breaks is its own
// independent transcode.EncodeHLS run, concatenated — so every join lands
// on a segment boundary by construction.
package contentprep

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
	"github.com/izaacledererjunior/stitchpoint/internal/probe"
	"github.com/izaacledererjunior/stitchpoint/internal/transcode"
)

// BreakSpec is one ad break to inject. Start is where the break is
// inserted (nothing is removed); Duration is a target length written as
// the insertion point's CueOutDuration, not a range carved from the source.
type BreakSpec struct {
	Start    time.Duration
	Duration time.Duration
}

// InjectBreak is InjectBreaks with a single BreakSpec — the common case
// for a playground upload.
func InjectBreak(sourcePath, outDir string, breakStart, breakDuration time.Duration) (*manifest.Playlist, error) {
	return InjectBreaks(sourcePath, outDir, []BreakSpec{{Start: breakStart, Duration: breakDuration}})
}

// InjectBreaks encodes sourcePath into outDir as an HLS VOD playlist with
// break insertion points at every point in breaks (any order; sorted
// internally), tagged with #EXT-X-CUE-OUT/#EXT-X-CUE-IN so the result can
// go straight into stitch.SpliceWithOptions(..., Options{PreserveAllContent:
// true}). Encode parameters are probed from sourcePath itself (not
// transcode.DefaultParams, which is for this project's known test
// content). Every break.Start must be strictly greater than the previous
// one and less than the source's total duration.
func InjectBreaks(sourcePath, outDir string, breaks []BreakSpec) (*manifest.Playlist, error) {
	if len(breaks) == 0 {
		return nil, fmt.Errorf("contentprep: at least one break is required")
	}
	sorted := append([]BreakSpec(nil), breaks...)
	sortBreaks(sorted)

	total, err := probe.Duration(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("contentprep: probing source duration: %w", err)
	}

	cursor := time.Duration(0)
	for i, br := range sorted {
		if br.Duration <= 0 {
			return nil, fmt.Errorf("contentprep: break %d: duration must be positive", i)
		}
		if br.Start <= cursor {
			return nil, fmt.Errorf("contentprep: break %d: starts at %v, at or before the previous break (or 0) at %v", i, br.Start, cursor)
		}
		if br.Start >= total {
			return nil, fmt.Errorf("contentprep: break %d: starts at %v, at or past the source's duration (%v)", i, br.Start, total)
		}
		cursor = br.Start
	}

	video, err := probe.Video(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("contentprep: probing source video params: %w", err)
	}
	params := transcode.Params{
		Width:            video.Width,
		Height:           video.Height,
		VideoBitrateKbps: video.BitrateKbps,
		AudioBitrateKbps: 96, // not probed separately; a reasonable fixed default for a demo upload
		SegmentSeconds:   transcode.DefaultParams.SegmentSeconds,
	}
	if params.VideoBitrateKbps <= 0 {
		return nil, fmt.Errorf("contentprep: source reports no usable bitrate")
	}

	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, err
	}

	out := &manifest.Playlist{Version: 3, PlaylistType: "VOD", EndList: true}

	// pendingCueIn: the next span's first segment needs #EXT-X-CUE-IN.
	pendingCueIn := false
	cursor = 0
	for i, br := range sorted {
		dir := filepath.Join(outDir, fmt.Sprintf("span%d", i))
		pl, err := transcode.EncodeHLS(sourcePath, dir, withParams(params, cursor, br.Start-cursor))
		if err != nil {
			return nil, fmt.Errorf("contentprep: encoding [%v, %v): %w", cursor, br.Start, err)
		}
		if len(pl.Segments) == 0 {
			return nil, fmt.Errorf("contentprep: content encode [%v, %v) produced no segments", cursor, br.Start)
		}
		startIdx := len(out.Segments)
		if err := appendSegments(out, pl, dir, outDir, fmt.Sprintf("c%d_", i)); err != nil {
			return nil, err
		}
		if pendingCueIn {
			out.Segments[startIdx].CueIn = true
		}

		// Mark the insertion point on the segment ending this span;
		// nothing carved out, it stays real content.
		lastIdx := len(out.Segments) - 1
		out.Segments[lastIdx].CueOut = true
		out.Segments[lastIdx].CueOutDuration = br.Duration.Seconds()
		pendingCueIn = true

		cursor = br.Start
	}

	// Final span: from the last break to the source's end.
	dir := filepath.Join(outDir, "post")
	pl, err := transcode.EncodeHLS(sourcePath, dir, withParams(params, cursor, 0))
	if err != nil {
		return nil, fmt.Errorf("contentprep: encoding [%v, end): %w", cursor, err)
	}
	if len(pl.Segments) == 0 {
		return nil, fmt.Errorf("contentprep: final encode produced no segments")
	}
	startIdx := len(out.Segments)
	if err := appendSegments(out, pl, dir, outDir, "post_"); err != nil {
		return nil, err
	}
	if pendingCueIn {
		out.Segments[startIdx].CueIn = true
	}

	out.TargetDuration = int(params.SegmentSeconds + 0.999) // round up, matches manifest.Write's expectations

	manifestPath := filepath.Join(outDir, "content.m3u8")
	mf, err := os.Create(manifestPath)
	if err != nil {
		return nil, err
	}
	if err := manifest.Write(mf, out); err != nil {
		_ = mf.Close()
		return nil, err
	}
	if err := mf.Close(); err != nil {
		return nil, err
	}

	return out, nil
}

// sortBreaks sorts breaks by Start, ascending.
func sortBreaks(breaks []BreakSpec) {
	for i := 1; i < len(breaks); i++ {
		for j := i; j > 0 && breaks[j].Start < breaks[j-1].Start; j-- {
			breaks[j], breaks[j-1] = breaks[j-1], breaks[j]
		}
	}
}

// withParams returns a copy of base with StartOffset/MaxDuration set for
// one sub-range encode. maxDuration of 0 means unbounded (run to EOF),
// matching transcode.Params.MaxDuration's own zero-value meaning.
func withParams(base transcode.Params, startOffset, maxDuration time.Duration) transcode.Params {
	p := base
	p.StartOffset = startOffset
	p.MaxDuration = maxDuration
	return p
}

// appendSegments copies src's segment files into outDir with prefix
// prepended (avoiding collisions between sub-range encodes, which all
// produce seg_000.ts), and appends the renamed segments to out.
func appendSegments(out *manifest.Playlist, src *manifest.Playlist, srcDir, outDir, prefix string) error {
	for _, s := range src.Segments {
		newURI := prefix + s.URI
		if err := copyFile(filepath.Join(srcDir, s.URI), filepath.Join(outDir, newURI)); err != nil {
			return fmt.Errorf("contentprep: copying segment %q: %w", s.URI, err)
		}
		s.URI = newURI
		out.Segments = append(out.Segments, s)
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
