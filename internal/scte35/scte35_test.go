package scte35

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// --- test-only bit writer, symmetric to bitReader, used to hand-build
// splice command payloads bit-for-bit against the spec's syntax tables. ---

type bitWriter struct {
	buf  []byte
	bits int // total bits written
}

func (w *bitWriter) writeBits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		bit := byte((v >> uint(i)) & 1)
		byteIdx := w.bits / 8
		if byteIdx == len(w.buf) {
			w.buf = append(w.buf, 0)
		}
		if bit == 1 {
			w.buf[byteIdx] |= 1 << uint(7-(w.bits%8))
		}
		w.bits++
	}
}

func (w *bitWriter) writeFlag(b bool) {
	if b {
		w.writeBits(1, 1)
	} else {
		w.writeBits(0, 1)
	}
}

// buildSpliceTime encodes a splice_time() structure per section 9.7.4.
func buildSpliceTime(w *bitWriter, specified bool, pts uint64) {
	w.writeFlag(specified)
	if !specified {
		w.writeBits(0x7F, 7) // reserved
		return
	}
	w.writeBits(0x3F, 6) // reserved
	w.writeBits(pts, 33)
}

// buildBreakDuration encodes a break_duration() structure per section 9.7.5.
func buildBreakDuration(w *bitWriter, autoReturn bool, dur uint64) {
	w.writeFlag(autoReturn)
	w.writeBits(0x3F, 6) // reserved
	w.writeBits(dur, 33)
}

// buildSpliceInsert encodes a full splice_insert() command body.
// helperer is satisfied by both *testing.T and *testing.F — just enough to
// let buildSpliceInsert/buildSection mark themselves as test helpers (for
// useful stack traces on failure) regardless of whether they're called
// from a table-driven test or from FuzzParse's seed corpus setup.
type helperer interface {
	Helper()
}

func buildSpliceInsert(t helperer, eventID uint32, cancel bool, outOfNetwork, immediate bool, pts uint64, hasDuration bool, breakDur uint64, uniqueProgramID uint16, availNum, availsExpected uint8) []byte {
	t.Helper()
	w := &bitWriter{}
	w.writeBits(uint64(eventID), 32)
	w.writeFlag(cancel)
	w.writeBits(0x7F, 7) // reserved
	if cancel {
		return w.buf
	}
	w.writeFlag(outOfNetwork)
	w.writeFlag(true) // program_splice_flag: always whole-program in these tests
	w.writeFlag(hasDuration)
	w.writeFlag(immediate)
	w.writeBits(0xF, 4) // reserved
	if !immediate {
		buildSpliceTime(w, true, pts)
	}
	if hasDuration {
		buildBreakDuration(w, true, breakDur)
	}
	w.writeBits(uint64(uniqueProgramID), 16)
	w.writeBits(uint64(availNum), 8)
	w.writeBits(uint64(availsExpected), 8)
	return w.buf
}

// buildSpliceInsertComponents encodes a splice_insert() command body with
// program_splice_flag=false — the component-level splicing path
// buildSpliceInsert never exercises (it always hardcodes whole-program
// splicing), covering ANSI/SCTE 35 section 9.7.3.1's component_count loop
// and its per-component optional splice_time().
func buildSpliceInsertComponents(eventID uint32, outOfNetwork, immediate bool, tags []byte, pts uint64, hasDuration bool, breakDur uint64, uniqueProgramID uint16, availNum, availsExpected uint8) []byte {
	w := &bitWriter{}
	w.writeBits(uint64(eventID), 32)
	w.writeFlag(false) // splice_event_cancel_indicator
	w.writeBits(0x7F, 7)
	w.writeFlag(outOfNetwork)
	w.writeFlag(false) // program_splice_flag
	w.writeFlag(hasDuration)
	w.writeFlag(immediate)
	w.writeBits(0xF, 4) // reserved
	w.writeBits(uint64(len(tags)), 8)
	for _, tag := range tags {
		w.writeBits(uint64(tag), 8)
		if !immediate {
			buildSpliceTime(w, true, pts)
		}
	}
	if hasDuration {
		buildBreakDuration(w, true, breakDur)
	}
	w.writeBits(uint64(uniqueProgramID), 16)
	w.writeBits(uint64(availNum), 8)
	w.writeBits(uint64(availsExpected), 8)
	return w.buf
}

func buildTimeSignal(specified bool, pts uint64) []byte {
	w := &bitWriter{}
	buildSpliceTime(w, specified, pts)
	return w.buf
}

// buildSection assembles a complete splice_info_section around a splice
// command body. The header (table_id through splice_command_type) is
// naturally byte-aligned per the spec, so it's built directly as bytes;
// section_length is computed from the command length rather than
// hand-counted, so tests can't drift out of sync with the builder.
func buildSection(t helperer, commandType SpliceCommandType, command []byte) []byte {
	t.Helper()

	const (
		protocolVersion = 0
		ptsAdjustment   = 0
	)

	body := []byte{}
	body = append(body, protocolVersion) // protocol_version

	// encrypted_packet(1) + encryption_algorithm(6) + pts_adjustment(33), byte-aligned at 40 bits (5 bytes)
	w := &bitWriter{}
	w.writeFlag(false)
	w.writeBits(0, 6)
	w.writeBits(ptsAdjustment, 33)
	body = append(body, w.buf...)

	body = append(body, 0) // cw_index

	w2 := &bitWriter{}
	w2.writeBits(0, 12)                    // tier
	w2.writeBits(uint64(len(command)), 12) // splice_command_length
	body = append(body, w2.buf...)

	body = append(body, byte(commandType))
	body = append(body, command...)

	body = append(body, 0x00, 0x00)             // descriptor_loop_length = 0
	body = append(body, 0xDE, 0xAD, 0xBE, 0xEF) // CRC32 (unvalidated by this parser)

	sectionLength := len(body)

	hdr := &bitWriter{}
	hdr.writeBits(0, 1)                      // section_syntax_indicator
	hdr.writeBits(0, 1)                      // private_indicator
	hdr.writeBits(0x3, 2)                    // sap_type (unspecified)
	hdr.writeBits(uint64(sectionLength), 12) // section_length

	out := []byte{expectedTableID}
	out = append(out, hdr.buf...)
	out = append(out, body...)
	return out
}

func TestParse_SpliceInsert(t *testing.T) {
	tests := []struct {
		name            string
		eventID         uint32
		cancel          bool
		outOfNetwork    bool
		immediate       bool
		pts             uint64
		hasDuration     bool
		breakDur        uint64
		uniqueProgramID uint16
	}{
		{
			name:            "cue-out with PTS and duration",
			eventID:         1001,
			outOfNetwork:    true,
			immediate:       false,
			pts:             27000000, // 300s at 90kHz
			hasDuration:     true,
			breakDur:        2700000, // 30s
			uniqueProgramID: 42,
		},
		{
			name:            "cue-in, immediate, no duration",
			eventID:         1002,
			outOfNetwork:    false,
			immediate:       true,
			hasDuration:     false,
			uniqueProgramID: 42,
		},
		{
			name:    "cancelled event carries no further fields",
			eventID: 2001,
			cancel:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildSpliceInsert(t, tc.eventID, tc.cancel, tc.outOfNetwork, tc.immediate, tc.pts, tc.hasDuration, tc.breakDur, tc.uniqueProgramID, 1, 1)
			data := buildSection(t, CommandSpliceInsert, cmd)

			section, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if section.SpliceCommandType != CommandSpliceInsert {
				t.Fatalf("SpliceCommandType = %v, want CommandSpliceInsert", section.SpliceCommandType)
			}
			si, ok := section.SpliceCommand.(SpliceInsert)
			if !ok {
				t.Fatalf("SpliceCommand type = %T, want SpliceInsert", section.SpliceCommand)
			}
			if si.SpliceEventID != tc.eventID {
				t.Errorf("SpliceEventID = %d, want %d", si.SpliceEventID, tc.eventID)
			}
			if si.SpliceEventCancelIndicator != tc.cancel {
				t.Errorf("SpliceEventCancelIndicator = %v, want %v", si.SpliceEventCancelIndicator, tc.cancel)
			}
			if tc.cancel {
				return // spec: nothing else is present
			}
			if si.OutOfNetworkIndicator != tc.outOfNetwork {
				t.Errorf("OutOfNetworkIndicator = %v, want %v", si.OutOfNetworkIndicator, tc.outOfNetwork)
			}
			if si.SpliceImmediateFlag != tc.immediate {
				t.Errorf("SpliceImmediateFlag = %v, want %v", si.SpliceImmediateFlag, tc.immediate)
			}
			if !tc.immediate {
				if si.SpliceTime == nil || !si.SpliceTime.TimeSpecifiedFlag {
					t.Fatalf("SpliceTime not decoded as specified")
				}
				if si.SpliceTime.PTSTime != tc.pts {
					t.Errorf("PTSTime = %d, want %d", si.SpliceTime.PTSTime, tc.pts)
				}
			}
			if si.DurationFlag != tc.hasDuration {
				t.Errorf("DurationFlag = %v, want %v", si.DurationFlag, tc.hasDuration)
			}
			if tc.hasDuration {
				if si.BreakDuration == nil {
					t.Fatalf("BreakDuration not decoded")
				}
				if si.BreakDuration.Duration != tc.breakDur {
					t.Errorf("BreakDuration.Duration = %d, want %d", si.BreakDuration.Duration, tc.breakDur)
				}
				wantSecs := float64(tc.breakDur) / PTSTicksPerSecond
				if got := si.BreakDuration.AsDuration().Seconds(); got != wantSecs {
					t.Errorf("AsDuration() = %v, want %v", got, wantSecs)
				}
			}
			if si.UniqueProgramID != tc.uniqueProgramID {
				t.Errorf("UniqueProgramID = %d, want %d", si.UniqueProgramID, tc.uniqueProgramID)
			}
		})
	}
}

