package mpd

import "testing"

func TestMergeTrailingShortSegment(t *testing.T) {
	u64 := func(v uint64) *uint64 { return &v }

	tests := []struct {
		name      string
		timescale uint64
		in        []S
		want      []S
	}{
		{
			name:      "short trailing singleton merges into previous singleton",
			timescale: 44100,
			in:        []S{{T: u64(0), D: 441000}, {D: 3072}}, // 10s, then ~0.07s
			want:      []S{{T: u64(0), D: 444072}},
		},
		{
			name:      "short trailing singleton merges into previous repeat group",
			timescale: 44100,
			in:        []S{{T: u64(0), D: 441000, R: 3}, {D: 3072}}, // 4x10s, then ~0.07s
			want:      []S{{T: u64(0), D: 441000, R: 2}, {D: 444072}},
		},
		{
			name:      "last segment already long enough: unchanged",
			timescale: 44100,
			in:        []S{{T: u64(0), D: 441000}, {D: 441000}},
			want:      []S{{T: u64(0), D: 441000}, {D: 441000}},
		},
		{
			name:      "short segment is part of a repeat group: left alone",
			timescale: 44100,
			in:        []S{{T: u64(0), D: 3072, R: 5}},
			want:      []S{{T: u64(0), D: 3072, R: 5}},
		},
		{
			name:      "only one entry, and it's short: nothing to merge into",
			timescale: 44100,
			in:        []S{{T: u64(0), D: 3072}},
			want:      []S{{T: u64(0), D: 3072}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := &SegmentTemplate{Timescale: tt.timescale, SegmentTimeline: &SegmentTimeline{S: append([]S(nil), tt.in...)}}
			tpl.MergeTrailingShortSegment()
			got := tpl.SegmentTimeline.S
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i].D != tt.want[i].D || got[i].R != tt.want[i].R {
					t.Errorf("S[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMergeTrailingShortSegment_NilSafe(_ *testing.T) {
	var tpl *SegmentTemplate
	tpl.MergeTrailingShortSegment() // must not panic

	tpl = &SegmentTemplate{}
	tpl.MergeTrailingShortSegment() // nil SegmentTimeline; must not panic
}
