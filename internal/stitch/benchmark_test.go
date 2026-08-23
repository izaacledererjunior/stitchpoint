package stitch

import (
	"fmt"
	"testing"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
)

// benchmarkContentPlaylist approximates a real ~50-minute VOD asset (see
// internal/manifest's benchmark for the same sizing rationale) with a
// single 10s break at the midpoint — large enough that Splice's
// segment-copying work actually shows up, not just its fixed per-call
// overhead.
func benchmarkContentPlaylist() *manifest.Playlist {
	const segments = 300
	p := &manifest.Playlist{Version: 3, TargetDuration: 10, PlaylistType: "VOD", EndList: true}
	for i := 0; i < segments; i++ {
		seg := manifest.Segment{URI: fmt.Sprintf("seg_%03d.ts", i), Duration: 10}
		if i == segments/2 {
			seg.CueOut = true
		}
		if i == segments/2+1 {
			seg.CueIn = true
		}
		p.Segments = append(p.Segments, seg)
	}
	return p
}

func benchmarkAdPlaylist() *manifest.Playlist {
	return &manifest.Playlist{Segments: []manifest.Segment{{URI: "ad_000.ts", Duration: 10}}}
}

// BenchmarkSpliceWithOptions measures the actual manifest-level splice —
// this project's core operation (see the package doc) — end to end: break
// detection, duration-match validation, and segment-list assembly.
func BenchmarkSpliceWithOptions(b *testing.B) {
	content := benchmarkContentPlaylist()
	ad := benchmarkAdPlaylist()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := SpliceWithOptions(content, ad, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
