package hls

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/izaacledererjunior/stitchpoint/internal/scte35"
)

// TestExtractCues_RealTestStream is Phase 1's end-to-end proof: it runs
// cue extraction against the checked-in, self-authored VOD test asset
// (testdata/vod/content/content.m3u8 — see README "Test assets" for how it
// was generated) and asserts on the exact ad breaks found, rather than
// just "it didn't crash." This is what actually demonstrates Phase 1's
// done-criteria — identifying and printing all ad breaks in a known test
// stream with real SCTE-35 markers — as opposed to the unit tests, which
// only prove the parser matches the spec on synthetic vectors.
func TestExtractCues_RealTestStream(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "vod", "content", "content.m3u8")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open test stream: %v (see README Test assets to regenerate)", err)
	}
	defer f.Close()

	cues, err := ExtractCues(f)
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}

	wantEvents := []struct {
		eventID      uint32
		outOfNetwork bool
		ptsSeconds   float64
		hasDuration  bool
		durSeconds   float64
	}{
		{eventID: 100, outOfNetwork: true, ptsSeconds: 30, hasDuration: true, durSeconds: 10},
		{eventID: 101, outOfNetwork: false, ptsSeconds: 40},
	}

	if len(cues) != len(wantEvents) {
		t.Fatalf("got %d cues, want %d: %+v", len(cues), len(wantEvents), cues)
	}

	for i, cue := range cues {
		if cue.Tag != TagOATCLSSCTE35 {
			t.Errorf("cue %d: Tag = %s, want %s", i, cue.Tag, TagOATCLSSCTE35)
		}

		section, err := cue.Decode()
		if err != nil {
			t.Fatalf("cue %d: Decode() error = %v", i, err)
		}
		si, ok := section.SpliceCommand.(scte35.SpliceInsert)
		if !ok {
			t.Fatalf("cue %d: SpliceCommand type = %T, want scte35.SpliceInsert", i, section.SpliceCommand)
		}

		want := wantEvents[i]
		if si.SpliceEventID != want.eventID {
			t.Errorf("cue %d: SpliceEventID = %d, want %d", i, si.SpliceEventID, want.eventID)
		}
		if si.OutOfNetworkIndicator != want.outOfNetwork {
			t.Errorf("cue %d: OutOfNetworkIndicator = %v, want %v", i, si.OutOfNetworkIndicator, want.outOfNetwork)
		}
		if si.SpliceTime == nil || si.SpliceTime.Duration().Seconds() != want.ptsSeconds {
			t.Errorf("cue %d: PTS = %v, want %vs", i, si.SpliceTime, want.ptsSeconds)
		}
		if want.hasDuration {
			if si.BreakDuration == nil || si.BreakDuration.AsDuration().Seconds() != want.durSeconds {
				t.Errorf("cue %d: BreakDuration = %v, want %vs", i, si.BreakDuration, want.durSeconds)
			}
		} else if si.DurationFlag {
			t.Errorf("cue %d: expected no duration (cue-in), got DurationFlag=true", i)
		}
	}
}
