// Package stitch splices an ad asset into a VOD manifest at the
// SCTE-35-signaled ad break(s), at the manifest level (no re-encode):
// each #EXT-X-CUE-OUT/#EXT-X-CUE-IN segment range is replaced with the
// ad's segments, with #EXT-X-DISCONTINUITY at the transitions. Every
// break found gets spliced, not just the first — unlike internal/dashsplice.
package stitch

import (
	"errors"
	"fmt"
	"math"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
)

// durationEpsilon absorbs encoder rounding (e.g. 10.000000 vs 9.999967)
// when comparing break and ad durations.
const durationEpsilon = 0.1 // seconds

// ErrNoAdBreak is returned when the content playlist has no
// #EXT-X-CUE-OUT/#EXT-X-CUE-IN pair to splice into.
var ErrNoAdBreak = errors.New("stitch: no ad break (EXT-X-CUE-OUT/EXT-X-CUE-IN) found in content playlist")

// DurationMismatchError is returned when the ad's total duration doesn't
// match a signaled break's — refused rather than silently trimmed/padded.
type DurationMismatchError struct {
	BreakIndex    int // 0-based, which break (in playlist order) mismatched
	BreakDuration float64
	AdDuration    float64
}

// Error implements the error interface.
func (e *DurationMismatchError) Error() string {
	return fmt.Sprintf("stitch: break %d: ad duration %.3fs does not match break duration %.3fs (tolerance %.3fs)",
		e.BreakIndex, e.AdDuration, e.BreakDuration, durationEpsilon)
}

// Options controls Splice's behavior.
type Options struct {
	// AllowDurationMismatch skips the ad/break duration check — off by
	// default; real ad-decisioning can't guarantee an exact match.
	AllowDurationMismatch bool

	// LoopAdToFillBreak repeats an under-length ad until the break is
	// covered, instead of leaving it under-filled. Mirrors
	// internal/live.LoopFiller — a placeholder, not a real ad-pod feature.
	LoopAdToFillBreak bool

	// PreserveAllContent inserts the ad without removing anything, rather
	// than replacing a pre-authored placeholder range — see ADR 0009.
	PreserveAllContent bool
}

// Splice replaces every ad break in content with ad's segments, returning
// a new playlist; content and ad are not modified. Segment URIs are
// copied verbatim — resolving them to files is the caller's job.
func Splice(content, ad *manifest.Playlist) (*manifest.Playlist, error) {
	return SpliceWithOptions(content, ad, Options{})
}

// SpliceWithOptions is Splice with explicit Options; see Options for what
// each field changes.
func SpliceWithOptions(content, ad *manifest.Playlist, opts Options) (*manifest.Playlist, error) {
	breaks, err := findBreaks(content)
	if err != nil {
		return nil, err
	}

	adDuration := sumDuration(ad.Segments)

	out := &manifest.Playlist{
		Version:        content.Version,
		TargetDuration: content.TargetDuration,
		MediaSequence:  content.MediaSequence,
		PlaylistType:   content.PlaylistType,
		EndList:        content.EndList,
	}

	cursor := 0
	for i, br := range breaks {
		// spanEnd/nextCursor differ in replace mode ([br.start, br.end) is
		// discarded); PreserveAllContent sets both to br.start+1 (nothing
		// dropped).
		spanEnd := br.start
		nextCursor := br.end
		targetDuration := sumDuration(content.Segments[br.start:br.end])
		if opts.PreserveAllContent {
			spanEnd = br.start + 1
			nextCursor = spanEnd
			targetDuration = content.Segments[br.start].CueOutDuration
		}

		adSegs := ad.Segments
		switch {
		case opts.LoopAdToFillBreak && targetDuration > 0 && adDuration < targetDuration-durationEpsilon:
			adSegs = loopSegments(adSegs, adDuration, targetDuration)
		case opts.PreserveAllContent:
			// Nothing replaced, so no length to mismatch against.
		case !opts.AllowDurationMismatch && math.Abs(targetDuration-adDuration) > durationEpsilon:
			return nil, &DurationMismatchError{BreakIndex: i, BreakDuration: targetDuration, AdDuration: adDuration}
		}

		span := append([]manifest.Segment(nil), content.Segments[cursor:spanEnd]...)
		if i > 0 && len(span) > 0 {
			// span[0] carries the previous break's CueIn resume marker;
			// clear it and mark the real bitstream transition instead.
			span[0].CueOut, span[0].CueIn = false, false
			span[0].Discontinuity = true
		}
		if opts.PreserveAllContent {
			// Last segment carried CueOut; still real content, just no
			// longer signaling now that the ad follows it directly.
			span[len(span)-1].CueOut = false
		}
		out.Segments = append(out.Segments, span...)

		for j, s := range adSegs {
			s.CueOut, s.CueIn = false, false // the break is now filled, not signaled
			if j == 0 {
				s.Discontinuity = true // entering the ad's bitstream
			}
			out.Segments = append(out.Segments, s)
		}
		cursor = nextCursor
	}

	if cursor < len(content.Segments) {
		rest := append([]manifest.Segment(nil), content.Segments[cursor:]...)
		rest[0].CueOut, rest[0].CueIn = false, false
		rest[0].Discontinuity = true // returning to the content's bitstream
		out.Segments = append(out.Segments, rest...)
	}

	if td := maxSegmentDuration(out.Segments); td > out.TargetDuration {
		out.TargetDuration = td
	}

	return out, nil
}

// breakRange is one #EXT-X-CUE-OUT/#EXT-X-CUE-IN pair's segment index
// range [start, end) in a content playlist.
type breakRange struct {
	start, end int
}

// findBreaks locates every #EXT-X-CUE-OUT/#EXT-X-CUE-IN pair in p. A
// CUE-OUT with no matching CUE-IN is an error, not an implicit
// "runs to the end."
func findBreaks(p *manifest.Playlist) ([]breakRange, error) {
	var breaks []breakRange
	start := -1
	for i, s := range p.Segments {
		if start == -1 && s.CueOut {
			start = i
			continue
		}
		if start != -1 && s.CueIn {
			breaks = append(breaks, breakRange{start, i})
			start = -1
		}
	}
	if start != -1 {
		return nil, fmt.Errorf("%w: EXT-X-CUE-OUT at segment %d has no matching EXT-X-CUE-IN", ErrNoAdBreak, start)
	}
	if len(breaks) == 0 {
		return nil, ErrNoAdBreak
	}
	return breaks, nil
}

// loopSegments repeats segs (marking Discontinuity on each repeat) until
// their combined duration reaches target — mirrors
// internal/live.LoopFiller.Fill.
func loopSegments(segs []manifest.Segment, actual, target float64) []manifest.Segment {
	if actual <= 0 || len(segs) == 0 {
		return segs
	}
	const maxLoopRepeats = 200
	out := append([]manifest.Segment(nil), segs...)
	total := actual
	for i := 0; i < maxLoopRepeats && total < target-durationEpsilon; i++ {
		loop := append([]manifest.Segment(nil), segs...)
		loop[0].Discontinuity = true
		out = append(out, loop...)
		total += actual
	}
	return out
}

func sumDuration(segs []manifest.Segment) float64 {
	var total float64
	for _, s := range segs {
		total += s.Duration
	}
	return total
}

func maxSegmentDuration(segs []manifest.Segment) int {
	var longest float64
	for _, s := range segs {
		longest = max(longest, s.Duration)
	}
	return int(math.Ceil(longest))
}
