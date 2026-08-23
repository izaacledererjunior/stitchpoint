// Package dashsplice is DASH's equivalent of internal/stitch: splicing a
// resolved ad into a content MPD at its signaled SCTE-35 break (ADR 0007).
// DASH splits the content's Period at the break and inserts a new Period
// for the ad, rather than rewriting a flat segment list — a Period
// boundary is a hard reset for the player, so unlike HLS the ad doesn't
// need matching codec/bitrate. Only SegmentTimeline-based content is
// supported, and only the first cue found is spliced per call.
package dashsplice

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
)

// durationEpsilon mirrors internal/stitch's tolerance for the same
// reason: real encodes rarely land on bit-exact durations.
const durationEpsilon = 100 * time.Millisecond

// ErrNoAdBreak is returned when content has no SCTE-35 cue to splice
// into (see internal/mpd.ExtractCues).
var ErrNoAdBreak = errors.New("dashsplice: no SCTE-35 cue found in content MPD")

// ErrCueMissingDuration is returned when the cue has no signaled break
// duration — DASH has no second "CUE-IN" marker to infer the end from.
var ErrCueMissingDuration = errors.New("dashsplice: cue has no signaled break duration")

// DurationMismatchError mirrors stitch.DurationMismatchError.
type DurationMismatchError struct {
	BreakDuration time.Duration
	AdDuration    time.Duration
}

// Error implements the error interface.
func (e *DurationMismatchError) Error() string {
	return fmt.Sprintf("dashsplice: ad duration %v does not match break duration %v (tolerance %v)",
		e.AdDuration, e.BreakDuration, durationEpsilon)
}

// Options mirrors stitch.Options.
type Options struct {
	// AllowDurationMismatch skips the ad/break duration check — see
	// stitch.Options.AllowDurationMismatch.
	AllowDurationMismatch bool
}

// Splice is SpliceWithOptions with default Options.
func Splice(content, ad *mpd.MPD) (*mpd.MPD, error) {
	return SpliceWithOptions(content, ad, Options{})
}

// SpliceWithOptions splits content's Period at its first SCTE-35 cue and
// inserts ad's own Period between the two halves, returning a new MPD.
// content and ad are not modified.
func SpliceWithOptions(content, ad *mpd.MPD, opts Options) (*mpd.MPD, error) {
	cues, err := mpd.ExtractCues(content)
	if err != nil {
		return nil, fmt.Errorf("dashsplice: %w", err)
	}
	if len(cues) == 0 {
		return nil, ErrNoAdBreak
	}
	cue := cues[0]
	if cue.Duration <= 0 {
		return nil, ErrCueMissingDuration
	}
	if len(ad.Periods) == 0 {
		return nil, fmt.Errorf("dashsplice: ad MPD has no Periods")
	}

	breakStart := cue.PresentationTime
	breakEnd := breakStart + cue.Duration

	contentPeriodIdx := -1
	for i, p := range content.Periods {
		if p.ID == cue.PeriodID {
			contentPeriodIdx = i
			break
		}
	}
	if contentPeriodIdx == -1 {
		return nil, fmt.Errorf("dashsplice: cue's period %q not found in content", cue.PeriodID)
	}
	contentPeriod := content.Periods[contentPeriodIdx]

	periodStart, err := periodStartOrZero(contentPeriod.Start)
	if err != nil {
		return nil, fmt.Errorf("dashsplice: content period %q: %w", contentPeriod.ID, err)
	}

	adPeriod := ad.Periods[0]
	adDuration, err := periodDuration(adPeriod)
	if err != nil {
		return nil, fmt.Errorf("dashsplice: measuring ad duration: %w", err)
	}
	breakDuration := cue.Duration
	if !opts.AllowDurationMismatch && absDuration(breakDuration-adDuration) > durationEpsilon {
		return nil, &DurationMismatchError{BreakDuration: breakDuration, AdDuration: adDuration}
	}

	beforeAS, afterAS, err := splitAdaptationSets(contentPeriod.AdaptationSets, breakStart, breakEnd)
	if err != nil {
		return nil, err
	}

	before := mpd.Period{
		ID:             contentPeriod.ID + "-pre",
		Start:          contentPeriod.Start,
		AdaptationSets: beforeAS,
	}
	adOut := mpd.Period{
		ID:             contentPeriod.ID + "-ad",
		Start:          mpd.FormatDuration(periodStart + breakStart),
		AdaptationSets: adPeriod.AdaptationSets,
	}
	after := mpd.Period{
		ID:             contentPeriod.ID + "-post",
		Start:          mpd.FormatDuration(periodStart + breakEnd),
		AdaptationSets: afterAS,
	}

	out := &mpd.MPD{
		Type:                      content.Type,
		MediaPresentationDuration: content.MediaPresentationDuration,
	}
	for i, p := range content.Periods {
		switch {
		case i < contentPeriodIdx:
			out.Periods = append(out.Periods, p)
		case i == contentPeriodIdx:
			out.Periods = append(out.Periods, before, adOut, after)
		default:
			out.Periods = append(out.Periods, p)
		}
	}
	return out, nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func periodStartOrZero(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return mpd.ParseDuration(s)
}

// periodDuration returns p's first AdaptationSet/Representation's
// SegmentTimeline duration — one representative track, since DASH's
// independent per-track segmentation means tracks can differ slightly
// (see mpd.MergeTrailingShortSegment).
func periodDuration(p mpd.Period) (time.Duration, error) {
	if len(p.AdaptationSets) == 0 || len(p.AdaptationSets[0].Representations) == 0 {
		return 0, fmt.Errorf("period %q has no Representations", p.ID)
	}
	as := p.AdaptationSets[0]
	rep := as.Representations[0]
	tpl := rep.EffectiveSegmentTemplate(as)
	if tpl == nil || tpl.SegmentTimeline == nil {
		return 0, fmt.Errorf("period %q representation %q: no SegmentTimeline", p.ID, rep.ID)
	}
	entries := expandTimeline(tpl.SegmentTimeline)
	if len(entries) == 0 {
		return 0, nil
	}
	last := entries[len(entries)-1]
	totalTicks := last.start + last.dur - entries[0].start
	return ticksToDuration(totalTicks, tpl.Timescale), nil
}

func ticksToDuration(ticks, timescale uint64) time.Duration {
	if timescale == 0 {
		return 0
	}
	return time.Duration(float64(ticks) / float64(timescale) * float64(time.Second))
}

func durationToTicks(d time.Duration, timescale uint64) uint64 {
	return uint64(math.Round(d.Seconds() * float64(timescale)))
}
