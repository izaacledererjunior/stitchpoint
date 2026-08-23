package scte35

import "testing"

// noopHelper satisfies helperer for building fixtures at package-init
// time, before any *testing.T/*testing.F exists to pass in.
type noopHelper struct{}

func (noopHelper) Helper() {}

// benchmarkCue is a realistic splice_insert cue (30s ad break, PTS and
// duration set) reused from ExampleParseBase64 — decoded here in binary
// form so BenchmarkParse measures Parse itself, not base64 decoding.
var benchmarkCue = func() []byte {
	cmd := buildSpliceInsert(noopHelper{}, 1001, false, true, false, 27000000, true, 2700000, 42, 1, 1)
	return buildSection(noopHelper{}, CommandSpliceInsert, cmd)
}()

// BenchmarkParse measures decoding a single splice_info_section — the
// per-cue cost a live channel watcher (internal/live) or VOD ingest path
// pays every time a new cue shows up in the upstream manifest.
func BenchmarkParse(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(benchmarkCue); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBitReader_ReadBits measures the bit-level primitive every field
// of every cue is decoded through — the tightest loop in the parser, and
// the one most worth knowing the per-call cost of before reaching for a
// faster (but harder to verify against the spec's syntax tables)
// byte-aligned fast path.
func BenchmarkBitReader_ReadBits(b *testing.B) {
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i * 7)
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := newBitReader(data)
		for r.bitsLeft() >= 13 {
			if _, err := r.readBits(13); err != nil {
				b.Fatal(err)
			}
		}
	}
}
