package scte35

import "fmt"

// ExampleParseBase64 decodes a splice_insert cue signaling a 30s ad break
// starting at 300s — the form SCTE-35 takes in an HLS
// #EXT-X-DATERANGE SCTE35-OUT attribute or most ad-decision APIs. The
// message itself is a synthetic-but-spec-valid cue built by this
// package's own test suite (internal/scte35/scte35_test.go's
// buildSpliceInsert/buildSection), not captured from a real stream, so
// this example has no external fixture dependency.
func ExampleParseBase64() {
	const cue = "/DAlAAAAAAAAAAAAFAUAAAPpf+/+AZv8wP4AKTLgACoBAQAA3q2+7w=="

	section, err := ParseBase64(cue)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	si, ok := section.SpliceCommand.(SpliceInsert)
	if !ok {
		fmt.Printf("unexpected command type: %T\n", section.SpliceCommand)
		return
	}

	fmt.Println("out of network:", si.OutOfNetworkIndicator)
	fmt.Println("splice PTS:", si.SpliceTime.Duration())
	fmt.Println("break duration:", si.BreakDuration.AsDuration())
	// Output:
	// out of network: true
	// splice PTS: 5m0s
	// break duration: 30s
}
