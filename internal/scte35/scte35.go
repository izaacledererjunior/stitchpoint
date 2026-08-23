// Package scte35 parses SCTE-35 splice_info_section messages — the cue
// points signaling ad breaks. Only splice_insert and time_signal are
// decoded (the two ad-insertion needs); other command types are
// recognized but not decoded. Field names mirror ANSI/SCTE 35's syntax
// tables for direct cross-checking against the spec.
package scte35

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// PTSTicksPerSecond is the clock rate SCTE-35 PTS and duration fields are
// expressed in (the MPEG-2 90kHz system clock), used to convert raw tick
// counts into a time.Duration.
const PTSTicksPerSecond = 90000

// SpliceCommandType identifies which splice command a splice_info_section
// carries. Values are defined by the spec; see the splice_command_type
// table in ANSI/SCTE 35 section 9.7.3.1.
type SpliceCommandType uint8

// SpliceCommandType values, per the splice_command_type table.
const (
	CommandSpliceNull           SpliceCommandType = 0x00
	CommandSpliceSchedule       SpliceCommandType = 0x04
	CommandSpliceInsert         SpliceCommandType = 0x05
	CommandTimeSignal           SpliceCommandType = 0x06
	CommandBandwidthReservation SpliceCommandType = 0x07
	CommandPrivate              SpliceCommandType = 0xff
)

// SpliceCommand is implemented by the decoded body of a splice command.
// Only SpliceInsert and TimeSignal are decoded into their full structure;
// every other command type is represented as RawCommand.
type SpliceCommand interface {
	commandType() SpliceCommandType
}

// SpliceTime carries an optional PTS value. Per spec, when
// time_specified_flag is false no PTS was signaled and the splice should be
// treated as happening immediately (or is otherwise conveyed out of band).
type SpliceTime struct {
	TimeSpecifiedFlag bool
	PTSTime           uint64 // 33-bit PTS, valid only if TimeSpecifiedFlag
}

// Duration converts a valid PTS value to a time.Duration since the stream's
// PTS origin. Callers must check TimeSpecifiedFlag first.
func (t SpliceTime) Duration() time.Duration {
	return ticksToDuration(t.PTSTime)
}

func ticksToDuration(ticks uint64) time.Duration {
	return time.Duration(ticks) * time.Second / PTSTicksPerSecond
}

// BreakDuration describes how long a signaled ad break lasts.
type BreakDuration struct {
	AutoReturn bool
	Duration   uint64 // 33-bit duration, in 90kHz ticks
}

// AsDuration converts the 90kHz tick count to a time.Duration.
func (d BreakDuration) AsDuration() time.Duration {
	return ticksToDuration(d.Duration)
}

// Component is a single entry of splice_insert's component-level splicing
// list, used only when ProgramSpliceFlag is false (component-by-component
// splicing rather than whole-program splicing). Rare in practice — most
// real-world streams splice at the program level — but decoded for
// completeness since the bitstream can't be skipped without parsing it.
type Component struct {
	ComponentTag byte
	SpliceTime   *SpliceTime // nil if the command uses splice_immediate
}

// SpliceInsert is the classic "insert an avail" command: either the start
// of an ad break (OutOfNetworkIndicator true) or the return to content
// (OutOfNetworkIndicator false).
type SpliceInsert struct {
	SpliceEventID              uint32
	SpliceEventCancelIndicator bool

	// The remaining fields are only meaningful when
	// SpliceEventCancelIndicator is false — a cancel indicator means this
	// message revokes a previously signaled event by ID and carries no
	// further splice details.
	OutOfNetworkIndicator bool
	ProgramSpliceFlag     bool
	DurationFlag          bool
	SpliceImmediateFlag   bool

	SpliceTime *SpliceTime // set when ProgramSpliceFlag && !SpliceImmediateFlag
	Components []Component // set when !ProgramSpliceFlag

	BreakDuration *BreakDuration // set when DurationFlag

	UniqueProgramID uint16
	AvailNum        uint8
	AvailsExpected  uint8
}

func (SpliceInsert) commandType() SpliceCommandType { return CommandSpliceInsert }

// TimeSignal is a bare PTS marker. On its own it says only "this moment
// matters" — what it means (ad break start/end, provider placement
// opportunity, etc.) is carried by an accompanying segmentation_descriptor,
// which this v1 parser does not yet decode.
type TimeSignal struct {
	SpliceTime SpliceTime
}

func (TimeSignal) commandType() SpliceCommandType { return CommandTimeSignal }

// RawCommand holds an undecoded splice command: its type and raw payload
// bytes, for command types this package doesn't interpret.
type RawCommand struct {
	Type    SpliceCommandType
	Payload []byte
}

