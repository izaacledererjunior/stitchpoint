package scte35

import "fmt"

// Describe renders a splice_info_section as a single human-readable line
// summarizing cue type, timing, and duration — the information Phase 1
// needs to prove out (ad break type, timing, duration) without requiring a
// caller to know the SCTE-35 field names.
func Describe(s *SpliceInfoSection) string {
	switch cmd := s.SpliceCommand.(type) {
	case SpliceInsert:
		return describeSpliceInsert(cmd)
	case TimeSignal:
		return fmt.Sprintf("time_signal %s", describeSpliceTime(cmd.SpliceTime))
	case RawCommand:
		return fmt.Sprintf("splice_command_type=0x%02X (undecoded, %d bytes)", uint8(cmd.Type), len(cmd.Payload))
	default:
		return "unknown splice command"
	}
}

func describeSpliceInsert(si SpliceInsert) string {
	if si.SpliceEventCancelIndicator {
		return fmt.Sprintf("splice_insert event=%d CANCEL", si.SpliceEventID)
	}

	direction := "CUE-IN"
	if si.OutOfNetworkIndicator {
		direction = "CUE-OUT"
	}

	out := fmt.Sprintf("splice_insert event=%d %s", si.SpliceEventID, direction)

	switch {
	case si.SpliceImmediateFlag:
		out += " immediate"
	case si.SpliceTime != nil:
		out += " " + describeSpliceTime(*si.SpliceTime)
	}

	if si.DurationFlag && si.BreakDuration != nil {
		out += fmt.Sprintf(" duration=%s", si.BreakDuration.AsDuration())
	}

	return out
}

func describeSpliceTime(st SpliceTime) string {
	if !st.TimeSpecifiedFlag {
		return "pts=unspecified"
	}
	return fmt.Sprintf("pts=%s", st.Duration())
}
