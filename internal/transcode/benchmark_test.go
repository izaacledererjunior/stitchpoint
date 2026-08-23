package transcode

import (
	"testing"
	"time"
)

// BenchmarkEvenSegmentPlan measures the segment-boundary planning step
// EncodeHLS/EncodeDASH run before ever invoking FFmpeg — see the
// function's doc for the real bug (spurious short trailing segments) this
// replaced a naive fixed-interval cut with. It's pure arithmetic (no I/O),
// but it runs on every encode, including every span of a multi-break
// internal/contentprep job, so its own cost is worth knowing on its own
// rather than assuming it's negligible next to FFmpeg's.
func BenchmarkEvenSegmentPlan(b *testing.B) {
	const duration = 143 * time.Second // ~2m23s, a typical short test-clip length
	const target = 10.0                // seconds, this project's usual segment duration

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		evenSegmentPlan(duration, target)
	}
}