func (c RawCommand) commandType() SpliceCommandType { return c.Type }

// SpliceInfoSection is a fully parsed SCTE-35 splice_info_section: the
// top-level container for a single cue message.
type SpliceInfoSection struct {
	TableID                byte
	SectionSyntaxIndicator bool
	PrivateIndicator       bool
	SAPType                uint8
	ProtocolVersion        uint8
	EncryptedPacket        bool
	EncryptionAlgorithm    uint8
	PTSAdjustment          uint64 // 33-bit
	CWIndex                uint8
	Tier                   uint16 // 12-bit

	SpliceCommandType SpliceCommandType
	SpliceCommand     SpliceCommand

	CRC32 uint32
}

// expectedTableID is the fixed table_id every splice_info_section must
// carry (0xFC, per spec section 9.7.2).
const expectedTableID = 0xFC

// ParseBase64 decodes a base64-encoded SCTE-35 message (the form it takes
// in HLS #EXT-X-DATERANGE SCTE35-* attributes and most ad-decision APIs)
// and parses it.
func ParseBase64(s string) (*SpliceInfoSection, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("scte35: invalid base64: %w", err)
	}
	return Parse(data)
}

// ParseHex decodes a hex-encoded SCTE-35 message and parses it.
func ParseHex(s string) (*SpliceInfoSection, error) {
	data, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("scte35: invalid hex: %w", err)
	}
	return Parse(data)
}

// Parse decodes a raw binary splice_info_section.
func Parse(data []byte) (*SpliceInfoSection, error) {
	r := newBitReader(data)

	tableID, err := r.readBits(8)
	if err != nil {
		return nil, err
	}
	if byte(tableID) != expectedTableID {
		return nil, fmt.Errorf("scte35: unexpected table_id 0x%02X, want 0x%02X", tableID, expectedTableID)
	}

	s := &SpliceInfoSection{TableID: byte(tableID)}

	if s.SectionSyntaxIndicator, err = r.readFlag(); err != nil {
		return nil, err
	}
	if s.PrivateIndicator, err = r.readFlag(); err != nil {
		return nil, err
	}
	sapType, err := r.readBits(2)
	if err != nil {
		return nil, err
	}
	s.SAPType = uint8(sapType)

	sectionLength, err := r.readBits(12)
	if err != nil {
		return nil, err
	}

	protocolVersion, err := r.readBits(8)
	if err != nil {
		return nil, err
	}
	s.ProtocolVersion = uint8(protocolVersion)

	if s.EncryptedPacket, err = r.readFlag(); err != nil {
		return nil, err
	}
	encAlg, err := r.readBits(6)
	if err != nil {
		return nil, err
	}
	s.EncryptionAlgorithm = uint8(encAlg)

	if s.PTSAdjustment, err = r.readBits(33); err != nil {
		return nil, err
	}

	cwIndex, err := r.readBits(8)
	if err != nil {
		return nil, err
	}
	s.CWIndex = uint8(cwIndex)

	tier, err := r.readBits(12)
	if err != nil {
		return nil, err
	}
	s.Tier = uint16(tier)

	spliceCommandLength, err := r.readBits(12)
	if err != nil {
		return nil, err
	}

	cmdType, err := r.readBits(8)
	if err != nil {
		return nil, err
	}
	s.SpliceCommandType = SpliceCommandType(cmdType)

	cmd, err := parseSpliceCommand(r, s.SpliceCommandType, int(spliceCommandLength))
	if err != nil {
		return nil, err
	}
	s.SpliceCommand = cmd

	// descriptor_loop_length(16) plus the descriptors themselves follow;
	// not decoded, so skip over them using the declared length.
	descLoopLen, err := r.readBits(16)
	if err != nil {
		return nil, err
	}
	if err := r.skipBits(int(descLoopLen) * 8); err != nil {
		return nil, err
	}

	// Locate CRC_32 (the final 32 bits) directly from section_length
	// instead of walking opaque alignment_stuffing/E_CRC_32 fields.
	// section_length counts everything after its own field, which starts
	// at bit 24 (table_id(8) + syntax/private/sap/length(16)).
	sectionEndBit := 24 + int(sectionLength)*8
	if sectionEndBit < 32 || sectionEndBit > len(data)*8 {
		return nil, fmt.Errorf("%w: section_length points outside message", ErrTruncated)
	}
	crcStartBit := sectionEndBit - 32
	if crcStartBit < r.pos {
		return nil, fmt.Errorf("%w: section_length shorter than parsed fields", ErrTruncated)
	}
	r.pos = crcStartBit
	crc, err := r.readBits(32)
	if err != nil {
		return nil, err
	}
	s.CRC32 = uint32(crc)

	return s, nil
}

