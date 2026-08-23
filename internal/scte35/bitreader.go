package scte35

import (
	"errors"
	"fmt"
)

// ErrTruncated is returned when a splice_info_section ends before all of
// its declared fields have been read — the only way to detect a short
// buffer, since SCTE-35 has no self-describing framing below the section.
var ErrTruncated = errors.New("scte35: truncated message")

// bitReader reads big-endian, MSB-first bitfields out of a byte slice —
// SCTE-35 fields routinely span non-byte-aligned widths (33-bit PTS,
// 12-bit lengths, single-bit flags), so a plain byte reader isn't enough.
type bitReader struct {
	data []byte
	pos  int // bit offset from the start of data
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

// bitsLeft reports how many bits remain unread.
func (r *bitReader) bitsLeft() int {
	return len(r.data)*8 - r.pos
}

// readBits reads n bits (0 <= n <= 64) as an unsigned, MSB-first integer.
func (r *bitReader) readBits(n int) (uint64, error) {
	if n < 0 || n > 64 {
		return 0, fmt.Errorf("scte35: invalid bit width %d", n)
	}
	if r.bitsLeft() < n {
		return 0, ErrTruncated
	}

	var v uint64
	for i := 0; i < n; i++ {
		byteIdx := r.pos / 8
		bitIdx := 7 - (r.pos % 8) // MSB-first within the byte
		bit := (r.data[byteIdx] >> bitIdx) & 1
		v = (v << 1) | uint64(bit)
		r.pos++
	}
	return v, nil
}

// readFlag reads a single bit as a bool.
func (r *bitReader) readFlag() (bool, error) {
	v, err := r.readBits(1)
	if err != nil {
		return false, err
	}
	return v == 1, nil
}

// skipBits discards n bits, typically reserved/padding fields whose value
// the spec says to ignore.
func (r *bitReader) skipBits(n int) error {
	if r.bitsLeft() < n {
		return ErrTruncated
	}
	r.pos += n
	return nil
}

// bytePos returns the current position rounded down to a byte offset. Only
// valid to call when the reader is byte-aligned (splice_info_section is
// aligned at the points this is used).
func (r *bitReader) bytePos() int {
	return r.pos / 8
}
