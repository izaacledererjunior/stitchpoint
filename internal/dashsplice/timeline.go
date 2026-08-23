package dashsplice

import (
	"fmt"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
)

// segEntry is one expanded (non-repeat-compacted) SegmentTimeline entry,
// in its Representation's own timescale ticks.
type segEntry struct {
	start, dur uint64
}

// expandTimeline flattens a SegmentTimeline's S/@r-compacted entries into
// one segEntry per actual segment, so a break boundary falling partway
// through a repeated run can still be located precisely.
func expandTimeline(tl *mpd.SegmentTimeline) []segEntry {
	var out []segEntry
	var cur uint64
	for _, s := range tl.S {
		start := cur
		if s.T != nil {
			start = *s.T
		}
		for i := 0; i <= s.R; i++ {
			out = append(out, segEntry{start: start, dur: s.D})
			start += s.D
		}
		cur = start
	}
	return out
}

// compactTimeline is expandTimeline's inverse: re-groups a flat segment
// list into S/@r form, merging consecutive entries with equal duration.
// Only the first output entry carries an explicit @t.
func compactTimeline(entries []segEntry) *mpd.SegmentTimeline {
	tl := &mpd.SegmentTimeline{}
	if len(entries) == 0 {
		return tl
	}
	i := 0
	for i < len(entries) {
		d := entries[i].dur
		j := i
		for j+1 < len(entries) && entries[j+1].dur == d {
			j++
		}
		s := mpd.S{D: d, R: j - i}
		if i == 0 {
			t := entries[0].start
			s.T = &t
		}
		tl.S = append(tl.S, s)
		i = j + 1
	}
	return tl
}

// splitAdaptationSets splits every Representation's SegmentTimeline in as
// at [breakStart, breakEnd), returning the "before"/"after"
// AdaptationSets for two new Periods. The break must land exactly on a
// tick boundary in every Representation, or this refuses rather than
// approximates.
func splitAdaptationSets(as []mpd.AdaptationSet, breakStart, breakEnd time.Duration) (before, after []mpd.AdaptationSet, err error) {
	before = make([]mpd.AdaptationSet, len(as))
	after = make([]mpd.AdaptationSet, len(as))
	for i, set := range as {
		beforeReps := make([]mpd.Representation, len(set.Representations))
		afterReps := make([]mpd.Representation, len(set.Representations))
		for j, rep := range set.Representations {
			tpl := rep.EffectiveSegmentTemplate(set)
			if tpl == nil || tpl.SegmentTimeline == nil {
				return nil, nil, fmt.Errorf("dashsplice: adaptationSet %d representation %q: no SegmentTimeline (only SegmentTimeline-based content is supported — see package doc)", i, rep.ID)
			}

			beforeTpl, afterTpl, err := splitTemplate(*tpl, breakStart, breakEnd)
			if err != nil {
				return nil, nil, fmt.Errorf("dashsplice: adaptationSet %d representation %q: %w", i, rep.ID, err)
			}

			beforeRep := rep
			beforeRep.SegmentTemplate = beforeTpl
			afterRep := rep
			afterRep.SegmentTemplate = afterTpl
			beforeReps[j] = beforeRep
			afterReps[j] = afterRep
		}
		beforeSet := set
		beforeSet.SegmentTemplate = nil // per-Representation templates only, after the split
		beforeSet.Representations = beforeReps
		afterSet := set
		afterSet.SegmentTemplate = nil
		afterSet.Representations = afterReps
		before[i] = beforeSet
		after[i] = afterSet
	}
	return before, after, nil
}

// splitTemplate splits one SegmentTemplate's timeline at [breakStart,
// breakEnd). The "after" half keeps original tick values (nothing
// rebased) and adjusts PresentationTimeOffset so its first segment's
// Period-relative time is 0; StartNumber advances so $Number$-templated
// URLs still resolve to the correct existing files.
func splitTemplate(tpl mpd.SegmentTemplate, breakStart, breakEnd time.Duration) (before, after *mpd.SegmentTemplate, err error) {
	entries := expandTimeline(tpl.SegmentTimeline)
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("empty SegmentTimeline")
	}

	startTicks := durationToTicks(breakStart, tpl.Timescale)
	endTicks := durationToTicks(breakEnd, tpl.Timescale)

	splitIdx := indexOfStart(entries, startTicks)
	if splitIdx == -1 {
		return nil, nil, fmt.Errorf("break start (%v) does not land on a segment boundary", breakStart)
	}
	resumeIdx := indexOfStart(entries, endTicks)
	if resumeIdx == -1 {
		if endTicks == entries[len(entries)-1].start+entries[len(entries)-1].dur {
			resumeIdx = len(entries) // break runs exactly to the end of this timeline
		} else {
			return nil, nil, fmt.Errorf("break end (%v) does not land on a segment boundary", breakEnd)
		}
	}
	if resumeIdx < splitIdx {
		return nil, nil, fmt.Errorf("break end (%v) is before break start (%v)", breakEnd, breakStart)
	}

	beforeEntries := entries[:splitIdx]
	afterEntries := entries[resumeIdx:]

	beforeTpl := tpl
	beforeTpl.SegmentTimeline = compactTimeline(beforeEntries)

	afterTpl := tpl
	afterTpl.StartNumber = tpl.StartNumber + uint64(resumeIdx)
	if len(afterEntries) > 0 {
		afterTpl.PresentationTimeOffset = afterEntries[0].start
	}
	afterTpl.SegmentTimeline = compactTimeline(afterEntries)

	return &beforeTpl, &afterTpl, nil
}

// indexOfStart returns the index of the segEntry whose start tick
// exactly equals target, or -1 if none does.
func indexOfStart(entries []segEntry, target uint64) int {
	for i, e := range entries {
		if e.start == target {
			return i
		}
	}
	return -1
}
