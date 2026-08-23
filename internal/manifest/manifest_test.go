package manifest

import (
	"bytes"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Playlist
		wantErr bool
	}{
		{
			name: "basic VOD playlist",
			input: `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:10.000000,
seg_000.ts
#EXTINF:8.500000,
seg_001.ts
#EXT-X-ENDLIST
`,
			want: &Playlist{
				Version: 3, TargetDuration: 10, MediaSequence: 0, PlaylistType: "VOD", EndList: true,
				Segments: []Segment{
					{URI: "seg_000.ts", Duration: 10},
					{URI: "seg_001.ts", Duration: 8.5},
				},
			},
		},
		{
			name: "cue-out/cue-in and discontinuity tags attach to the following segment",
			input: `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXTINF:10.000000,
seg_000.ts
#EXT-X-CUE-OUT:10.000000
#EXTINF:10.000000,
ad_000.ts
#EXT-X-CUE-IN
#EXT-X-DISCONTINUITY
#EXTINF:10.000000,
seg_001.ts
`,
			want: &Playlist{
				Version: 3, TargetDuration: 10,
				Segments: []Segment{
					{URI: "seg_000.ts", Duration: 10},
					{URI: "ad_000.ts", Duration: 10, CueOut: true, CueOutDuration: 10},
					{URI: "seg_001.ts", Duration: 10, CueIn: true, Discontinuity: true},
				},
			},
		},
		{
			name: "unrecognized tags are ignored, not rejected",
			input: `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-PROGRAM-DATE-TIME:2026-01-01T00:00:00Z
#EXT-X-KEY:METHOD=NONE
#EXTINF:10.000000,
seg_000.ts
`,
			want: &Playlist{
				Version: 3, TargetDuration: 10,
				Segments: []Segment{{URI: "seg_000.ts", Duration: 10}},
			},
		},
		{
			name: "bare CUE-OUT (no duration) leaves CueOutDuration at zero",
			input: `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXT-X-CUE-OUT
#EXTINF:10.000000,
ad_000.ts
`,
			want: &Playlist{
				Version: 3, TargetDuration: 10,
				Segments: []Segment{{URI: "ad_000.ts", Duration: 10, CueOut: true}},
			},
		},
		{
			name: "CUE-OUT-CONT is skipped, not treated as a new cue",
			input: `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXT-X-CUE-OUT-CONT:ElapsedTime=2.0,Duration=10.0
#EXTINF:10.000000,
ad_001.ts
`,
			want: &Playlist{
				Version: 3, TargetDuration: 10,
				Segments: []Segment{{URI: "ad_001.ts", Duration: 10}},
			},
		},
		{
			name: "segment URI without preceding EXTINF is an error",
			input: `#EXTM3U
seg_000.ts
`,
			wantErr: true,
		},
		{
			name: "malformed EXTINF is an error",
			input: `#EXTM3U
#EXTINF:not-a-number,
seg_000.ts
`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			assertPlaylistEqual(t, got, tc.want)
		})
	}
}

func TestWriteParseRoundTrip(t *testing.T) {
	p := &Playlist{
		Version: 3, TargetDuration: 10, MediaSequence: 0, PlaylistType: "VOD", EndList: true,
		Segments: []Segment{
			{URI: "seg_000.ts", Duration: 10},
			{URI: "ad_000.ts", Duration: 10, CueOut: true},
			{URI: "seg_001.ts", Duration: 10, CueIn: true, Discontinuity: true},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, p); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, err := Parse(&buf)
	if err != nil {
		t.Fatalf("Parse(Write(p)) error = %v", err)
	}
	assertPlaylistEqual(t, got, p)
}

func TestTotalDuration(t *testing.T) {
	p := &Playlist{Segments: []Segment{{Duration: 10}, {Duration: 8.5}, {Duration: 10}}}
	if got, want := p.TotalDuration(), 28.5; got != want {
		t.Errorf("TotalDuration() = %v, want %v", got, want)
	}
}

func assertPlaylistEqual(t *testing.T, got, want *Playlist) {
	t.Helper()
	if got.Version != want.Version {
		t.Errorf("Version = %d, want %d", got.Version, want.Version)
	}
	if got.TargetDuration != want.TargetDuration {
		t.Errorf("TargetDuration = %d, want %d", got.TargetDuration, want.TargetDuration)
	}
	if got.MediaSequence != want.MediaSequence {
		t.Errorf("MediaSequence = %d, want %d", got.MediaSequence, want.MediaSequence)
	}
	if got.PlaylistType != want.PlaylistType {
		t.Errorf("PlaylistType = %q, want %q", got.PlaylistType, want.PlaylistType)
	}
	if got.EndList != want.EndList {
		t.Errorf("EndList = %v, want %v", got.EndList, want.EndList)
	}
	if len(got.Segments) != len(want.Segments) {
		t.Fatalf("len(Segments) = %d, want %d\ngot:  %+v\nwant: %+v", len(got.Segments), len(want.Segments), got.Segments, want.Segments)
	}
	for i := range want.Segments {
		if got.Segments[i] != want.Segments[i] {
			t.Errorf("Segments[%d] = %+v, want %+v", i, got.Segments[i], want.Segments[i])
		}
	}
}
