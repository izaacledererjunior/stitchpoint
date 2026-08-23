package stitch

import (
	"errors"
	"testing"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
)

func contentPlaylist() *manifest.Playlist {
	return &manifest.Playlist{
		Version: 3, TargetDuration: 10, PlaylistType: "VOD", EndList: true,
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10},
			{URI: "seg_001.ts", Duration: 10},
			{URI: "seg_002.ts", Duration: 10},
			{URI: "seg_003.ts", Duration: 10, CueOut: true},
			{URI: "seg_004.ts", Duration: 10, CueIn: true},
			{URI: "seg_005.ts", Duration: 10},
		},
	}
}

func TestSplice_ReplacesBreakWithAdSegments(t *testing.T) {
	content := contentPlaylist()
	ad := &manifest.Playlist{
		Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}},
	}

	out, err := Splice(content, ad)
	if err != nil {
		t.Fatalf("Splice() error = %v", err)
	}

	wantURIs := []string{"seg_000.ts", "seg_001.ts", "seg_002.ts", "ad_000.ts", "seg_004.ts", "seg_005.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d: %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}

	// Discontinuities mark both transitions: into the ad, and back to content.
	if !out.Segments[3].Discontinuity {
		t.Errorf("ad segment (index 3) should have Discontinuity=true")
	}
	if !out.Segments[4].Discontinuity {
		t.Errorf("first post-break content segment (index 4) should have Discontinuity=true")
	}
	for i, s := range out.Segments {
		if i == 3 || i == 4 {
			continue
		}
		if s.Discontinuity {
			t.Errorf("Segments[%d] (%s) should not have Discontinuity=true", i, s.URI)
		}
	}

	// The now-filled break shouldn't still advertise itself as a break.
	for i, s := range out.Segments {
		if s.CueOut || s.CueIn {
			t.Errorf("Segments[%d] (%s) still carries a cue marker after splicing: CueOut=%v CueIn=%v", i, s.URI, s.CueOut, s.CueIn)
		}
	}

	if out.TotalDuration() != content.TotalDuration() {
		t.Errorf("TotalDuration() = %v, want unchanged %v (ad duration matches break exactly)", out.TotalDuration(), content.TotalDuration())
	}
}

func TestSplice_AdSpansMultipleSegments(t *testing.T) {
	content := contentPlaylist() // break is one 10s segment
	ad := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "ad_000.ts", Duration: 5},
			{URI: "ad_001.ts", Duration: 5},
		},
	}

	out, err := Splice(content, ad)
	if err != nil {
		t.Fatalf("Splice() error = %v", err)
	}
	wantURIs := []string{"seg_000.ts", "seg_001.ts", "seg_002.ts", "ad_000.ts", "ad_001.ts", "seg_004.ts", "seg_005.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d: %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}
	if !out.Segments[3].Discontinuity {
		t.Errorf("first ad segment should have Discontinuity=true")
	}
	if out.Segments[4].Discontinuity {
		t.Errorf("second ad segment should not have Discontinuity=true")
	}
	if !out.Segments[5].Discontinuity {
		t.Errorf("first post-break content segment should have Discontinuity=true")
	}
}

func TestSplice_DurationMismatchIsRejected(t *testing.T) {
	content := contentPlaylist() // 10s break
	ad := &manifest.Playlist{
		Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 6}}, // way short
	}

	_, err := Splice(content, ad)
	if err == nil {
		t.Fatalf("Splice() error = nil, want DurationMismatchError")
	}
	var mismatch *DurationMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Splice() error = %v (%T), want *DurationMismatchError", err, err)
	}
	if mismatch.BreakDuration != 10 || mismatch.AdDuration != 6 {
		t.Errorf("mismatch = %+v, want BreakDuration=10 AdDuration=6", mismatch)
	}
}

func TestSpliceWithOptions_AllowDurationMismatch(t *testing.T) {
	content := contentPlaylist() // 10s break
	ad := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "ad_000.ts", Duration: 5},
			{URI: "ad_001.ts", Duration: 5},
			{URI: "ad_002.ts", Duration: 5}, // 15s total ad vs. 10s break: a real mismatch
		},
	}

	out, err := SpliceWithOptions(content, ad, Options{AllowDurationMismatch: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v, want success with AllowDurationMismatch", err)
	}

	wantURIs := []string{"seg_000.ts", "seg_001.ts", "seg_002.ts", "ad_000.ts", "ad_001.ts", "ad_002.ts", "seg_004.ts", "seg_005.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d: %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}

	// The manifest grows to fit the longer ad (65s total vs. original 60s) —
	// this is the "VOD manifests can grow freely" behavior, not a bug.
	if got, want := out.TotalDuration(), 65.0; got != want {
		t.Errorf("TotalDuration() = %v, want %v", got, want)
	}
}

func TestSplice_DurationWithinEpsilonIsAccepted(t *testing.T) {
	content := contentPlaylist() // 10s break
	ad := &manifest.Playlist{
		Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 9.95}}, // encoder rounding, not a real mismatch
	}

	if _, err := Splice(content, ad); err != nil {
		t.Fatalf("Splice() error = %v, want success (within tolerance)", err)
	}
}

