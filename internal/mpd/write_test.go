package mpd

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestWrite_RoundTrip constructs an MPD by hand (the shape dashsplice
// produces: no EventStream, plain SegmentTemplate/SegmentTimeline),
// writes it, re-parses the result, and checks the meaningful fields
// survive — proving Write's output is genuinely valid, parseable XML,
// not just checking it doesn't error.
func TestWrite_RoundTrip(t *testing.T) {
	in := &MPD{
		Type:                      "static",
		MediaPresentationDuration: FormatDuration(120 * time.Second),
		Periods: []Period{
			{
				ID:    "content-before",
				Start: FormatDuration(0),
				AdaptationSets: []AdaptationSet{
					{
						MimeType: "video/mp4",
						Representations: []Representation{
							{
								ID: "1", Width: 960, Height: 540, Bandwidth: 1000000, Codecs: "avc1.4D401F",
								SegmentTemplate: &SegmentTemplate{
									Timescale: 30000, Media: "v_$Number$.mp4", Initialization: "v_init.mp4", StartNumber: 1,
									SegmentTimeline: &SegmentTimeline{S: []S{{T: uint64Ptr(0), D: 60060, R: 2}}},
								},
							},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, in); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	out := buf.String()

	if !strings.HasPrefix(out, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("Write() output missing XML declaration: %.60q", out)
	}
	if !strings.Contains(out, `xmlns="`+defaultXmlns+`"`) {
		t.Errorf("Write() output missing default xmlns")
	}

	parsed, err := Parse(strings.NewReader(out))
	if err != nil {
		t.Fatalf("re-parsing Write() output: %v\n---\n%s", err, out)
	}

	if len(parsed.Periods) != 1 {
		t.Fatalf("parsed %d periods, want 1", len(parsed.Periods))
	}
	p := parsed.Periods[0]
	if p.ID != "content-before" {
		t.Errorf("Period.ID = %q, want %q", p.ID, "content-before")
	}
	if len(p.AdaptationSets) != 1 || len(p.AdaptationSets[0].Representations) != 1 {
		t.Fatalf("parsed AdaptationSets/Representations = %+v", p.AdaptationSets)
	}
	tpl := p.AdaptationSets[0].Representations[0].SegmentTemplate
	if tpl == nil || tpl.SegmentTimeline == nil || len(tpl.SegmentTimeline.S) != 1 {
		t.Fatalf("parsed SegmentTemplate/SegmentTimeline = %+v", tpl)
	}
	s := tpl.SegmentTimeline.S[0]
	if s.D != 60060 || s.R != 2 || s.T == nil || *s.T != 0 {
		t.Errorf("parsed S = %+v, want {T:0 D:60060 R:2}", s)
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }
