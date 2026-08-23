package mpd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/scte35"
)

// SchemeSCTE35Bin is the scheme_id_uri for an 'emsg' box's message_data
// as raw binary SCTE-35 (SCTE 214-1), distinct from SchemeSCTE35XML (the
// MPD-level EventStream scheme).
const SchemeSCTE35Bin = "urn:scte:scte35:2013:bin"

// EmsgCue is one SCTE-35 cue found inband via an 'emsg' box inside a
// DASH media segment (.m4s) — the inband counterpart to CueRef, which
// covers MPD-level EventStream cues instead.
type EmsgCue struct {
	ID      uint32
	Version int // the emsg box's version (0 or 1) — see PresentationTime

	// PresentationTime is presentation_time (v1, absolute) or
	// presentation_time_delta (v0, relative to the segment start) — the
	// two aren't comparable without checking Version.
	PresentationTime time.Duration

	// EventDurationUnknown is true when the raw event_duration field is
	// 0xFFFFFFFF (ISO/IEC 23009-1's "unknown duration" sentinel), in
	// which case EventDuration is zero, not a real zero-length event.
	EventDuration        time.Duration
	EventDurationUnknown bool

	SpliceInfoSection *scte35.SpliceInfoSection
}

// ExtractEmsgCues walks r's top-level ISOBMFF boxes and decodes every
// 'emsg' box carrying a SchemeSCTE35Bin payload; other schemes (ID3,
// manifest-update events) are silently skipped. Only parses enough of
// ISOBMFF to walk top-level boxes and decode 'emsg' — not a general MP4
// demuxer.
func ExtractEmsgCues(r io.Reader) ([]EmsgCue, error) {
	var cues []EmsgCue
	for {
		size, boxType, headerLen, err := readBoxHeader(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return cues, nil
			}
			return nil, fmt.Errorf("mpd: reading box header: %w", err)
		}

		var body []byte
		if size == 0 {
			// size==0 means "extends to end of file" (ISO/IEC 14496-12).
			body, err = io.ReadAll(r)
			if err != nil {
				return nil, fmt.Errorf("mpd: reading box %q body: %w", boxType, err)
			}
		} else {
			bodyLen := int64(size) - int64(headerLen)
			if bodyLen < 0 {
				return nil, fmt.Errorf("mpd: box %q declares size %d smaller than its own header", boxType, size)
			}
			body = make([]byte, bodyLen)
			if _, err := io.ReadFull(r, body); err != nil {
				return nil, fmt.Errorf("mpd: reading box %q body: %w", boxType, err)
			}
		}

		if boxType != "emsg" {
			if size == 0 {
				break
			}
			continue
		}

		cue, ok, err := decodeEmsg(body)
		if err != nil {
			return nil, fmt.Errorf("mpd: emsg box: %w", err)
		}
		if ok {
			cues = append(cues, cue)
		}
		if size == 0 {
			break
		}
	}
	return cues, nil
}

// readBoxHeader reads one ISOBMFF box header (ISO/IEC 14496-12 §4.2): a
// 32-bit size, 4-byte type, and (only when size==1) a 64-bit "largesize".
// err is io.EOF exactly when the stream ends cleanly at a box boundary.
func readBoxHeader(r io.Reader) (size uint64, boxType string, headerLen int, err error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, "", 0, io.EOF
		}
		return 0, "", 0, err
	}
	size32 := binary.BigEndian.Uint32(hdr[0:4])
	boxType = string(hdr[4:8])

	if size32 != 1 {
		return uint64(size32), boxType, 8, nil
	}
	var ext [8]byte
	if _, err := io.ReadFull(r, ext[:]); err != nil {
		return 0, "", 0, fmt.Errorf("reading largesize for box %q: %w", boxType, err)
	}
	return binary.BigEndian.Uint64(ext[:]), boxType, 16, nil
}

// decodeEmsg parses one 'emsg' box body (version 0 or 1, ISO/IEC
// 23009-1 §5.10.3.3). ok is false (nil error) for any scheme other than
// SchemeSCTE35Bin.
func decodeEmsg(body []byte) (cue EmsgCue, ok bool, err error) {
	if len(body) < 4 {
		return EmsgCue{}, false, fmt.Errorf("box too short for version/flags")
	}
	version := body[0]
	// body[1:4] is the 24-bit flags field — always 0 for emsg, not used.
	rest := body[4:]

	var schemeIDURI string
	var timescale uint32
	var presentationTime uint64
	var eventDuration, id uint32

	switch version {
	case 0:
		var ok bool
		schemeIDURI, rest, ok = readCString(rest)
		if !ok {
			return EmsgCue{}, false, fmt.Errorf("v0: truncated scheme_id_uri")
		}
		_, rest, ok = readCString(rest) // value: not used by this package
		if !ok {
			return EmsgCue{}, false, fmt.Errorf("v0: truncated value")
		}
		if len(rest) < 16 {
			return EmsgCue{}, false, fmt.Errorf("v0: truncated fixed fields")
		}
		timescale = binary.BigEndian.Uint32(rest[0:4])
		presentationTimeDelta := binary.BigEndian.Uint32(rest[4:8])
		eventDuration = binary.BigEndian.Uint32(rest[8:12])
		id = binary.BigEndian.Uint32(rest[12:16])
		rest = rest[16:]
		presentationTime = uint64(presentationTimeDelta)

	case 1:
		if len(rest) < 20 {
			return EmsgCue{}, false, fmt.Errorf("v1: truncated fixed fields")
		}
		timescale = binary.BigEndian.Uint32(rest[0:4])
		presentationTime = binary.BigEndian.Uint64(rest[4:12])
		eventDuration = binary.BigEndian.Uint32(rest[12:16])
		id = binary.BigEndian.Uint32(rest[16:20])
		rest = rest[20:]
		var ok bool
		schemeIDURI, rest, ok = readCString(rest)
		if !ok {
			return EmsgCue{}, false, fmt.Errorf("v1: truncated scheme_id_uri")
		}
		_, rest, ok = readCString(rest) // value: not used by this package
		if !ok {
			return EmsgCue{}, false, fmt.Errorf("v1: truncated value")
		}

	default:
		return EmsgCue{}, false, fmt.Errorf("unsupported version %d (only 0 and 1 are defined)", version)
	}

	if schemeIDURI != SchemeSCTE35Bin {
		return EmsgCue{}, false, nil
	}

	section, err := scte35.Parse(rest) // rest is now message_data
	if err != nil {
		return EmsgCue{}, false, fmt.Errorf("decoding message_data as SCTE-35: %w", err)
	}

	const unknownDuration = 0xFFFFFFFF
	unknown := eventDuration == unknownDuration
	dur := time.Duration(0)
	if !unknown {
		durTicks := uint64(eventDuration)
		dur = ticksToDuration(&durTicks, uint64(timescale))
	}

	return EmsgCue{
		ID:                   id,
		Version:              int(version),
		PresentationTime:     ticksToDuration(&presentationTime, uint64(timescale)),
		EventDuration:        dur,
		EventDurationUnknown: unknown,
		SpliceInfoSection:    section,
	}, true, nil
}

// readCString reads a NUL-terminated string from the front of b,
// returning the string (without the terminator) and the remaining
// bytes. ok is false if no NUL byte is found.
func readCString(b []byte) (s string, rest []byte, ok bool) {
	i := bytes.IndexByte(b, 0)
	if i == -1 {
		return "", nil, false
	}
	return string(b[:i]), b[i+1:], true
}
