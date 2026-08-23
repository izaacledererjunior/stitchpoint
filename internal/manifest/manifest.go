// Package manifest models HLS media playlists (the segment-list .m3u8,
// not multivariant) closely enough to support VOD and live splicing:
// segment order, duration, and the #EXT-X-CUE-OUT/#EXT-X-CUE-IN ad-break
// tags. Multivariant playlists, byte ranges, encryption, and most other
// HLS tags aren't modeled — not needed for a single-rendition splice.
package manifest

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Segment is one media segment entry in a playlist.
type Segment struct {
	URI      string
	Duration float64 // seconds, from EXTINF

	// Discontinuity is true if #EXT-X-DISCONTINUITY immediately precedes
	// this segment, signaling a decoder/timestamp reset (e.g. a codec or
	// encoding-parameter change) to the player.
	Discontinuity bool

	// CueOut is true if #EXT-X-CUE-OUT (optionally with a duration, e.g.
	// #EXT-X-CUE-OUT:30.000) immediately precedes this segment — the
	// standard HLS marker for "an ad break starts here."
	CueOut bool

	// CueOutDuration is the signaled break length from
	// #EXT-X-CUE-OUT:<duration>, or 0 if bare/CueOut is false. VOD
	// splicing infers real length from segments between CUE-OUT/CUE-IN;
	// live splicing needs this since those segments don't exist yet.
	CueOutDuration float64

	// CueIn is true if #EXT-X-CUE-IN immediately precedes this segment —
	// the standard marker for "content resumes here."
	CueIn bool
}

// Playlist is a parsed HLS media playlist.
type Playlist struct {
	Version        int
	TargetDuration int // seconds, per #EXT-X-TARGETDURATION
	MediaSequence  int
	PlaylistType   string // e.g. "VOD"; empty if not present
	EndList        bool   // #EXT-X-ENDLIST present
	Segments       []Segment
}

// TotalDuration sums every segment's duration.
func (p *Playlist) TotalDuration() float64 {
	var total float64
	for _, s := range p.Segments {
		total += s.Duration
	}
	return total
}

// Parse reads an HLS media playlist. Unrecognized tags are ignored rather
// than rejected — a real playlist may carry tags (encryption, byte ranges,
// program-date-time, etc.) this package doesn't need to understand in
// order to splice segments.
func Parse(r io.Reader) (*Playlist, error) {
	p := &Playlist{Version: 3} // spec default when #EXT-X-VERSION is absent

	var pendingDuration float64
	var haveDuration bool
	var pendingDiscontinuity, pendingCueOut, pendingCueIn bool
	var pendingCueOutDuration float64

	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		switch {
		case line == "#EXTM3U":
			// required first line; nothing to record

		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			v, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-VERSION:"))
			if err != nil {
				return nil, fmt.Errorf("manifest: line %d: invalid EXT-X-VERSION: %w", lineNo, err)
			}
			p.Version = v

		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			v, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if err != nil {
				return nil, fmt.Errorf("manifest: line %d: invalid EXT-X-TARGETDURATION: %w", lineNo, err)
			}
			p.TargetDuration = v

		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			if err != nil {
				return nil, fmt.Errorf("manifest: line %d: invalid EXT-X-MEDIA-SEQUENCE: %w", lineNo, err)
			}
			p.MediaSequence = v

		case strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE:"):
			p.PlaylistType = strings.TrimPrefix(line, "#EXT-X-PLAYLIST-TYPE:")

		case line == "#EXT-X-ENDLIST":
			p.EndList = true

		case line == "#EXT-X-DISCONTINUITY":
			pendingDiscontinuity = true

		case strings.HasPrefix(line, "#EXT-X-CUE-OUT-CONT"):
			// Marks a continuation of an already-announced break; not a
			// new cue, so not modeled — internal/live tracks break state
			// itself across polls.

		case strings.HasPrefix(line, "#EXT-X-CUE-OUT"):
			pendingCueOut = true
			if rest := strings.TrimPrefix(line, "#EXT-X-CUE-OUT:"); rest != line {
				if d, err := strconv.ParseFloat(strings.TrimSpace(rest), 64); err == nil {
					pendingCueOutDuration = d
				}
				// A malformed duration is treated the same as a bare tag
				// (pendingCueOutDuration stays 0) rather than failing the
				// whole parse — the cue-out itself is still real and
				// actionable even if its duration attribute is garbled.
			}

		case line == "#EXT-X-CUE-IN":
			pendingCueIn = true

		case strings.HasPrefix(line, "#EXTINF:"):
			// #EXTINF:<duration>,[<title>]
			rest := strings.TrimPrefix(line, "#EXTINF:")
			durStr, _, _ := strings.Cut(rest, ",")
			d, err := strconv.ParseFloat(strings.TrimSpace(durStr), 64)
			if err != nil {
				return nil, fmt.Errorf("manifest: line %d: invalid EXTINF duration: %w", lineNo, err)
			}
			pendingDuration = d
			haveDuration = true

		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			// This is a multivariant (master) playlist, not a media
			// playlist — call out the specific mistake rather than
			// falling through to a generic "no preceding EXTINF" error.
			return nil, fmt.Errorf("manifest: line %d: this is a multivariant (master) playlist, not a media playlist — use one of its listed variant URIs (e.g. the one ending in a resolution like \"_360p.m3u8\") instead", lineNo)

		case strings.HasPrefix(line, "#"):
			// Unrecognized tag: ignored by design (see package doc).

		default:
			// A bare line with no leading '#' is a segment URI.
			if !haveDuration {
				return nil, fmt.Errorf("manifest: line %d: segment URI %q with no preceding EXTINF", lineNo, line)
			}
			p.Segments = append(p.Segments, Segment{
				URI:            line,
				Duration:       pendingDuration,
				Discontinuity:  pendingDiscontinuity,
				CueOut:         pendingCueOut,
				CueOutDuration: pendingCueOutDuration,
				CueIn:          pendingCueIn,
			})
			haveDuration = false
			pendingDiscontinuity, pendingCueOut, pendingCueIn = false, false, false
			pendingCueOutDuration = 0
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	return p, nil
}

// Write serializes a playlist back to HLS media playlist text.
func Write(w io.Writer, p *Playlist) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintln(bw, "#EXTM3U")
	fmt.Fprintf(bw, "#EXT-X-VERSION:%d\n", p.Version)
	fmt.Fprintf(bw, "#EXT-X-TARGETDURATION:%d\n", p.TargetDuration)
	fmt.Fprintf(bw, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.MediaSequence)
	if p.PlaylistType != "" {
		fmt.Fprintf(bw, "#EXT-X-PLAYLIST-TYPE:%s\n", p.PlaylistType)
	}

	for _, s := range p.Segments {
		if s.CueOut {
			if s.CueOutDuration > 0 {
				fmt.Fprintf(bw, "#EXT-X-CUE-OUT:%g\n", s.CueOutDuration)
			} else {
				fmt.Fprintln(bw, "#EXT-X-CUE-OUT")
			}
		}
		if s.CueIn {
			fmt.Fprintln(bw, "#EXT-X-CUE-IN")
		}
		if s.Discontinuity {
			fmt.Fprintln(bw, "#EXT-X-DISCONTINUITY")
		}
		fmt.Fprintf(bw, "#EXTINF:%f,\n", s.Duration)
		fmt.Fprintln(bw, s.URI)
	}

	if p.EndList {
		fmt.Fprintln(bw, "#EXT-X-ENDLIST")
	}

	return bw.Flush()
}
