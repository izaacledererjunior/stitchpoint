package mpd

import (
	"fmt"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/scte35"
)

// SchemeSCTE35XML is the schemeIdUri for a fully-XML-modeled SCTE-35
// splice_info_section (SCTE 35 section 12.1) — not "...2014:xml+bin",
// which wraps base64 binary instead. Only this form is supported; it's
// what real production DAI publishes (see AWS MediaTailor's DASH docs).
const SchemeSCTE35XML = "urn:scte:scte35:2013:xml"

// CueRef is one SCTE-35 cue found in an MPD, analogous to internal/hls's
// CueRef. Carries the decoded *scte35.SpliceInfoSection directly (unlike
// hls.CueRef's Decode() method) since XML decoding can't fail the way
// decoding a base64/hex string can.
type CueRef struct {
	PeriodID          string
	EventID           string
	PresentationTime  time.Duration // Event.PresentationTime, converted from EventStream.Timescale units
	Duration          time.Duration // Event.Duration, same conversion; zero if unset
	SpliceInfoSection *scte35.SpliceInfoSection
}

// ExtractCues walks every Period's EventStreams and decodes every Event
// carrying a SchemeSCTE35XML SpliceInfoSection; other schemes and Events
// with no SpliceInfoSection are silently skipped, not an error.
func ExtractCues(m *MPD) ([]CueRef, error) {
	var cues []CueRef
	for _, period := range m.Periods {
		for _, es := range period.EventStreams {
			if es.SchemeIDURI != SchemeSCTE35XML {
				continue
			}
			timescale := es.Timescale
			if timescale == 0 {
				timescale = 1 // avoid divide-by-zero on a malformed value
			}
			for _, ev := range es.Events {
				if ev.SCTE35 == nil {
					continue
				}
				section, err := ev.SCTE35.toSpliceInfoSection()
				if err != nil {
					return nil, fmt.Errorf("mpd: period %q event %q: %w", period.ID, ev.ID, err)
				}
				cues = append(cues, CueRef{
					PeriodID:          period.ID,
					EventID:           ev.ID,
					PresentationTime:  ticksToDuration(ev.PresentationTime, timescale),
					Duration:          ticksToDuration(ev.Duration, timescale),
					SpliceInfoSection: section,
				})
			}
		}
	}
	return cues, nil
}

func ticksToDuration(ticks *uint64, timescale uint64) time.Duration {
	if ticks == nil {
		return 0
	}
	return time.Duration(float64(*ticks) / float64(timescale) * float64(time.Second))
}

// The types below model SCTE 35 section 12.1's XML binding, only
// SpliceInsert/TimeSignal (mirroring internal/scte35's binary parser
// scope). Field names follow the XML schema's camelCase, not Go idiom.
type scte35InfoSection struct {
	ProtocolVersion uint8                `xml:"protocolVersion,attr"`
	PTSAdjustment   uint64               `xml:"ptsAdjustment,attr"`
	Tier            uint16               `xml:"tier,attr"`
	SpliceInsert    *scte35SpliceInsert  `xml:"SpliceInsert"`
	TimeSignal      *scte35TimeSignalXML `xml:"TimeSignal"`
}

type scte35SpliceInsert struct {
	SpliceEventID              uint32               `xml:"spliceEventId,attr"`
	SpliceEventCancelIndicator bool                 `xml:"spliceEventCancelIndicator,attr"`
	OutOfNetworkIndicator      bool                 `xml:"outOfNetworkIndicator,attr"`
	SpliceImmediateFlag        bool                 `xml:"spliceImmediateFlag,attr"`
	UniqueProgramID            uint16               `xml:"uniqueProgramId,attr"`
	AvailNum                   uint8                `xml:"availNum,attr"`
	AvailsExpected             uint8                `xml:"availsExpected,attr"`
	Program                    *scte35Program       `xml:"Program"`
	BreakDuration              *scte35BreakDuration `xml:"BreakDuration"`
}

// scte35Program's presence maps to ProgramSpliceFlag=true on the binary
// struct; a Component-level child isn't modeled.
type scte35Program struct {
	SpliceTime *scte35SpliceTimeXML `xml:"SpliceTime"`
}

// PTSTime is a pointer: an absent ptsTime attribute is
// TimeSpecifiedFlag=false, not zero.
type scte35SpliceTimeXML struct {
	PTSTime *uint64 `xml:"ptsTime,attr"`
}

type scte35BreakDuration struct {
	AutoReturn bool   `xml:"autoReturn,attr"`
	Duration   uint64 `xml:"duration,attr"`
}

type scte35TimeSignalXML struct {
	SpliceTime scte35SpliceTimeXML `xml:"SpliceTime"`
}

// toSpliceInfoSection reconstructs a real scte35.SpliceInfoSection from
// the XML-modeled fields. Fields the binary framing carries but XML
// doesn't (TableID, CRC32, SAPType, encryption, CWIndex) stay zero-valued
// — genuinely absent, not a parsing gap.
func (s *scte35InfoSection) toSpliceInfoSection() (*scte35.SpliceInfoSection, error) {
	out := &scte35.SpliceInfoSection{
		ProtocolVersion: s.ProtocolVersion,
		PTSAdjustment:   s.PTSAdjustment,
		Tier:            s.Tier,
	}

	switch {
	case s.SpliceInsert != nil:
		si := s.SpliceInsert
		cmd := scte35.SpliceInsert{
			SpliceEventID:              si.SpliceEventID,
			SpliceEventCancelIndicator: si.SpliceEventCancelIndicator,
			OutOfNetworkIndicator:      si.OutOfNetworkIndicator,
			SpliceImmediateFlag:        si.SpliceImmediateFlag,
			UniqueProgramID:            si.UniqueProgramID,
			AvailNum:                   si.AvailNum,
			AvailsExpected:             si.AvailsExpected,
		}
		if si.Program != nil {
			cmd.ProgramSpliceFlag = true
			if si.Program.SpliceTime != nil {
				cmd.SpliceTime = xmlSpliceTime(*si.Program.SpliceTime)
			}
		}
		if si.BreakDuration != nil {
			cmd.DurationFlag = true
			cmd.BreakDuration = &scte35.BreakDuration{
				AutoReturn: si.BreakDuration.AutoReturn,
				Duration:   si.BreakDuration.Duration,
			}
		}
		out.SpliceCommandType = scte35.CommandSpliceInsert
		out.SpliceCommand = cmd

	case s.TimeSignal != nil:
		out.SpliceCommandType = scte35.CommandTimeSignal
		out.SpliceCommand = scte35.TimeSignal{SpliceTime: *xmlSpliceTime(s.TimeSignal.SpliceTime)}

	default:
		return nil, fmt.Errorf("SpliceInfoSection has neither SpliceInsert nor TimeSignal (an unsupported or empty splice command)")
	}

	return out, nil
}

func xmlSpliceTime(st scte35SpliceTimeXML) *scte35.SpliceTime {
	if st.PTSTime == nil {
		return &scte35.SpliceTime{TimeSpecifiedFlag: false}
	}
	return &scte35.SpliceTime{TimeSpecifiedFlag: true, PTSTime: *st.PTSTime}
}