// TestParse_SpliceInsert_ComponentSplicing covers program_splice_flag=false
// (component-by-component splicing), a real spec-defined splice_insert
// path distinct from the whole-program splicing every other test here
// exercises — rare in practice, but the parser has to decode it correctly
// since the bitstream can't be skipped without understanding it (see
// Component's doc comment).
func TestParse_SpliceInsert_ComponentSplicing(t *testing.T) {
	tests := []struct {
		name      string
		tags      []byte
		immediate bool
		pts       uint64
	}{
		{
			name:      "components with explicit splice_time",
			tags:      []byte{0x01, 0x02, 0x03},
			immediate: false,
			pts:       9000000, // 100s
		},
		{
			name:      "components with splice_immediate (no per-component splice_time)",
			tags:      []byte{0x10, 0x20},
			immediate: true,
		},
		{
			name:      "zero components",
			tags:      nil,
			immediate: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildSpliceInsertComponents(3001, true, tc.immediate, tc.tags, tc.pts, false, 0, 42, 1, 1)
			data := buildSection(t, CommandSpliceInsert, cmd)

			section, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			si, ok := section.SpliceCommand.(SpliceInsert)
			if !ok {
				t.Fatalf("SpliceCommand type = %T, want SpliceInsert", section.SpliceCommand)
			}
			if si.ProgramSpliceFlag {
				t.Fatalf("ProgramSpliceFlag = true, want false")
			}
			if si.SpliceTime != nil {
				t.Errorf("SpliceTime = %+v, want nil (only set for whole-program splicing)", si.SpliceTime)
			}
			if len(si.Components) != len(tc.tags) {
				t.Fatalf("len(Components) = %d, want %d", len(si.Components), len(tc.tags))
			}
			for i, tag := range tc.tags {
				c := si.Components[i]
				if c.ComponentTag != tag {
					t.Errorf("Components[%d].ComponentTag = %d, want %d", i, c.ComponentTag, tag)
				}
				if tc.immediate {
					if c.SpliceTime != nil {
						t.Errorf("Components[%d].SpliceTime = %+v, want nil (splice_immediate_flag set)", i, c.SpliceTime)
					}
					continue
				}
				if c.SpliceTime == nil || !c.SpliceTime.TimeSpecifiedFlag {
					t.Fatalf("Components[%d].SpliceTime not decoded as specified", i)
				}
				if c.SpliceTime.PTSTime != tc.pts {
					t.Errorf("Components[%d].SpliceTime.PTSTime = %d, want %d", i, c.SpliceTime.PTSTime, tc.pts)
				}
			}
		})
	}
}

func TestParse_TimeSignal(t *testing.T) {
	tests := []struct {
		name      string
		specified bool
		pts       uint64
	}{
		{name: "PTS specified", specified: true, pts: 9000000}, // 100s
		{name: "PTS not specified", specified: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := buildSection(t, CommandTimeSignal, buildTimeSignal(tc.specified, tc.pts))

			section, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			ts, ok := section.SpliceCommand.(TimeSignal)
			if !ok {
				t.Fatalf("SpliceCommand type = %T, want TimeSignal", section.SpliceCommand)
			}
			if ts.SpliceTime.TimeSpecifiedFlag != tc.specified {
				t.Errorf("TimeSpecifiedFlag = %v, want %v", ts.SpliceTime.TimeSpecifiedFlag, tc.specified)
			}
			if tc.specified {
				if ts.SpliceTime.PTSTime != tc.pts {
					t.Errorf("PTSTime = %d, want %d", ts.SpliceTime.PTSTime, tc.pts)
				}
				wantSecs := float64(tc.pts) / PTSTicksPerSecond
				if got := ts.SpliceTime.Duration().Seconds(); got != wantSecs {
					t.Errorf("Duration() = %v, want %v", got, wantSecs)
				}
			}
		})
	}
}

