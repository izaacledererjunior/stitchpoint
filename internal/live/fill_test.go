package live

import (
	"testing"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
)

func segs(durations ...float64) []manifest.Segment {
	out := make([]manifest.Segment, len(durations))
	for i, d := range durations {
		out[i] = manifest.Segment{URI: "seg.ts", Duration: d}
	}
	return out
}

func TestLoopFiller_Fill(t *testing.T) {
	tests := []struct {
		name       string
		segs       []manifest.Segment
		actual     time.Duration
		target     time.Duration
		wantCount  int  // total segments in the result
		wantLooped bool // true if the result is longer than the input
	}{
		{
			name:       "already meets target: returned unchanged",
			segs:       segs(6, 6),
			actual:     12 * time.Second,
			target:     10 * time.Second,
			wantCount:  2,
			wantLooped: false,
		},
		{
			name:       "underfilled: loops until target covered",
			segs:       segs(6),
			actual:     6 * time.Second,
			target:     20 * time.Second,
			wantCount:  4, // 6+6+6+6 = 24s >= 20s
			wantLooped: true,
		},
		{
			name:       "empty input: nothing to loop",
			segs:       nil,
			actual:     0,
			target:     30 * time.Second,
			wantCount:  0,
			wantLooped: false,
		},
		{
			name:       "zero actual duration: refuses to loop (would never terminate)",
			segs:       segs(0),
			actual:     0,
			target:     30 * time.Second,
			wantCount:  1,
			wantLooped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := LoopFiller{}.Fill(tt.segs, tt.actual, tt.target)
			if len(out) != tt.wantCount {
				t.Fatalf("len(out) = %d, want %d", len(out), tt.wantCount)
			}
			looped := len(out) > len(tt.segs)
			if looped != tt.wantLooped {
				t.Fatalf("looped = %v, want %v", looped, tt.wantLooped)
			}
			if looped {
				// Every repeat boundary after the first must carry a
				// discontinuity — same reasoning as spliceAd's own first
				// segment: a real player needs the marker whenever PTS
				// isn't continuous, which looping the same encode always
				// causes.
				for i := len(tt.segs); i < len(out); i += len(tt.segs) {
					if !out[i].Discontinuity {
						t.Errorf("out[%d]: loop repeat boundary missing Discontinuity", i)
					}
				}
			}
		})
	}
}

func TestLoopFiller_Fill_CapsRepeats(t *testing.T) {
	// A 1s creative against a very long target must not produce an
	// unbounded segment list.
	out := LoopFiller{}.Fill(segs(1), time.Second, time.Hour)
	if len(out) != maxLoopRepeats+1 {
		t.Fatalf("len(out) = %d, want %d (maxLoopRepeats+1 original)", len(out), maxLoopRepeats+1)
	}
}

// TestLoopFiller_Fill_UniqueURIsPerRepeat is a regression test: a player
// that tracks segments by URI (Shaka Player included — confirmed against
// a real deployment, ad playback stalling right at a loop boundary) can't
// distinguish one repeat of the same creative from the next if every
// repeat reuses the exact same URI. Each repeat must get a distinct
// reference even though the underlying file (and the real HTTP request
// once a client strips the fragment) is the same.
func TestLoopFiller_Fill_UniqueURIsPerRepeat(t *testing.T) {
	out := LoopFiller{}.Fill(segs(6), 6*time.Second, 20*time.Second)
	seen := make(map[string]bool, len(out))
	for i, s := range out {
		if seen[s.URI] {
			t.Fatalf("out[%d]: duplicate URI %q — a player tracking segments by URI can't tell repeats apart", i, s.URI)
		}
		seen[s.URI] = true
	}
}

func TestLoopFiller_Fill_DoesNotMutateInput(t *testing.T) {
	in := segs(6)
	inCopy := append([]manifest.Segment(nil), in...)
	_ = LoopFiller{}.Fill(in, 6*time.Second, 20*time.Second)
	if in[0] != inCopy[0] {
		t.Fatalf("Fill mutated its input slice: got %+v, want %+v", in[0], inCopy[0])
	}
}
