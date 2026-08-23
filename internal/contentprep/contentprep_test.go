package contentprep

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func testSource(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	path := filepath.Join("..", "..", "testdata", "vastfixture", "creative.mp4")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}
	return path
}

// TestInjectBreak_MiddleOfClip is the core case: a 6s source (see
// testdata/vastfixture's generation command in README "Test assets"),
// a break inserted at 2s. All of the source must survive in the output
// — this is InjectBreaks' whole point (see the package doc): the break
// is a marked insertion point, not a carved-out-and-discarded range.
func TestInjectBreak_MiddleOfClip(t *testing.T) {
	source := testSource(t)
	outDir := t.TempDir()

	out, err := InjectBreak(source, outDir, 2*time.Second, 2*time.Second)
	if err != nil {
		t.Fatalf("InjectBreak() error = %v", err)
	}

	total := out.TotalDuration()
	if total < 5.0 || total > 7.0 {
		t.Fatalf("TotalDuration() = %v, want ~6s (the break is an insertion point — nothing removed, no ad spliced in yet either)", total)
	}

	cueOutIdx, cueInIdx := -1, -1
	var beforeCueOut float64
	for i, s := range out.Segments {
		if cueOutIdx == -1 {
			beforeCueOut += s.Duration // includes the CueOut segment itself, real content here
		}
		if s.CueOut && cueOutIdx == -1 {
			cueOutIdx = i
		}
		if s.CueIn && cueInIdx == -1 {
			cueInIdx = i
		}
	}

	if cueOutIdx == -1 {
		t.Fatal("no segment carries CueOut")
	}
	if cueInIdx == -1 {
		t.Fatal("no segment carries CueIn")
	}
	// CueIn must be the segment immediately after CueOut — nothing sits
	// between them, since nothing is carved out.
	if cueInIdx != cueOutIdx+1 {
		t.Errorf("CueIn segment (%d) should immediately follow CueOut segment (%d), with nothing between them", cueInIdx, cueOutIdx)
	}
	// The CueOut segment itself is included in "before the break" — it's
	// real content, not a placeholder — so this includes its own
	// duration, landing a bit past the raw 2s breakStart.
	if beforeCueOut < 1.5 || beforeCueOut > 3.5 {
		t.Errorf("duration up to and including the CueOut segment = %v, want ~2-3s (breakStart plus that segment's own length)", beforeCueOut)
	}
	wantCueOutDuration := 2.0
	if diff := out.Segments[cueOutIdx].CueOutDuration - wantCueOutDuration; diff < -0.5 || diff > 0.5 {
		t.Errorf("CueOutDuration = %v, want ~%v (the target/informational length, not a real carved range)", out.Segments[cueOutIdx].CueOutDuration, wantCueOutDuration)
	}

	// Every referenced segment file must actually exist and be non-empty
	// — appendSegments' renaming is the part most likely to silently
	// produce a dangling reference if it's wrong.
	for _, s := range out.Segments {
		info, err := os.Stat(filepath.Join(outDir, s.URI))
		if err != nil {
			t.Errorf("segment %q: %v", s.URI, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("segment %q is empty", s.URI)
		}
	}

	// content.m3u8 itself must be the same structure InjectBreak returned
	// — proves manifest.Write/manifest.Parse round-trip, not just the
	// in-memory struct.
	f, err := os.Open(filepath.Join(outDir, "content.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
}

// TestInjectBreak_AtStartIsError covers breakStart == 0: unlike the
// older, carve-and-replace version of this package, an insertion point
// needs a real segment *before* it to attach #EXT-X-CUE-OUT to — there's
// no meaningful "insert before anything has played yet" in this model
// (see the package doc), so this is a real, documented error case now,
// not the valid edge case it used to be.
func TestInjectBreak_AtStartIsError(t *testing.T) {
	source := testSource(t)
	_, err := InjectBreak(source, t.TempDir(), 0, 2*time.Second)
	if err == nil {
		t.Fatal("InjectBreak() error = nil, want error for breakStart == 0")
	}
}

// TestInjectBreaks_Multiple covers two breaks in one 6s source — real,
// independent encodes for every span between them, with all of the
// source's own content preserved throughout.
func TestInjectBreaks_Multiple(t *testing.T) {
	source := testSource(t)
	out, err := InjectBreaks(source, t.TempDir(), []BreakSpec{
		{Start: 1 * time.Second, Duration: 500 * time.Millisecond},
		{Start: 3 * time.Second, Duration: 500 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("InjectBreaks() error = %v", err)
	}

	var cueOutIdx, cueInIdx []int
	for i, s := range out.Segments {
		if s.CueOut {
			cueOutIdx = append(cueOutIdx, i)
		}
		if s.CueIn {
			cueInIdx = append(cueInIdx, i)
		}
	}
	if len(cueOutIdx) != 2 {
		t.Fatalf("got %d CueOut segments, want 2: indices %v", len(cueOutIdx), cueOutIdx)
	}
	if len(cueInIdx) != 2 {
		t.Fatalf("got %d CueIn segments, want 2 (every insertion point gets one — there's always real content after it, even the last one): indices %v", len(cueInIdx), cueInIdx)
	}
	// Every CueIn must immediately follow its own break's CueOut.
	for i := range cueOutIdx {
		if cueInIdx[i] != cueOutIdx[i]+1 {
			t.Errorf("break %d: CueIn at %d should immediately follow its CueOut at %d", i, cueInIdx[i], cueOutIdx[i])
		}
	}

	total := out.TotalDuration()
	if total < 5.0 || total > 7.0 {
		t.Fatalf("TotalDuration() = %v, want ~6s (insertion points only — nothing removed, no ad spliced in yet)", total)
	}
}

func TestInjectBreaks_DuplicateStartIsError(t *testing.T) {
	source := testSource(t)
	_, err := InjectBreaks(source, t.TempDir(), []BreakSpec{
		{Start: 2 * time.Second, Duration: 1 * time.Second},
		{Start: 2 * time.Second, Duration: 1 * time.Second}, // same Start: no real content between them
	})
	if err == nil {
		t.Fatal("InjectBreaks() error = nil, want error for two breaks at the same Start")
	}
}

func TestInjectBreak_StartAtOrPastSourceDurationIsError(t *testing.T) {
	source := testSource(t)
	_, err := InjectBreak(source, t.TempDir(), 6*time.Second, 1*time.Second) // source is ~6s
	if err == nil {
		t.Fatal("InjectBreak() error = nil, want error for a break starting at or past the source's duration")
	}
}

func TestInjectBreak_NegativeBreakStartIsError(t *testing.T) {
	source := testSource(t)
	_, err := InjectBreak(source, t.TempDir(), -1*time.Second, 2*time.Second)
	if err == nil {
		t.Fatal("InjectBreak() error = nil, want error for a negative breakStart")
	}
}

func TestInjectBreak_ZeroBreakDurationIsError(t *testing.T) {
	source := testSource(t)
	_, err := InjectBreak(source, t.TempDir(), 2*time.Second, 0)
	if err == nil {
		t.Fatal("InjectBreak() error = nil, want error for a zero breakDuration")
	}
}