// parseSpliceCommand decodes the splice command body. declaredLen is the
// splice_command_length field; for known command types we parse the
// bitstream directly, but we still trust declaredLen for RawCommand so
// unknown types can be captured verbatim without understanding their
// internal layout.
func parseSpliceCommand(r *bitReader, t SpliceCommandType, declaredLen int) (SpliceCommand, error) {
	switch t {
	case CommandSpliceNull:
		return RawCommand{Type: t}, nil
	case CommandSpliceInsert:
		return parseSpliceInsert(r)
	case CommandTimeSignal:
		st, err := parseSpliceTime(r)
		if err != nil {
			return nil, err
		}
		return TimeSignal{SpliceTime: st}, nil
	default:
		if declaredLen < 0 || r.bitsLeft() < declaredLen*8 {
			return nil, ErrTruncated
		}
		startByte := r.bytePos()
		if err := r.skipBits(declaredLen * 8); err != nil {
			return nil, err
		}
		return RawCommand{Type: t, Payload: r.data[startByte:r.bytePos()]}, nil
	}
}

func parseSpliceTime(r *bitReader) (SpliceTime, error) {
	specified, err := r.readFlag()
	if err != nil {
		return SpliceTime{}, err
	}
	if !specified {
		if err := r.skipBits(7); err != nil { // reserved
			return SpliceTime{}, err
		}
		return SpliceTime{}, nil
	}
	if err := r.skipBits(6); err != nil { // reserved
		return SpliceTime{}, err
	}
	pts, err := r.readBits(33)
	if err != nil {
		return SpliceTime{}, err
	}
	return SpliceTime{TimeSpecifiedFlag: true, PTSTime: pts}, nil
}

func parseBreakDuration(r *bitReader) (BreakDuration, error) {
	autoReturn, err := r.readFlag()
	if err != nil {
		return BreakDuration{}, err
	}
	if err := r.skipBits(6); err != nil { // reserved
		return BreakDuration{}, err
	}
	dur, err := r.readBits(33)
	if err != nil {
		return BreakDuration{}, err
	}
	return BreakDuration{AutoReturn: autoReturn, Duration: dur}, nil
}

func parseSpliceInsert(r *bitReader) (SpliceInsert, error) {
	var si SpliceInsert

	eventID, err := r.readBits(32)
	if err != nil {
		return si, err
	}
	si.SpliceEventID = uint32(eventID)

	si.SpliceEventCancelIndicator, err = r.readFlag()
	if err != nil {
		return si, err
	}
	if err := r.skipBits(7); err != nil { // reserved
		return si, err
	}

	if si.SpliceEventCancelIndicator {
		// A cancel indicator means the rest of the command is absent —
		// nothing more to parse.
		return si, nil
	}

	if si.OutOfNetworkIndicator, err = r.readFlag(); err != nil {
		return si, err
	}
	if si.ProgramSpliceFlag, err = r.readFlag(); err != nil {
		return si, err
	}
	if si.DurationFlag, err = r.readFlag(); err != nil {
		return si, err
	}
	if si.SpliceImmediateFlag, err = r.readFlag(); err != nil {
		return si, err
	}
	if err := r.skipBits(4); err != nil { // reserved
		return si, err
	}

	if si.ProgramSpliceFlag && !si.SpliceImmediateFlag {
		st, err := parseSpliceTime(r)
		if err != nil {
			return si, err
		}
		si.SpliceTime = &st
	}

	if !si.ProgramSpliceFlag {
		componentCount, err := r.readBits(8)
		if err != nil {
			return si, err
		}
		si.Components = make([]Component, 0, componentCount)
		for i := uint64(0); i < componentCount; i++ {
			tag, err := r.readBits(8)
			if err != nil {
				return si, err
			}
			c := Component{ComponentTag: byte(tag)}
			if !si.SpliceImmediateFlag {
				st, err := parseSpliceTime(r)
				if err != nil {
					return si, err
				}
				c.SpliceTime = &st
			}
			si.Components = append(si.Components, c)
		}
	}

	if si.DurationFlag {
		bd, err := parseBreakDuration(r)
		if err != nil {
			return si, err
		}
		si.BreakDuration = &bd
	}

	uniqueProgramID, err := r.readBits(16)
	if err != nil {
		return si, err
	}
	si.UniqueProgramID = uint16(uniqueProgramID)

	availNum, err := r.readBits(8)
	if err != nil {
		return si, err
	}
	si.AvailNum = uint8(availNum)

	availsExpected, err := r.readBits(8)
	if err != nil {
		return si, err
	}
	si.AvailsExpected = uint8(availsExpected)

	return si, nil
}