func TestSplice_NoBreakInContentIsRejected(t *testing.T) {
	content := &manifest.Playlist{
		Segments: []manifest.Segment{{URI: "seg_000.ts", Duration: 10}},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	_, err := Splice(content, ad)
	if !errors.Is(err, ErrNoAdBreak) {
		t.Fatalf("Splice() error = %v, want wrapping ErrNoAdBreak", err)
	}
}

func TestSplice_UnmatchedCueOutIsRejected(t *testing.T) {
	content := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10},
			{URI: "seg_001.ts", Duration: 10, CueOut: true},
			{URI: "seg_002.ts", Duration: 10}, // no CueIn anywhere after
		},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	_, err := Splice(content, ad)
	if !errors.Is(err, ErrNoAdBreak) {
		t.Fatalf("Splice() error = %v, want wrapping ErrNoAdBreak", err)
	}
}

func TestSplice_MultipleBreaks(t *testing.T) {
	content := &manifest.Playlist{
		Version: 3, TargetDuration: 10, PlaylistType: "VOD", EndList: true,
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10},
			{URI: "seg_001.ts", Duration: 10, CueOut: true},
			{URI: "seg_002.ts", Duration: 10, CueIn: true},
			{URI: "seg_003.ts", Duration: 10},
			{URI: "seg_004.ts", Duration: 10, CueOut: true},
			{URI: "seg_005.ts", Duration: 10, CueIn: true},
			{URI: "seg_006.ts", Duration: 10},
		},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	out, err := Splice(content, ad)
	if err != nil {
		t.Fatalf("Splice() error = %v", err)
	}

	// seg_002.ts/seg_005.ts are each break's own CueIn-marked "resume"
	// segment — kept as real content (cue flags cleared), not removed;
	// same as the single-break case's own resume segment.
	wantURIs := []string{"seg_000.ts", "ad_000.ts", "seg_002.ts", "seg_003.ts", "ad_000.ts", "seg_005.ts", "seg_006.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d: %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}
	// Both ad instances (index 1 and 4) and each break's own resume
	// segment right after (index 2 and 5) must carry the discontinuity
	// marking their own transition — not just the first break's.
	for _, i := range []int{1, 2, 4, 5} {
		if !out.Segments[i].Discontinuity {
			t.Errorf("Segments[%d] (%s) should have Discontinuity=true", i, out.Segments[i].URI)
		}
	}
	for _, i := range []int{0, 3, 6} {
		if out.Segments[i].Discontinuity {
			t.Errorf("Segments[%d] (%s) should not have Discontinuity=true", i, out.Segments[i].URI)
		}
	}
}

func TestSplice_MultipleBreaks_OneMismatchedStillReportsWhichIndex(t *testing.T) {
	content := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10, CueOut: true},
			{URI: "seg_001.ts", Duration: 10, CueIn: true},
			{URI: "seg_002.ts", Duration: 6, CueOut: true}, // break 1 (index 1): a real mismatch — CueIn itself isn't part of the break span, only what's strictly between CueOut and CueIn is
			{URI: "seg_003.ts", Duration: 10, CueIn: true},
		},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	_, err := Splice(content, ad)
	var mismatch *DurationMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Splice() error = %v (%T), want *DurationMismatchError", err, err)
	}
	if mismatch.BreakIndex != 1 {
		t.Errorf("mismatch.BreakIndex = %d, want 1 (the second break, index 0-based)", mismatch.BreakIndex)
	}
}

