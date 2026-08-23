package dashsplice

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
)

// contentXML builds a single-Period content MPD (one video
// AdaptationSet, 10x1s segments, timescale 60000) with a minimal
// time_signal-style SCTE-35 cue signaling a break at
// [breakStartSec, breakStartSec+breakDurSec) — via a real XML document
// through mpd.Parse, the same way mpd_test.go's own fixtures work,
// since the cue-carrying XML struct type is unexported outside package
// mpd. A time_signal is used (not splice_insert) since dashsplice only
// reads Event/@presentationTime and Event/@duration — the splice
// command's own semantics don't matter to it (see package doc).
func contentXML(breakStartSec, breakDurSec uint64) string {
	return fmt.Sprintf(`<MPD type="static">
  <Period id="p0">
    <EventStream timescale="1" schemeIdUri="urn:scte:scte35:2013:xml">
      <Event presentationTime="%d" duration="%d">
        <scte35:SpliceInfoSection protocolVersion="0">
          <scte35:TimeSignal><scte35:SpliceTime ptsTime="0"/></scte35:TimeSignal>
        </scte35:SpliceInfoSection>
      </Event>
    </EventStream>
    <AdaptationSet contentType="video">
      <Representation id="1">
        <SegmentTemplate timescale="60000" startNumber="1" media="v_$Number$.m4s">
          <SegmentTimeline><S t="0" d="60000" r="9"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`, breakStartSec, breakDurSec)
}

func mustParseMPD(t *testing.T, xmlStr string) *mpd.MPD {
	t.Helper()
	m, err := mpd.Parse(strings.NewReader(xmlStr))
	if err != nil {
		t.Fatalf("mpd.Parse() error = %v", err)
	}
	return m
}

// adXML builds a single-Period ad MPD of n seconds — mirrors the shape
// transcode.EncodeDASH's output has (a fresh, zero-based timeline).
func adXML(seconds int) string {
	return fmt.Sprintf(`<MPD type="static">
  <Period id="ad0">
    <AdaptationSet contentType="video">
      <Representation id="1">
        <SegmentTemplate timescale="60000" startNumber="1" media="ad_$Number$.m4s">
          <SegmentTimeline><S t="0" d="60000" r="%d"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`, seconds-1)
}

func TestSplice(t *testing.T) {
	content := mustParseMPD(t, contentXML(3, 4)) // break: seconds [3,7)
	ad := mustParseMPD(t, adXML(4))              // matches exactly

	out, err := Splice(content, ad)
	if err != nil {
		t.Fatalf("Splice() error = %v", err)
	}

	if len(out.Periods) != 3 {
		t.Fatalf("got %d periods, want 3 (before/ad/after)", len(out.Periods))
	}
	before, adOut, after := out.Periods[0], out.Periods[1], out.Periods[2]

	beforeTpl := before.AdaptationSets[0].Representations[0].SegmentTemplate
	if got := len(expandTimeline(beforeTpl.SegmentTimeline)); got != 3 {
		t.Errorf("before period: %d segments, want 3", got)
	}

	if adOut.Start != "PT3S" {
		t.Errorf("ad period Start = %q, want %q", adOut.Start, "PT3S")
	}
	adTpl := adOut.AdaptationSets[0].Representations[0].SegmentTemplate
	if got := len(expandTimeline(adTpl.SegmentTimeline)); got != 4 {
		t.Errorf("ad period: %d segments, want 4", got)
	}

	if after.Start != "PT7S" {
		t.Errorf("after period Start = %q, want %q", after.Start, "PT7S")
	}
	afterTpl := after.AdaptationSets[0].Representations[0].SegmentTemplate
	afterEntries := expandTimeline(afterTpl.SegmentTimeline)
	if got := len(afterEntries); got != 3 {
		t.Errorf("after period: %d segments, want 3 (segments 7,8,9)", got)
	}
	if afterTpl.StartNumber != 8 { // original StartNumber(1) + 7 preceding segments
		t.Errorf("after period StartNumber = %d, want 8", afterTpl.StartNumber)
	}

	// Every original content segment must be accounted for exactly once
	// across before+after (none dropped, none duplicated) — plus the ad's
	// own segments, none of which alias a content segment's tick range.
	if got := len(expandTimeline(beforeTpl.SegmentTimeline)) + len(afterEntries); got != 6 {
		t.Errorf("before+after content segments = %d, want 6 (10 total - 4 in the break)", got)
	}
}

func TestSplice_NoAdBreak(t *testing.T) {
	content := mustParseMPD(t, `<MPD type="static"><Period id="p0"></Period></MPD>`)
	ad := mustParseMPD(t, adXML(4))
	if _, err := Splice(content, ad); !errors.Is(err, ErrNoAdBreak) {
		t.Fatalf("Splice() error = %v, want ErrNoAdBreak", err)
	}
}

func TestSplice_DurationMismatch(t *testing.T) {
	content := mustParseMPD(t, contentXML(3, 4)) // 4s break
	ad := mustParseMPD(t, adXML(2))              // 2s ad: mismatch

	_, err := Splice(content, ad)
	var mismatch *DurationMismatchError
	if err == nil {
		t.Fatal("Splice() error = nil, want a DurationMismatchError")
	}
	if !errors.As(err, &mismatch) {
		t.Fatalf("Splice() error = %v (%T), want *DurationMismatchError", err, err)
	}
	if mismatch.BreakDuration != 4*time.Second || mismatch.AdDuration != 2*time.Second {
		t.Errorf("mismatch = %+v, want BreakDuration=4s AdDuration=2s", mismatch)
	}
}

func TestSplice_AllowDurationMismatch(t *testing.T) {
	content := mustParseMPD(t, contentXML(3, 4))
	ad := mustParseMPD(t, adXML(2))

	out, err := SpliceWithOptions(content, ad, Options{AllowDurationMismatch: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v", err)
	}
	if len(out.Periods) != 3 {
		t.Fatalf("got %d periods, want 3", len(out.Periods))
	}
}

func TestSplice_MisalignedBreak_Refuses(t *testing.T) {
	// 3.5s doesn't land on a 1s segment boundary.
	content := mustParseMPD(t, `<MPD type="static">
  <Period id="p0">
    <EventStream timescale="1" schemeIdUri="urn:scte:scte35:2013:xml">
      <Event presentationTime="3" duration="4">
        <scte35:SpliceInfoSection protocolVersion="0">
          <scte35:TimeSignal><scte35:SpliceTime ptsTime="0"/></scte35:TimeSignal>
        </scte35:SpliceInfoSection>
      </Event>
    </EventStream>
    <AdaptationSet contentType="video">
      <Representation id="1">
        <SegmentTemplate timescale="60000" startNumber="1" media="v_$Number$.m4s">
          <SegmentTimeline><S t="0" d="70000" r="9"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`)
	ad := mustParseMPD(t, adXML(4))

	if _, err := Splice(content, ad); err == nil {
		t.Fatal("Splice() error = nil, want an error for a break that doesn't land on a segment boundary")
	}
}
