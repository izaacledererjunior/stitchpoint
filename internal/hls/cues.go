// Package hls extracts SCTE-35 cue references out of HLS playlist text —
// locating the tags real-world encoders use to carry SCTE-35 (base64 or
// hex) and handing back the raw encoded values. Doesn't model the
// playlist itself (segments, durations, variants); that's internal/manifest.
package hls

import (
	"bufio"
	"io"
	"strings"

	"github.com/izaacledererjunior/stitchpoint/internal/scte35"
)

// Tag names identifying which HLS tag a CueRef was extracted from. Kept as
// plain strings (not an enum) since new tag variants show up across
// packagers/encoders and callers mostly just want to display this.
const (
	TagOATCLSSCTE35 = "EXT-OATCLS-SCTE35"
	TagCueOutCont   = "EXT-X-CUE-OUT-CONT"
	TagDateRangeOut = "EXT-X-DATERANGE:SCTE35-OUT"
	TagDateRangeIn  = "EXT-X-DATERANGE:SCTE35-IN"
	TagDateRangeCmd = "EXT-X-DATERANGE:SCTE35-CMD"
	TagSCTE35       = "EXT-X-SCTE35"
)

// encoding identifies how a CueRef's Value is encoded, so callers know
// which scte35 parse function to use without guessing.
type encoding int

const (
	encodingBase64 encoding = iota
	encodingHex
)

// CueRef is a single SCTE-35 cue found while scanning a playlist: which
// line and tag it came from, and its still-encoded value.
type CueRef struct {
	Line  int    // 1-indexed source line number, for error reporting
	Tag   string // one of the Tag* constants
	Value string // still base64 or hex encoded

	encoding encoding
}

// Decode parses the cue's value into a splice_info_section, using
// whichever encoding the source tag uses.
func (c CueRef) Decode() (*scte35.SpliceInfoSection, error) {
	if c.encoding == encodingHex {
		return scte35.ParseHex(c.Value)
	}
	return scte35.ParseBase64(c.Value)
}

// ExtractCues scans HLS playlist text line by line and returns every
// SCTE-35 cue reference found; unrecognized lines are silently skipped.
func ExtractCues(r io.Reader) ([]CueRef, error) {
	var cues []CueRef
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())

		switch {
		case strings.HasPrefix(text, "#EXT-OATCLS-SCTE35:"):
			if v := strings.TrimPrefix(text, "#EXT-OATCLS-SCTE35:"); v != "" {
				cues = append(cues, CueRef{Line: line, Tag: TagOATCLSSCTE35, Value: v, encoding: encodingBase64})
			}

		case strings.HasPrefix(text, "#EXT-X-CUE-OUT-CONT:"):
			attrs := parseAttributeList(strings.TrimPrefix(text, "#EXT-X-CUE-OUT-CONT:"))
			if v := attrs["SCTE35"]; v != "" {
				cues = append(cues, CueRef{Line: line, Tag: TagCueOutCont, Value: v, encoding: encodingBase64})
			}

		case strings.HasPrefix(text, "#EXT-X-DATERANGE:"):
			attrs := parseAttributeList(strings.TrimPrefix(text, "#EXT-X-DATERANGE:"))
			for attrKey, tag := range map[string]string{
				"SCTE35-OUT": TagDateRangeOut,
				"SCTE35-IN":  TagDateRangeIn,
				"SCTE35-CMD": TagDateRangeCmd,
			} {
				if v := stripHexPrefix(attrs[attrKey]); v != "" {
					cues = append(cues, CueRef{Line: line, Tag: tag, Value: v, encoding: encodingHex})
				}
			}

		case strings.HasPrefix(text, "#EXT-X-SCTE35:"):
			attrs := parseAttributeList(strings.TrimPrefix(text, "#EXT-X-SCTE35:"))
			if v := attrs["CUE"]; v != "" {
				cues = append(cues, CueRef{Line: line, Tag: TagSCTE35, Value: v, encoding: encodingBase64})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sortByLine(cues), nil
}

// sortByLine fixes #EXT-X-DATERANGE's cue attributes coming out of a Go
// map in nondeterministic order; every other tag already appends in order.
func sortByLine(cues []CueRef) []CueRef {
	for i := 1; i < len(cues); i++ {
		for j := i; j > 0 && cues[j].Line < cues[j-1].Line; j-- {
			cues[j], cues[j-1] = cues[j-1], cues[j]
		}
	}
	return cues
}

// parseAttributeList parses an HLS attribute-list (the comma-separated
// KEY=VALUE syntax shared by #EXT-X-DATERANGE, #EXT-X-STREAM-INF, etc.),
// respecting double-quoted values so a comma inside e.g. a quoted ID
// attribute doesn't split into two fields.
func parseAttributeList(s string) map[string]string {
	attrs := make(map[string]string)
	i, n := 0, len(s)
	for i < n {
		for i < n && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		keyStart := i
		for i < n && s[i] != '=' {
			i++
		}
		if i >= n {
			break
		}
		key := strings.TrimSpace(s[keyStart:i])
		i++ // skip '='

		var val string
		if i < n && s[i] == '"' {
			i++
			valStart := i
			for i < n && s[i] != '"' {
				i++
			}
			val = s[valStart:i]
			if i < n {
				i++ // skip closing quote
			}
		} else {
			valStart := i
			for i < n && s[i] != ',' {
				i++
			}
			val = strings.TrimSpace(s[valStart:i])
		}
		attrs[key] = val
	}
	return attrs
}

func stripHexPrefix(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}
