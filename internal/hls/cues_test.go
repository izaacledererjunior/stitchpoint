package hls

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// spliceInsertB64 and timeSignalB64 are real, independently-verified
// SCTE-35 messages (cross-checked against Comcast's threefive decoder):
// a splice_insert cue-out/duration pair and a
// time_signal PTS marker. timeSignalHex is derived from timeSignalB64
// rather than hand-typed, so it can't drift from a value that's actually
// been verified.
const (
	spliceInsertB64 = "/DAvAAAAAAAA///wFAVIAACPf+/+c2nALv4AUsz1AAAAAAAKAAhDVUVJAAABNWLbowo="
	timeSignalB64   = "/DAnAAAAAAAAAP/wBQb+AA27oAARAg9DVUVJAAAAAX+HCQA0AAE0xUZn"
)

var timeSignalHex = func() string {
	raw, err := base64.StdEncoding.DecodeString(timeSignalB64)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}()

func TestExtractCues_OATCLS(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-VERSION:3
#EXTINF:10.000,
seg1.ts
#EXT-OATCLS-SCTE35:` + spliceInsertB64 + `
#EXT-X-CUE-OUT:30.000
#EXTINF:2.000,
seg2.ts
`
	cues, err := ExtractCues(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1: %+v", len(cues), cues)
	}
	c := cues[0]
	if c.Tag != TagOATCLSSCTE35 || c.Line != 5 || c.Value != spliceInsertB64 {
		t.Fatalf("cue = %+v, want Tag=%s Line=5 Value=%s", c, TagOATCLSSCTE35, spliceInsertB64)
	}
	section, err := c.Decode()
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if section.SpliceCommandType != 0x05 {
		t.Errorf("SpliceCommandType = %v, want splice_insert (0x05)", section.SpliceCommandType)
	}
}

func TestExtractCues_CueOutCont(t *testing.T) {
	manifest := `#EXTM3U
#EXTINF:2.000,
seg2.ts
#EXT-X-CUE-OUT-CONT:ElapsedTime=2.000,Duration=30,SCTE35=` + spliceInsertB64 + `
#EXTINF:2.000,
seg3.ts
`
	cues, err := ExtractCues(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1: %+v", len(cues), cues)
	}
	if cues[0].Tag != TagCueOutCont || cues[0].Value != spliceInsertB64 {
		t.Fatalf("cue = %+v", cues[0])
	}
}

func TestExtractCues_DateRange(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-DATERANGE:ID="ad1",CLASS="com.example",START-DATE="2026-01-01T00:00:00Z",DURATION=30,SCTE35-OUT=0x` + timeSignalHex + `,SCTE35-CMD=0x` + timeSignalHex + `
#EXTINF:2.000,
seg1.ts
`
	cues, err := ExtractCues(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2: %+v", len(cues), cues)
	}
	seenTags := map[string]bool{}
	for _, c := range cues {
		if c.Line != 2 {
			t.Errorf("cue %+v: Line = %d, want 2", c, c.Line)
		}
		if c.Value != timeSignalHex {
			t.Errorf("cue %+v: Value mismatch (0x prefix not stripped correctly?)", c)
		}
		seenTags[c.Tag] = true
	}
	if !seenTags[TagDateRangeOut] || !seenTags[TagDateRangeCmd] {
		t.Errorf("missing expected tags, got %v", seenTags)
	}
}

func TestExtractCues_XSCTE35(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-SCTE35:CUE="` + spliceInsertB64 + `",CUE-OUT=YES
#EXTINF:2.000,
seg1.ts
`
	cues, err := ExtractCues(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 1 || cues[0].Tag != TagSCTE35 || cues[0].Value != spliceInsertB64 {
		t.Fatalf("cues = %+v", cues)
	}
}

func TestExtractCues_NoTagsIsEmptyNotError(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:10.000,\nseg1.ts\n"
	cues, err := ExtractCues(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 0 {
		t.Fatalf("got %d cues, want 0", len(cues))
	}
}

func TestExtractCues_EmptyTagValueSkipped(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-OATCLS-SCTE35:\n#EXTINF:10.000,\nseg1.ts\n"
	cues, err := ExtractCues(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 0 {
		t.Fatalf("got %d cues, want 0 (empty tag value should be skipped, not produce a broken cue)", len(cues))
	}
}

func TestExtractCues_MultipleCuesOrderedByLine(t *testing.T) {
	manifest := `#EXTM3U
#EXT-OATCLS-SCTE35:` + spliceInsertB64 + `
#EXTINF:2.000,
seg1.ts
#EXT-X-CUE-IN
#EXT-OATCLS-SCTE35:` + spliceInsertB64 + `
#EXTINF:2.000,
seg2.ts
`
	cues, err := ExtractCues(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2", len(cues))
	}
	if cues[0].Line >= cues[1].Line {
		t.Errorf("cues not in line order: %+v", cues)
	}
}
