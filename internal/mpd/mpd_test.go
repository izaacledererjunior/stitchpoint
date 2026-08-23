package mpd

import (
	"strings"
	"testing"

	"github.com/izaacledererjunior/stitchpoint/internal/scte35"
)

// awsSpliceInsertExample and awsTimeSignalExample are the two DASH MPD
// Period fragments AWS's own MediaTailor documentation publishes as
// real-world examples of SCTE-35 EventStream signaling — copied
// verbatim, not invented, the same discipline internal/scte35's own
// tests apply (a real AWS MediaConvert cue string) and internal/hls's
// integration test applies (a self-authored but spec-conformant stream).
// Source: https://docs.aws.amazon.com/mediatailor/latest/ug/manifest-dash-example.html
const awsSpliceInsertExample = `<MPD type="static">
<Period start="PT173402.036S" id="46041">
  <EventStream timescale="90000" schemeIdUri="urn:scte:scte35:2013:xml">
    <Event duration="9450000">
      <scte35:SpliceInfoSection protocolVersion="0" ptsAdjustment="183265" tier="4095">
        <scte35:SpliceInsert spliceEventId="99" spliceEventCancelIndicator="false" outOfNetworkIndicator="true" spliceImmediateFlag="false" uniqueProgramId="1" availNum="1" availsExpected="1">
          <scte35:Program><scte35:SpliceTime ptsTime="7835775000"/></scte35:Program>
          <scte35:BreakDuration autoReturn="true" duration="9450000"/>
        </scte35:SpliceInsert>
        <scte35:SegmentationDescriptor segmentationEventId="99" segmentationEventCancelIndicator="false" segmentationDuration="9450000">
          <scte35:DeliveryRestrictions webDeliveryAllowedFlag="true" noRegionalBlackoutFlag="true" archiveAllowedFlag="true" deviceRestrictions="3"/>
          <scte35:SegmentationUpid segmentationUpidType="8" segmentationUpidLength="0"/>
          <scte35:SegmentationTypeID segmentationType="52"/>
          <scte35:SegmentNum segmentNum="1"/>
          <scte35:SegmentsExpected segmentsExpected="1"/>
        </scte35:SegmentationDescriptor>
      </scte35:SpliceInfoSection>
    </Event>
  </EventStream>
  <AdaptationSet mimeType="video/mp4" segmentAlignment="true" subsegmentAlignment="true" startWithSAP="1" subsegmentStartsWithSAP="1" bitstreamSwitching="true">
    <Representation id="1" width="960" height="540" frameRate="30000/1001" bandwidth="1000000" codecs="avc1.4D401F">
      <SegmentTemplate timescale="30000" media="index_video_1_0_$Number$.mp4?m=1528475245" initialization="index_video_1_0_init.mp4?m=1528475245" startNumber="178444" presentationTimeOffset="10395907501">
        <SegmentTimeline>
          <S t="10395907501" d="60060" r="29"/>
          <S t="10397709301" d="45045"/>
        </SegmentTimeline>
      </SegmentTemplate>
    </Representation>
  </AdaptationSet>
  <AdaptationSet mimeType="audio/mp4" segmentAlignment="0" lang="eng">
    <Representation id="2" bandwidth="96964" audioSamplingRate="48000" codecs="mp4a.40.2">
      <SegmentTemplate timescale="48000" media="index_audio_2_0_$Number$.mp4?m=1528475245" initialization="index_audio_2_0_init.mp4?m=1528475245" startNumber="178444" presentationTimeOffset="16633452001">
        <SegmentTimeline>
          <S t="16633452289" d="96256" r="3"/>
          <S t="16633837313" d="95232"/>
        </SegmentTimeline>
      </SegmentTemplate>
    </Representation>
  </AdaptationSet>
</Period>
</MPD>`

const awsTimeSignalExample = `<MPD type="static">
<Period start="PT173402.036S" id="46041">
  <EventStream timescale="90000" schemeIdUri="urn:scte:scte35:2013:xml">
    <Event duration="9450000">
      <scte35:SpliceInfoSection protocolVersion="0" ptsAdjustment="183265" tier="4095">
        <scte35:TimeSignal>
          <scte35:SpliceTime ptsTime="7835775000"/>
        </scte35:TimeSignal>
        <scte35:SegmentationDescriptor segmentationEventId="99" segmentationEventCancelIndicator="false" segmentationDuration="9450000">
          <scte35:DeliveryRestrictions webDeliveryAllowedFlag="true" noRegionalBlackoutFlag="true" archiveAllowedFlag="true" deviceRestrictions="3"/>
          <scte35:SegmentationUpid segmentationUpidType="8" segmentationUpidLength="0"/>
          <scte35:SegmentationTypeID segmentationType="52"/>
          <scte35:SegmentNum segmentNum="1"/>
          <scte35:SegmentsExpected segmentsExpected="1"/>
        </scte35:SegmentationDescriptor>
      </scte35:SpliceInfoSection>
    </Event>
  </EventStream>
  <AdaptationSet mimeType="video/mp4" segmentAlignment="true" subsegmentAlignment="true" startWithSAP="1" subsegmentStartsWithSAP="1" bitstreamSwitching="true">
    <Representation id="1" width="960" height="540" frameRate="30000/1001" bandwidth="1000000" codecs="avc1.4D401F">
      <SegmentTemplate timescale="30000" media="index_video_1_0_$Number$.mp4?m=1528475245" initialization="index_video_1_0_init.mp4?m=1528475245" startNumber="178444" presentationTimeOffset="10395907501">
        <SegmentTimeline>
          <S t="10395907501" d="60060" r="29"/>
          <S t="10397709301" d="45045"/>
        </SegmentTimeline>
      </SegmentTemplate>
    </Representation>
  </AdaptationSet>
</Period>
</MPD>`

