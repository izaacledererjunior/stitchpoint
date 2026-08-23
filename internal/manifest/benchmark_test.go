package manifest

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// benchmarkSegmentCount approximates a real VOD asset at this project's
// usual 10s segment duration (see transcode.DefaultParams): ~50 minutes
// of content, plus one ad break — large enough that Parse/Write's
// per-line overhead actually shows up, not just fixed setup cost.
const benchmarkSegmentCount = 300

func benchmarkPlaylist() *Playlist {
	p := &Playlist{Version: 3, TargetDuration: 10, PlaylistType: "VOD", EndList: true}
	for i := 0; i < benchmarkSegmentCount; i++ {
		seg := Segment{URI: fmt.Sprintf("seg_%03d.ts", i), Duration: 10}
		switch i {
		case benchmarkSegmentCount / 2:
			seg.CueOut = true
			seg.CueOutDuration = 30
		case benchmarkSegmentCount/2 + 3:
			seg.CueIn = true
		}
		p.Segments = append(p.Segments, seg)
	}
	return p
}

func benchmarkPlaylistText() string {
	var sb strings.Builder
	if err := Write(&sb, benchmarkPlaylist()); err != nil {
		panic(err)
	}
	return sb.String()
}

// BenchmarkParse measures decoding a realistic-size media playlist — the
// per-request cost internal/server pays for every stitch, and internal/live
// pays on every poll interval.
func BenchmarkParse(b *testing.B) {
	text := benchmarkPlaylistText()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(strings.NewReader(text)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWrite measures serializing a realistic-size playlist back to
// HLS text — the last step of every stitch and every live poll's output
// window rebuild.
func BenchmarkWrite(b *testing.B) {
	p := benchmarkPlaylist()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := Write(io.Discard, p); err != nil {
			b.Fatal(err)
		}
	}
}