func TestParse_Base64AndHexRoundTrip(t *testing.T) {
	data := buildSection(t, CommandTimeSignal, buildTimeSignal(true, 900000))

	t.Run("base64", func(t *testing.T) {
		s := base64.StdEncoding.EncodeToString(data)
		section, err := ParseBase64(s)
		if err != nil {
			t.Fatalf("ParseBase64() error = %v", err)
		}
		if section.SpliceCommandType != CommandTimeSignal {
			t.Errorf("SpliceCommandType = %v, want CommandTimeSignal", section.SpliceCommandType)
		}
	})

	t.Run("hex", func(t *testing.T) {
		s := hexEncode(data)
		section, err := ParseHex(s)
		if err != nil {
			t.Fatalf("ParseHex() error = %v", err)
		}
		if section.SpliceCommandType != CommandTimeSignal {
			t.Errorf("SpliceCommandType = %v, want CommandTimeSignal", section.SpliceCommandType)
		}
	})
}

func TestParseBase64AndHex_InvalidEncoding(t *testing.T) {
	if _, err := ParseBase64("not valid base64!!"); err == nil {
		t.Error("ParseBase64() error = nil, want error for malformed base64")
	}
	if _, err := ParseHex("not valid hex"); err == nil {
		t.Error("ParseHex() error = nil, want error for malformed hex")
	}
}

func TestParse_UnknownCommandTypeIsRaw(t *testing.T) {
	// splice_schedule (0x04) is a real, spec-defined command type this
	// package doesn't decode; it must come back as an inert RawCommand
	// rather than an error, so an unfamiliar-but-valid message doesn't
	// break the whole parse.
	payload := []byte{0xAA, 0xBB, 0xCC}
	data := buildSection(t, CommandSpliceSchedule, payload)

	section, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	raw, ok := section.SpliceCommand.(RawCommand)
	if !ok {
		t.Fatalf("SpliceCommand type = %T, want RawCommand", section.SpliceCommand)
	}
	if raw.Type != CommandSpliceSchedule {
		t.Errorf("RawCommand.Type = %v, want CommandSpliceSchedule", raw.Type)
	}
	if len(raw.Payload) != len(payload) {
		t.Errorf("RawCommand.Payload len = %d, want %d", len(raw.Payload), len(payload))
	}
}

func TestParse_MalformedInput(t *testing.T) {
	validCmd := buildSpliceInsert(t, 1, false, true, false, 27000000, true, 2700000, 1, 1, 1)
	valid := buildSection(t, CommandSpliceInsert, validCmd)

	tests := []struct {
		name    string
		data    []byte
		wantErr error // nil means "any error", just must not panic and must not succeed
	}{
		{
			name: "empty input",
			data: []byte{},
		},
		{
			name: "wrong table_id",
			data: func() []byte {
				d := append([]byte{}, valid...)
				d[0] = 0x00
				return d
			}(),
		},
		{
			name: "truncated mid-header",
			data: valid[:4],
		},
		{
			name:    "truncated mid-command",
			data:    valid[:len(valid)-10],
			wantErr: ErrTruncated,
		},
		{
			name: "section_length points past buffer",
			data: func() []byte {
				d := append([]byte{}, valid...)
				// Corrupt section_length (bits 4..16, spanning bytes 1-2)
				// to claim a much larger section than actually present.
				d[1] |= 0x0F
				d[2] = 0xFF
				return d
			}(),
			wantErr: ErrTruncated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse() panicked: %v", r)
				}
			}()
			_, err := Parse(tc.data)
			if err == nil {
				t.Fatalf("Parse() error = nil, want error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("Parse() error = %v, want wrapping %v", err, tc.wantErr)
			}
		})
	}
}

func TestTicksToDuration(t *testing.T) {
	if got, want := ticksToDuration(90000), time.Second; got != want {
		t.Errorf("ticksToDuration(90000) = %v, want %v", got, want)
	}
	if got, want := ticksToDuration(45000), 500*time.Millisecond; got != want {
		t.Errorf("ticksToDuration(45000) = %v, want %v", got, want)
	}
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0x0F]
	}
	return string(out)
}
