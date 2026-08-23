package mpd

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/scte35"
)

// realCueBase64 is the same real, externally-published SCTE-35
// splice_insert cue README's own top-level usage example decodes and
// verifies (`splice_insert event=1207959695 CUE-OUT pts=5h58m34.559088888s
// duration=1m0.293566666s`) — reused here as message_data so this
// package's emsg box parsing is proven against a real, previously
// externally-validated cue payload, not an invented one, matching this
// project's established testing discipline (internal/scte35's own tests
// do the same with a real AWS MediaConvert example).
const realCueBase64 = "/DAvAAAAAAAA///wFAVIAACPf+/+c2nALv4AUsz1AAAAAAAKAAhDVUVJAAABNWLbowo="

func mustDecodeCue(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(realCueBase64)
	if err != nil {
		t.Fatalf("decoding realCueBase64: %v", err)
	}
	return b
}

// packEmsgV1 hand-packs a real, spec-conformant 'emsg' box (ISO/IEC
// 23009-1 §5.10.3.3, version 1) byte-for-byte — the same "construct the
// binary vector bit-for-bit against the spec's own syntax table" style
// internal/scte35's own tests use, rather than a checked-in opaque blob.
func packEmsgV1(t *testing.T, schemeIDURI, value string, timescale uint32, presentationTime uint64, eventDuration, id uint32, messageData []byte) []byte {
	t.Helper()
	var body bytes.Buffer
	body.WriteByte(1)           // version
	body.Write([]byte{0, 0, 0}) // flags

	var fixed [20]byte
	binary.BigEndian.PutUint32(fixed[0:4], timescale)
	binary.BigEndian.PutUint64(fixed[4:12], presentationTime)
	binary.BigEndian.PutUint32(fixed[12:16], eventDuration)
	binary.BigEndian.PutUint32(fixed[16:20], id)
	body.Write(fixed[:])

	body.WriteString(schemeIDURI)
	body.WriteByte(0)
	body.WriteString(value)
	body.WriteByte(0)
	body.Write(messageData)

	return wrapBox(t, "emsg", body.Bytes())
}

// packEmsgV0 is packEmsgV1's version-0 equivalent (field order and
// presentation_time_delta instead of presentation_time differ per spec —
// see decodeEmsg's own version switch).
func packEmsgV0(t *testing.T, schemeIDURI, value string, timescale, presentationTimeDelta, eventDuration, id uint32, messageData []byte) []byte {
	t.Helper()
	var body bytes.Buffer
	body.WriteByte(0)
	body.Write([]byte{0, 0, 0})

	body.WriteString(schemeIDURI)
	body.WriteByte(0)
	body.WriteString(value)
	body.WriteByte(0)

	var fixed [16]byte
	binary.BigEndian.PutUint32(fixed[0:4], timescale)
	binary.BigEndian.PutUint32(fixed[4:8], presentationTimeDelta)
	binary.BigEndian.PutUint32(fixed[8:12], eventDuration)
	binary.BigEndian.PutUint32(fixed[12:16], id)
	body.Write(fixed[:])
	body.Write(messageData)

	return wrapBox(t, "emsg", body.Bytes())
}

func wrapBox(t *testing.T, boxType string, body []byte) []byte {
	t.Helper()
	if len(boxType) != 4 {
		t.Fatalf("box type %q must be exactly 4 bytes", boxType)
	}
	var out bytes.Buffer
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(8+len(body)))
	out.Write(size[:])
	out.WriteString(boxType)
	out.Write(body)
	return out.Bytes()
}

