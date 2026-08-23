package manifest

import (
	"fmt"
	"strings"
)

// ExampleParse decodes a small HLS media playlist carrying one ad break,
// marked the standard way: #EXT-X-CUE-OUT on the segment immediately
// before the break, #EXT-X-CUE-IN on the segment immediately after.
func ExampleParse() {
	const playlist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:10.000,
seg_000.ts
#EXT-X-CUE-OUT:30.000
#EXTINF:10.000,
seg_001.ts
#EXT-X-CUE-IN
#EXTINF:10.000,
seg_002.ts
#EXT-X-ENDLIST
`

	p, err := Parse(strings.NewReader(playlist))
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("segments:", len(p.Segments))
	fmt.Println("total duration:", p.TotalDuration())
	for _, s := range p.Segments {
		switch {
		case s.CueOut:
			fmt.Printf("%s: cue-out, break duration %.0fs\n", s.URI, s.CueOutDuration)
		case s.CueIn:
			fmt.Printf("%s: cue-in\n", s.URI)
		default:
			fmt.Printf("%s: content\n", s.URI)
		}
	}
	// Output:
	// segments: 3
	// total duration: 30
	// seg_000.ts: content
	// seg_001.ts: cue-out, break duration 30s
	// seg_002.ts: cue-in
}