func TestSpliceWithOptions_LoopAdToFillBreak(t *testing.T) {
	content := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10, CueOut: true},
			{URI: "seg_001.ts", Duration: 10},
			{URI: "seg_002.ts", Duration: 10},
			{URI: "seg_003.ts", Duration: 10, CueIn: true}, // break span [0,3): 30s
		},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10.033}}} // real ad-clip-shaped duration

	out, err := SpliceWithOptions(content, ad, Options{LoopAdToFillBreak: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v", err)
	}

	// 3 loops to cover a 30s break with a ~10.033s ad (3*10.033 >= 30),
	// then the break's own resume segment (seg_003.ts, its CueIn cleared).
	wantURIs := []string{"ad_000.ts", "ad_000.ts", "ad_000.ts", "seg_003.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d: %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}
	for _, i := range []int{0, 1, 2} {
		if !out.Segments[i].Discontinuity {
			t.Errorf("Segments[%d]: every loop repeat needs its own Discontinuity (PTS restarts each time)", i)
		}
	}
}

func TestSpliceWithOptions_LoopAdToFillBreak_NoOpWhenAlreadyLongEnough(t *testing.T) {
	content := contentPlaylist() // 10s break
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	out, err := SpliceWithOptions(content, ad, Options{LoopAdToFillBreak: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v", err)
	}
	// Exactly one ad segment in the output — looping shouldn't trigger
	// when the ad already covers the break.
	adCount := 0
	for _, s := range out.Segments {
		if s.URI == "ad_000.ts" {
			adCount++
		}
	}
	if adCount != 1 {
		t.Errorf("ad segment appears %d times, want 1 (no loop needed)", adCount)
	}
}

func TestSpliceWithOptions_PreserveAllContent(t *testing.T) {
	// CueOut/CueIn are adjacent (indices 2,3) — the shape
	// internal/contentprep.InjectBreaks authors under this mode: no
	// placeholder range at all, just an insertion point.
	content := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10},
			{URI: "seg_001.ts", Duration: 10},
			{URI: "seg_002.ts", Duration: 10, CueOut: true, CueOutDuration: 10},
			{URI: "seg_003.ts", Duration: 10, CueIn: true},
			{URI: "seg_004.ts", Duration: 10},
		},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	out, err := SpliceWithOptions(content, ad, Options{PreserveAllContent: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v", err)
	}

	// Every original segment must still be present — nothing removed —
	// plus the ad inserted right after seg_002.ts.
	wantURIs := []string{"seg_000.ts", "seg_001.ts", "seg_002.ts", "ad_000.ts", "seg_003.ts", "seg_004.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d: %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}

	if got, want := out.TotalDuration(), content.TotalDuration()+ad.TotalDuration(); got != want {
		t.Errorf("TotalDuration() = %v, want %v (original content's duration plus the ad's — nothing displaced)", got, want)
	}

	// seg_002.ts (was CueOut) stays real content, cue flag cleared, no
	// discontinuity of its own (still the same original bitstream up to
	// that point).
	if out.Segments[2].CueOut || out.Segments[2].Discontinuity {
		t.Errorf("Segments[2] (seg_002.ts) = %+v, want CueOut=false Discontinuity=false", out.Segments[2])
	}
	// ad_000.ts: entering the ad's bitstream.
	if !out.Segments[3].Discontinuity {
		t.Errorf("Segments[3] (ad_000.ts) should have Discontinuity=true")
	}
	// seg_003.ts (was CueIn) stays real content, cue flag cleared,
	// discontinuity marks the return to the original bitstream.
	if out.Segments[4].CueIn || !out.Segments[4].Discontinuity {
		t.Errorf("Segments[4] (seg_003.ts) = %+v, want CueIn=false Discontinuity=true", out.Segments[4])
	}
}

func TestSpliceWithOptions_PreserveAllContent_LoopsToCueOutDuration(t *testing.T) {
	content := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10, CueOut: true, CueOutDuration: 30},
			{URI: "seg_001.ts", Duration: 10, CueIn: true},
		},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10.033}}}

	out, err := SpliceWithOptions(content, ad, Options{PreserveAllContent: true, LoopAdToFillBreak: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v", err)
	}

	wantURIs := []string{"seg_000.ts", "ad_000.ts", "ad_000.ts", "ad_000.ts", "seg_001.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d (3 ad loops to reach the 30s CueOutDuration target): %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}
}

func TestSpliceWithOptions_PreserveAllContent_MultipleBreaks(t *testing.T) {
	content := &manifest.Playlist{
		Segments: []manifest.Segment{
			{URI: "seg_000.ts", Duration: 10},
			{URI: "seg_001.ts", Duration: 10, CueOut: true, CueOutDuration: 10},
			{URI: "seg_002.ts", Duration: 10, CueIn: true},
			{URI: "seg_003.ts", Duration: 10, CueOut: true, CueOutDuration: 10},
			{URI: "seg_004.ts", Duration: 10, CueIn: true},
		},
	}
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	out, err := SpliceWithOptions(content, ad, Options{PreserveAllContent: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v", err)
	}

	wantURIs := []string{"seg_000.ts", "seg_001.ts", "ad_000.ts", "seg_002.ts", "seg_003.ts", "ad_000.ts", "seg_004.ts"}
	if len(out.Segments) != len(wantURIs) {
		t.Fatalf("got %d segments, want %d: %+v", len(out.Segments), len(wantURIs), out.Segments)
	}
	for i, want := range wantURIs {
		if out.Segments[i].URI != want {
			t.Errorf("Segments[%d].URI = %q, want %q", i, out.Segments[i].URI, want)
		}
	}
	if got, want := out.TotalDuration(), content.TotalDuration()+2*ad.TotalDuration(); got != want {
		t.Errorf("TotalDuration() = %v, want %v (all 5 original segments plus both ad insertions)", got, want)
	}
}

func TestSplice_DoesNotMutateInputs(t *testing.T) {
	content := contentPlaylist()
	contentCopy := contentPlaylist()
	ad := &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}

	if _, err := Splice(content, ad); err != nil {
		t.Fatalf("Splice() error = %v", err)
	}
	for i := range content.Segments {
		if content.Segments[i] != contentCopy.Segments[i] {
			t.Errorf("content.Segments[%d] mutated: got %+v, want %+v", i, content.Segments[i], contentCopy.Segments[i])
		}
	}
}