func TestExtractEmsgCues_V1(t *testing.T) {
	cueBytes := mustDecodeCue(t)
	box := packEmsgV1(t, SchemeSCTE35Bin, "", 90000, 8100000, 5400000, 42, cueBytes)

	cues, err := ExtractEmsgCues(bytes.NewReader(box))
	if err != nil {
		t.Fatalf("ExtractEmsgCues() error = %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	cue := cues[0]

	if cue.ID != 42 || cue.Version != 1 {
		t.Errorf("cue = %+v, want ID=42 Version=1", cue)
	}
	wantPresentation := ticksToDurationForTest(8100000, 90000)
	if cue.PresentationTime != wantPresentation {
		t.Errorf("PresentationTime = %v, want %v", cue.PresentationTime, wantPresentation)
	}
	wantDuration := ticksToDurationForTest(5400000, 90000)
	if cue.EventDuration != wantDuration || cue.EventDurationUnknown {
		t.Errorf("EventDuration = %v (unknown=%v), want %v (unknown=false)", cue.EventDuration, cue.EventDurationUnknown, wantDuration)
	}

	got := scte35.Describe(cue.SpliceInfoSection)
	want := "splice_insert event=1207959695 CUE-OUT pts=5h58m34.559088888s duration=1m0.293566666s"
	if got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}

func TestExtractEmsgCues_V0_PresentationTimeDelta(t *testing.T) {
	cueBytes := mustDecodeCue(t)
	box := packEmsgV0(t, SchemeSCTE35Bin, "", 90000, 4500000, 5400000, 7, cueBytes)

	cues, err := ExtractEmsgCues(bytes.NewReader(box))
	if err != nil {
		t.Fatalf("ExtractEmsgCues() error = %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	if cues[0].Version != 0 || cues[0].ID != 7 {
		t.Errorf("cue = %+v, want Version=0 ID=7", cues[0])
	}
	if cues[0].SpliceInfoSection == nil {
		t.Error("SpliceInfoSection is nil")
	}
}

func TestExtractEmsgCues_UnknownDurationSentinel(t *testing.T) {
	cueBytes := mustDecodeCue(t)
	box := packEmsgV1(t, SchemeSCTE35Bin, "", 90000, 0, 0xFFFFFFFF, 1, cueBytes)

	cues, err := ExtractEmsgCues(bytes.NewReader(box))
	if err != nil {
		t.Fatalf("ExtractEmsgCues() error = %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	if !cues[0].EventDurationUnknown || cues[0].EventDuration != 0 {
		t.Errorf("cue = %+v, want EventDurationUnknown=true EventDuration=0", cues[0])
	}
}

func TestExtractEmsgCues_OtherSchemeSkipped(t *testing.T) {
	box := packEmsgV1(t, "urn:mpeg:dash:event:2012", "1", 1000, 0, 0, 1, []byte("some manifest-update payload"))

	cues, err := ExtractEmsgCues(bytes.NewReader(box))
	if err != nil {
		t.Fatalf("ExtractEmsgCues() error = %v", err)
	}
	if len(cues) != 0 {
		t.Fatalf("got %d cues, want 0 (non-SCTE-35 scheme should be silently skipped)", len(cues))
	}
}

func TestExtractEmsgCues_IgnoresOtherTopLevelBoxes(t *testing.T) {
	cueBytes := mustDecodeCue(t)
	var stream bytes.Buffer
	stream.Write(wrapBox(t, "styp", []byte("msdhmsix")))
	stream.Write(wrapBox(t, "moof", bytes.Repeat([]byte{0xAA}, 12))) // opaque, not actually parsed
	stream.Write(packEmsgV1(t, SchemeSCTE35Bin, "", 90000, 0, 1000, 9, cueBytes))
	stream.Write(wrapBox(t, "mdat", []byte("fake media bytes")))

	cues, err := ExtractEmsgCues(&stream)
	if err != nil {
		t.Fatalf("ExtractEmsgCues() error = %v", err)
	}
	if len(cues) != 1 || cues[0].ID != 9 {
		t.Fatalf("cues = %+v, want exactly one cue with ID=9", cues)
	}
}

func TestExtractEmsgCues_EmptyStream(t *testing.T) {
	cues, err := ExtractEmsgCues(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("ExtractEmsgCues() error = %v", err)
	}
	if len(cues) != 0 {
		t.Fatalf("got %d cues, want 0", len(cues))
	}
}

func TestExtractEmsgCues_TruncatedBox(t *testing.T) {
	box := packEmsgV1(t, SchemeSCTE35Bin, "", 90000, 0, 1000, 1, mustDecodeCue(t))
	truncated := box[:len(box)-10] // cut off the tail of message_data

	if _, err := ExtractEmsgCues(bytes.NewReader(truncated)); err == nil {
		t.Fatal("ExtractEmsgCues() error = nil, want an error for a truncated box")
	}
}

// ticksToDurationForTest mirrors the package's own unexported
// ticksToDuration but takes a plain (non-pointer) value, since these
// tests always have a real tick count to compare against.
func ticksToDurationForTest(ticks, timescale uint64) time.Duration {
	return time.Duration(float64(ticks) / float64(timescale) * float64(time.Second))
}
