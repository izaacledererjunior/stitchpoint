// Command gentestcue builds spec-conformant SCTE-35 splice_insert cues
// as test fixtures — a dev tool, not part of the stitching pipeline
// (stitchpoint only ever decodes SCTE-35, never authors it).
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
)

const ptsTicksPerSecond = 90000

type bitWriter struct {
	buf  []byte
	bits int
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

// buildSpliceTime encodes splice_time() with a specified PTS.
func buildSpliceTime(w *bitWriter, pts uint64) {
	w.writeFlag(true)
	w.writeBits(0x3F, 6) // reserved
	w.writeBits(pts, 33)
}

func buildBreakDuration(w *bitWriter, dur uint64) {
	w.writeFlag(true)    // auto_return
	w.writeBits(0x3F, 6) // reserved
	w.writeBits(dur, 33)
}

// buildSpliceInsert encodes a full splice_insert() command body for a
// program-level (not component-level) splice with a specified PTS.
func buildSpliceInsert(eventID uint32, outOfNetwork bool, pts uint64, hasDuration bool, breakDur uint64, uniqueProgramID uint16) []byte {
	w := &bitWriter{}
	w.writeBits(uint64(eventID), 32)
	w.writeFlag(false)   // splice_event_cancel_indicator
	w.writeBits(0x7F, 7) // reserved

	w.writeFlag(outOfNetwork)
	w.writeFlag(true) // program_splice_flag
	w.writeFlag(hasDuration)
	w.writeFlag(false)  // splice_immediate_flag
	w.writeBits(0xF, 4) // reserved

	buildSpliceTime(w, pts)
	if hasDuration {
		buildBreakDuration(w, breakDur)
	}

	w.writeBits(uint64(uniqueProgramID), 16)
	w.writeBits(1, 8) // avail_num
	w.writeBits(1, 8) // avails_expected
	return w.buf
}

// buildSection assembles a complete splice_info_section around a
// splice_insert command body. Mirrors internal/scte35's test builder;
// see that file's comments for why the header is byte-aligned and how
// section_length is computed.
func buildSection(command []byte) []byte {
	const commandType = 0x05 // splice_insert

	body := []byte{0} // protocol_version

	w := &bitWriter{}
	w.writeFlag(false) // encrypted_packet
	w.writeBits(0, 6)  // encryption_algorithm
	w.writeBits(0, 33) // pts_adjustment
	body = append(body, w.buf...)

	body = append(body, 0) // cw_index

	w2 := &bitWriter{}
	w2.writeBits(0, 12)                    // tier
	w2.writeBits(uint64(len(command)), 12) // splice_command_length
	body = append(body, w2.buf...)

	body = append(body, byte(commandType))
	body = append(body, command...)
	body = append(body, 0x00, 0x00) // descriptor_loop_length = 0

	// CRC32 is not validated by stitchpoint's parser (section_length gives
	// an independent way to find the end of the section), so a fixed
	// placeholder is fine for a decode-only test fixture.
	body = append(body, 0x00, 0x00, 0x00, 0x00)

	hdr := &bitWriter{}
	hdr.writeBits(0, 1)                  // section_syntax_indicator
	hdr.writeBits(0, 1)                  // private_indicator
	hdr.writeBits(0x3, 2)                // sap_type
	hdr.writeBits(uint64(len(body)), 12) // section_length

	out := []byte{0xFC} // table_id
	out = append(out, hdr.buf...)
	out = append(out, body...)
	return out
}

func main() {
	event := flag.Uint("event", 100, "splice_event_id")
	ptsSeconds := flag.Float64("pts", 0, "PTS time of the splice, in seconds")
	durationSeconds := flag.Float64("duration", 0, "break duration in seconds (0 omits duration_flag, for a CUE-IN)")
	cueIn := flag.Bool("cue-in", false, "encode as a cue-in (return from break) instead of a cue-out")
	uniqueProgramID := flag.Uint("program-id", 1, "unique_program_id")
	flag.Parse()

	pts := uint64(*ptsSeconds * ptsTicksPerSecond)
	hasDuration := *durationSeconds > 0
	dur := uint64(*durationSeconds * ptsTicksPerSecond)

	cmd := buildSpliceInsert(uint32(*event), !*cueIn, pts, hasDuration, dur, uint16(*uniqueProgramID))
	section := buildSection(cmd)

	if _, err := fmt.Fprintln(os.Stdout, base64.StdEncoding.EncodeToString(section)); err != nil {
		fmt.Fprintln(os.Stderr, "gentestcue:", err)
		os.Exit(1)
	}
}
