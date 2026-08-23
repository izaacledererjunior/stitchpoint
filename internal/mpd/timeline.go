package mpd

import "time"

// MinSegmentDuration is the threshold below which
// MergeTrailingShortSegment treats a SegmentTimeline's final entry as a
// rounding remainder (FFmpeg's dash muxer rounds audio to the nearest
// AAC frame, leaving a real ~0.07s trailing segment), not a real one.
const MinSegmentDuration = time.Second

// MergeTrailingShortSegment folds a short trailing segment into the one
// before it — the DASH equivalent of evenSegmentPlan's HLS fix. No-op if
// the short segment is part of a repeated (S/@r) run (a real
// uniform-duration stream, not a rounding artifact).
func (t *SegmentTemplate) MergeTrailingShortSegment() {
	if t == nil || t.SegmentTimeline == nil {
		return
	}
	tl := t.SegmentTimeline
	n := len(tl.S)
	if n == 0 {
		return
	}
	last := tl.S[n-1]
	if last.R > 0 {
		return
	}
	lastDur := time.Duration(float64(last.D) / float64(t.Timescale) * float64(time.Second))
	if lastDur >= MinSegmentDuration {
		return
	}
	if n == 1 {
		return // nothing earlier to merge into
	}

	prev := tl.S[n-2]
	if prev.R > 0 {
		// prev represents R+1 segments of duration prev.D; folding the
		// short trailing segment into just the last of those means
		// splitting the group: R-1 segments still share prev.D, and one
		// new final entry covers prev.D+last.D.
		tl.S[n-2].R--
		tl.S[n-1] = S{D: prev.D + last.D}
		return
	}
	tl.S[n-2].D += last.D
	tl.S = tl.S[:n-1]
}
