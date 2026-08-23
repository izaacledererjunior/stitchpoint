package dashsplice

import (
	"testing"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
)

func u64(v uint64) *uint64 { return &v }

// timeline builds a SegmentTimeline of n uniform segments of dur ticks
// each, starting at startTicks — the common case both the real
// reference example and this project's own EncodeDASH output produce.
func timeline(startTicks, dur uint64, n int) *mpd.SegmentTimeline {
	return &mpd.SegmentTimeline{S: []mpd.S{{T: u64(startTicks), D: dur, R: n - 1}}}
}

func TestExpandCompactTimeline_RoundTrip(t *testing.T) {
	tl := timeline(1000, 60000, 5)
	entries := expandTimeline(tl)
	if len(entries) != 5 {
		t.Fatalf("expandTimeline: got %d entries, want 5", len(entries))
	}
	for i, e := range entries {
		wantStart := uint64(1000 + i*60000)
		if e.start != wantStart || e.dur != 60000 {
			t.Errorf("entries[%d] = %+v, want start=%d dur=60000", i, e, wantStart)
		}
	}

	back := compactTimeline(entries)
	if len(back.S) != 1 || back.S[0].R != 4 || back.S[0].D != 60000 || back.S[0].T == nil || *back.S[0].T != 1000 {
		t.Errorf("compactTimeline round-trip = %+v, want one S{T:1000,D:60000,R:4}", back.S)
	}
}

func TestSplitTemplate(t *testing.T) {
	// 10 segments of 60000 ticks each at timescale 60000 (so each segment
	// is exactly 1 real second) starting at t=0 — break covers segments
	// [3,7) i.e. seconds [3,7).
	mkTpl := func() mpd.SegmentTemplate {
		return mpd.SegmentTemplate{
			Timescale: 60000, StartNumber: 1,
			Media:           "seg_$Number$.m4s",
			SegmentTimeline: timeline(0, 60000, 10),
		}
	}

	t.Run("clean split mid-timeline", func(t *testing.T) {
		before, after, err := splitTemplate(mkTpl(), 3*time.Second, 7*time.Second)
		if err != nil {
			t.Fatalf("splitTemplate() error = %v", err)
		}
		beforeEntries := expandTimeline(before.SegmentTimeline)
		afterEntries := expandTimeline(after.SegmentTimeline)
		if len(beforeEntries) != 3 {
			t.Errorf("before: %d entries, want 3", len(beforeEntries))
		}
		if len(afterEntries) != 3 {
			t.Errorf("after: %d entries, want 3 (segments 7,8,9)", len(afterEntries))
		}
		if after.StartNumber != 1+7 {
			t.Errorf("after.StartNumber = %d, want %d", after.StartNumber, 1+7)
		}
		if after.PresentationTimeOffset != 7*60000 {
			t.Errorf("after.PresentationTimeOffset = %d, want %d", after.PresentationTimeOffset, 7*60000)
		}
		// "after" first entry's period-relative time must be exactly 0
		// once PresentationTimeOffset is applied — this is the whole
		// point of setting it.
		relStart := afterEntries[0].start - after.PresentationTimeOffset
		if relStart != 0 {
			t.Errorf("after's first segment period-relative start = %d, want 0", relStart)
		}
	})

	t.Run("break runs to the end of the timeline", func(t *testing.T) {
		before, after, err := splitTemplate(mkTpl(), 8*time.Second, 10*time.Second)
		if err != nil {
			t.Fatalf("splitTemplate() error = %v", err)
		}
		if len(expandTimeline(before.SegmentTimeline)) != 8 {
			t.Errorf("before: want 8 entries")
		}
		if len(after.SegmentTimeline.S) != 0 {
			t.Errorf("after: want empty timeline, got %+v", after.SegmentTimeline.S)
		}
	})

	t.Run("break start not on a segment boundary: refuses", func(t *testing.T) {
		_, _, err := splitTemplate(mkTpl(), 3500*time.Millisecond, 7*time.Second)
		if err == nil {
			t.Fatal("splitTemplate() error = nil, want an error for a misaligned break start")
		}
	})

	t.Run("break end not on a segment boundary: refuses", func(t *testing.T) {
		_, _, err := splitTemplate(mkTpl(), 3*time.Second, 7500*time.Millisecond)
		if err == nil {
			t.Fatal("splitTemplate() error = nil, want an error for a misaligned break end")
		}
	})

	t.Run("break spans a repeat group partway through: still splits correctly", func(t *testing.T) {
		// Confirms this isn't limited to S-array-element boundaries —
		// the break here starts/ends mid-run of a single S{R:9} entry.
		before, after, err := splitTemplate(mkTpl(), 3*time.Second, 6*time.Second)
		if err != nil {
			t.Fatalf("splitTemplate() error = %v", err)
		}
		if len(expandTimeline(before.SegmentTimeline)) != 3 {
			t.Errorf("before: want 3 entries")
		}
		if len(expandTimeline(after.SegmentTimeline)) != 4 {
			t.Errorf("after: want 4 entries (segments 6,7,8,9)")
		}
	})
}

func TestSplitAdaptationSets_RequiresSegmentTimeline(t *testing.T) {
	as := []mpd.AdaptationSet{
		{
			ContentType: "video",
			Representations: []mpd.Representation{
				{ID: "1", SegmentTemplate: &mpd.SegmentTemplate{Timescale: 1000, Duration: 5000}}, // fixed-duration, no timeline
			},
		},
	}
	_, _, err := splitAdaptationSets(as, time.Second, 2*time.Second)
	if err == nil {
		t.Fatal("splitAdaptationSets() error = nil, want an error for a non-SegmentTimeline representation")
	}
}
