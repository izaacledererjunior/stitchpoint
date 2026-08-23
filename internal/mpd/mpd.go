// Package mpd parses DASH MPD (Media Presentation Description) documents
// far enough to extract content structure and SCTE-35 ad-break cues — the
// DASH equivalent of internal/hls. It models only what's needed for that
// (not the full MPD schema); splicing lives in internal/dashsplice (ADR
// 0007), inband `emsg` signaling in emsg.go (ADR 0008).
package mpd

import (
	"encoding/xml"
	"io"
)

// MPD is a parsed Media Presentation Description document.
type MPD struct {
	XMLName xml.Name `xml:"MPD"`

	// Xmlns/Profiles/MinBufferTime round-trip through Parse when present;
	// Write fills defaults if a freshly-built MPD leaves them empty.
	Xmlns                     string   `xml:"xmlns,attr,omitempty"`
	Profiles                  string   `xml:"profiles,attr,omitempty"`
	MinBufferTime             string   `xml:"minBufferTime,attr,omitempty"`
	Type                      string   `xml:"type,attr"` // "static" (VOD) or "dynamic" (live)
	MediaPresentationDuration string   `xml:"mediaPresentationDuration,attr"`
	Periods                   []Period `xml:"Period"`
}

// Period is one temporal section of the presentation.
type Period struct {
	ID             string          `xml:"id,attr"`
	Start          string          `xml:"start,attr"`
	Duration       string          `xml:"duration,attr"`
	EventStreams   []EventStream   `xml:"EventStream"`
	AdaptationSets []AdaptationSet `xml:"AdaptationSet"`
}

// AdaptationSet groups interchangeable encoded versions of one media
// component (e.g. all video quality levels).
type AdaptationSet struct {
	MimeType        string           `xml:"mimeType,attr"`
	ContentType     string           `xml:"contentType,attr"`
	Lang            string           `xml:"lang,attr"`
	SegmentTemplate *SegmentTemplate `xml:"SegmentTemplate"`
	Representations []Representation `xml:"Representation"`
}

// Representation is one specific encoded rendition within an
// AdaptationSet — see EffectiveSegmentTemplate for how SegmentTemplate
// resolves between the two levels.
type Representation struct {
	ID              string           `xml:"id,attr"`
	Bandwidth       int              `xml:"bandwidth,attr"`
	Width           int              `xml:"width,attr"`
	Height          int              `xml:"height,attr"`
	Codecs          string           `xml:"codecs,attr"`
	FrameRate       string           `xml:"frameRate,attr"`
	SegmentTemplate *SegmentTemplate `xml:"SegmentTemplate"`
}

// SegmentTemplate describes how to derive this rendition's segment URLs,
// via either a fixed per-segment Duration or an explicit SegmentTimeline.
type SegmentTemplate struct {
	Media                  string           `xml:"media,attr"`
	Initialization         string           `xml:"initialization,attr"`
	Timescale              uint64           `xml:"timescale,attr"`
	Duration               uint64           `xml:"duration,attr"`
	StartNumber            uint64           `xml:"startNumber,attr"`
	PresentationTimeOffset uint64           `xml:"presentationTimeOffset,attr"`
	SegmentTimeline        *SegmentTimeline `xml:"SegmentTimeline"`
}

// SegmentTimeline is an explicit list of segment start-time/duration
// pairs, with run-length repetition (S.R) for consecutive equal-duration
// segments.
type SegmentTimeline struct {
	S []S `xml:"S"`
}

// S is one SegmentTimeline entry. T is a pointer since it's only
// required on the first S element (or after a gap) — later, consecutive
// entries derive their start time from the previous entry's end.
type S struct {
	T *uint64 `xml:"t,attr"`
	D uint64  `xml:"d,attr"`
	R int     `xml:"r,attr"` // repeat count: this entry represents R+1 consecutive segments
}

// EventStream is a Period-level carrier for out-of-band event signaling
// — SCTE-35 ad cues, in this package's case (see cues.go).
type EventStream struct {
	SchemeIDURI string  `xml:"schemeIdUri,attr"`
	Value       string  `xml:"value,attr"`
	Timescale   uint64  `xml:"timescale,attr"`
	Events      []Event `xml:"Event"`
}

// Event is one EventStream entry. PresentationTime/Duration are in the
// containing EventStream's Timescale units, not seconds directly — see
// CueRef.PresentationTime/Duration for the already-converted form.
type Event struct {
	ID               string  `xml:"id,attr"`
	PresentationTime *uint64 `xml:"presentationTime,attr"`
	Duration         *uint64 `xml:"duration,attr"`

	// SCTE35 is set when this Event carries a scte35:SpliceInfoSection
	// child; nil for any other EventStream scheme.
	SCTE35 *scte35InfoSection `xml:"SpliceInfoSection"`
}

// EffectiveSegmentTemplate returns r's own SegmentTemplate if set,
// falling back to the containing AdaptationSet's — DASH allows either
// level to carry it, and a Representation-level one overrides.
func (r Representation) EffectiveSegmentTemplate(as AdaptationSet) *SegmentTemplate {
	if r.SegmentTemplate != nil {
		return r.SegmentTemplate
	}
	return as.SegmentTemplate
}

// Defaults for MPDs this package generates itself (e.g. dashsplice output).
const (
	defaultXmlns         = "urn:mpeg:dash:schema:mpd:2011"
	defaultProfiles      = "urn:mpeg:dash:profile:isoff-live:2011"
	defaultMinBufferTime = "PT2S"
)

// Write serializes m as an MPD XML document; m itself is not mutated.
func Write(w io.Writer, m *MPD) error {
	out := *m
	if out.Xmlns == "" {
		out.Xmlns = defaultXmlns
	}
	if out.Profiles == "" {
		out.Profiles = defaultProfiles
	}
	if out.MinBufferTime == "" {
		out.MinBufferTime = defaultMinBufferTime
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// Parse reads an MPD document; unrecognized elements/attributes are
// ignored rather than rejected (same policy as manifest.Parse for HLS).
func Parse(r io.Reader) (*MPD, error) {
	var m MPD
	if err := xml.NewDecoder(r).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}
