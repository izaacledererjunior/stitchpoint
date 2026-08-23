package live

import (
	"log/slog"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
)

// BreakFiller covers a break's target duration when the resolved ad runs
// shorter. LoopFiller (below) is a placeholder; a real ad-pod filler is
// future work — see README "Future ideas".
type BreakFiller interface {
	// Fill takes a break's resolved ad segments plus actual/target
	// duration and returns the segments to splice. Must not mutate segs.
	Fill(segs []manifest.Segment, actual, target time.Duration) []manifest.Segment
}

// maxLoopRepeats bounds LoopFiller's output against a pathological input
// (e.g. a sub-second creative against a multi-minute break).
const maxLoopRepeats = 200

// LoopFiller covers an underfilled break by repeating the resolved
// creative until the target duration is reached, exceeded, or
// maxLoopRepeats is hit — a placeholder, not a real ad-pod filler.
type LoopFiller struct{}

// Fill implements BreakFiller.
func (LoopFiller) Fill(segs []manifest.Segment, actual, target time.Duration) []manifest.Segment {
	if actual <= 0 || len(segs) == 0 || actual >= target {
		return segs
	}

	out := append([]manifest.Segment(nil), segs...)
	total := actual
	for i := 0; i < maxLoopRepeats && total < target; i++ {
		loop := append([]manifest.Segment(nil), segs...)
		// Each repeat restarts the creative's PTS at 0, so it needs its
		// own discontinuity marker or players stall at the loop boundary.
		loop[0].Discontinuity = true
		out = append(out, loop...)
		total += actual
	}
	if total < target {
		slog.Warn("live: LoopFiller: hit repeat cap before reaching target — creative is too short relative to the break for looping alone to cover it",
			"maxLoopRepeats", maxLoopRepeats, "target", target, "reached", total)
	}
	return out
}

func sumSegmentDuration(segs []manifest.Segment) time.Duration {
	var total float64
	for _, s := range segs {
		total += s.Duration
	}
	return time.Duration(total * float64(time.Second))
}