func TestParse_RealAWSExample_SegmentStructure(t *testing.T) {
	m, err := Parse(strings.NewReader(awsSpliceInsertExample))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(m.Periods) != 1 {
		t.Fatalf("len(Periods) = %d, want 1", len(m.Periods))
	}
	period := m.Periods[0]
	if period.ID != "46041" {
		t.Errorf("Period.ID = %q, want %q", period.ID, "46041")
	}
	if len(period.AdaptationSets) != 2 {
		t.Fatalf("len(AdaptationSets) = %d, want 2 (video+audio)", len(period.AdaptationSets))
	}

	video := period.AdaptationSets[0]
	if video.MimeType != "video/mp4" {
		t.Errorf("video AdaptationSet.MimeType = %q, want video/mp4", video.MimeType)
	}
	rep := video.Representations[0]
	if rep.Width != 960 || rep.Height != 540 {
		t.Errorf("video Representation dimensions = %dx%d, want 960x540", rep.Width, rep.Height)
	}
	tmpl := rep.EffectiveSegmentTemplate(video)
	if tmpl == nil {
		t.Fatal("EffectiveSegmentTemplate() = nil")
	}
	if len(tmpl.SegmentTimeline.S) != 2 {
		t.Fatalf("len(SegmentTimeline.S) = %d, want 2", len(tmpl.SegmentTimeline.S))
	}
	// First S entry: t=10395907501, d=60060, r=29 -> 30 segments at that
	// duration (r=29 means 29 *additional* repeats beyond the first).
	first := tmpl.SegmentTimeline.S[0]
	if first.T == nil || *first.T != 10395907501 {
		t.Errorf("first S.T = %v, want 10395907501", first.T)
	}
	if first.D != 60060 || first.R != 29 {
		t.Errorf("first S = {D:%d R:%d}, want {D:60060 R:29}", first.D, first.R)
	}
}

func TestExtractCues_RealAWSExample_SpliceInsert(t *testing.T) {
	m, err := Parse(strings.NewReader(awsSpliceInsertExample))
	if err != nil {
		t.Fatal(err)
	}
	cues, err := ExtractCues(m)
	if err != nil {
		t.Fatalf("ExtractCues() error = %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("len(cues) = %d, want 1", len(cues))
	}
	cue := cues[0]

	if cue.PeriodID != "46041" {
		t.Errorf("PeriodID = %q, want %q", cue.PeriodID, "46041")
	}
	// Event duration=9450000 at EventStream timescale=90000 -> 105s.
	wantDuration := 105_000_000_000 // 105s in nanoseconds
	if int(cue.Duration) != wantDuration {
		t.Errorf("Duration = %v, want 105s", cue.Duration)
	}
	if cue.SpliceInfoSection.SpliceCommandType != scte35.CommandSpliceInsert {
		t.Errorf("SpliceCommandType = %v, want CommandSpliceInsert", cue.SpliceInfoSection.SpliceCommandType)
	}
}

func TestExtractCues_RealAWSExample_SpliceInsert_Describe(t *testing.T) {
	m, err := Parse(strings.NewReader(awsSpliceInsertExample))
	if err != nil {
		t.Fatal(err)
	}
	cues, err := ExtractCues(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 1 {
		t.Fatalf("len(cues) = %d, want 1", len(cues))
	}

	// The real test: feed the reconstructed SpliceInfoSection through
	// scte35.Describe(), the project's own real consumer, exactly the
	// same way internal/hls's cues get described — not just asserting
	// on this package's own intermediate fields.
	got := scte35.Describe(cues[0].SpliceInfoSection)
	// ptsTime=7835775000 / 90000 = 87064.1666...s; duration=9450000/90000=105s.
	if !strings.Contains(got, "splice_insert event=99 CUE-OUT") {
		t.Errorf("Describe() = %q, want it to contain \"splice_insert event=99 CUE-OUT\"", got)
	}
	if !strings.Contains(got, "duration=1m45s") {
		t.Errorf("Describe() = %q, want it to contain \"duration=1m45s\" (9450000/90000 ticks)", got)
	}
}

func TestExtractCues_RealAWSExample_TimeSignal(t *testing.T) {
	m, err := Parse(strings.NewReader(awsTimeSignalExample))
	if err != nil {
		t.Fatal(err)
	}
	cues, err := ExtractCues(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 1 {
		t.Fatalf("len(cues) = %d, want 1", len(cues))
	}
	got := scte35.Describe(cues[0].SpliceInfoSection)
	if !strings.Contains(got, "time_signal") {
		t.Errorf("Describe() = %q, want it to contain \"time_signal\"", got)
	}
}

func TestExtractCues_IgnoresOtherSchemes(t *testing.T) {
	doc := `<MPD type="static"><Period id="1">
	  <EventStream schemeIdUri="urn:mpeg:dash:event:2012" timescale="1">
	    <Event id="x" presentationTime="0" duration="10"/>
	  </EventStream>
	</Period></MPD>`
	m, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	cues, err := ExtractCues(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 0 {
		t.Errorf("len(cues) = %d, want 0 (non-SCTE-35 scheme should be ignored)", len(cues))
	}
}

func TestExtractCues_NoEventStreams(t *testing.T) {
	m, err := Parse(strings.NewReader(`<MPD type="static"><Period id="1"></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	cues, err := ExtractCues(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 0 {
		t.Errorf("len(cues) = %d, want 0", len(cues))
	}
}
