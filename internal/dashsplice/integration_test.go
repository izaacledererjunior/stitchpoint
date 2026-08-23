package dashsplice

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
	"github.com/izaacledererjunior/stitchpoint/internal/transcode"
)

// TestSplice_RealDASHAssets is dashsplice's end-to-end proof, mirroring
// internal/stitch/integration_test.go's approach for HLS: real FFmpeg
// encodes real content and a real ad to genuine DASH (fMP4) segments via
// transcode.EncodeDASH, this package splices them, the result is
// materialized to a real directory, and FFmpeg is asked to decode the
// spliced MPD start to finish.
//
// Audio is dropped for both assets (Params.AudioBitrateKbps: 0 —
// see EncodeDASH's doc) specifically so the chosen break point lands
// exactly on every remaining track's segment boundary; DASH's
// independent per-track segmentation means audio's real boundaries drift
// from the nominal target (the same root cause mpd.
// MergeTrailingShortSegment fixes for the *trailing* segment — here it
// would also affect the *interior* boundary this test needs to be exact
// at). Real-world alignment of independently-segmented tracks to an
// arbitrary break point is what production packaging tools (e.g. Bento4,
// Shaka Packager) solve with SAP-aligned segmentation guarantees FFmpeg's
// CLI muxer doesn't provide by default — a real, separate concern from
// what this test exists to prove (the Period-splice mechanism itself).
func TestSplice_RealDASHAssets(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	contentSrc := filepath.Join("..", "..", "testdata", "vod", "content", "content.m3u8")
	adSrc := filepath.Join("..", "..", "testdata", "vod", "ad", "ad.m3u8")
	if _, err := os.Stat(contentSrc); err != nil {
		t.Skipf("test input not found: %v (see README Test assets)", err)
	}

	contentDir := t.TempDir()
	adDir := t.TempDir()
	outDir := t.TempDir()

	// 60s content at a 10s target -> exactly 6 segments; break covers the
	// third one, [20s, 30s) — a full segment, matching the ad source's
	// own natural ~10s length so this doesn't also need to exercise
	// duration-trimming (that's covered separately by the unit tests).
	content, err := transcode.EncodeDASH(contentSrc, contentDir, transcode.Params{
		Width: 640, Height: 360, VideoBitrateKbps: 400, SegmentSeconds: 10,
	})
	if err != nil {
		t.Fatalf("EncodeDASH(content) error = %v", err)
	}
	ad, err := transcode.EncodeDASH(adSrc, adDir, transcode.Params{
		Width: 640, Height: 360, VideoBitrateKbps: 400, SegmentSeconds: 10,
	})
	if err != nil {
		t.Fatalf("EncodeDASH(ad) error = %v", err)
	}

	content = injectCue(t, content, 20, 10) // presentationTime=20s, duration=10s
	prefixAdMediaPaths(ad, "ad/")           // avoid colliding with content's own chunk-stream0-*.m4s

	// AllowDurationMismatch: the ad source's real duration is ~10.02s,
	// not bit-exact with the content segment's own ~10.02s either — real
	// encoder rounding on two independently-encoded assets, exactly what
	// this option exists for (see Options' doc). Strict-matching
	// behavior itself is already covered by TestSplice_DurationMismatch.
	out, err := SpliceWithOptions(content, ad, Options{AllowDurationMismatch: true})
	if err != nil {
		t.Fatalf("SpliceWithOptions() error = %v", err)
	}
	if len(out.Periods) != 3 {
		t.Fatalf("got %d periods, want 3", len(out.Periods))
	}

	// Materialize: content's own files land directly in outDir (its
	// Media/Initialization templates are untouched, still relative to
	// wherever the manifest sits); the ad's files land in outDir/ad/,
	// matching the "ad/" prefix applied above.
	copyDir(t, contentDir, outDir)
	copyDir(t, adDir, filepath.Join(outDir, "ad"))

	manifestPath := filepath.Join(outDir, "stitched.mpd")
	mf, err := os.Create(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := mpd.Write(mf, out); err != nil {
		mf.Close()
		t.Fatalf("mpd.Write() error = %v", err)
	}
	mf.Close()

	cmd := exec.Command("ffmpeg", "-v", "error", "-i", manifestPath, "-f", "null", "-")
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg failed to decode spliced DASH stream: %v\n%s", err, stderr)
	}
	t.Logf("ffmpeg stderr:\n%s", stderr)
}

// injectCue re-parses content's own serialized form with a hand-authored
// SCTE-35 EventStream inserted into its (single, real) Period — see
// dashsplice_test.go's contentXML for why this goes through real XML
// text rather than struct construction (the cue-carrying type is
// unexported outside package mpd).
func injectCue(t *testing.T, content *mpd.MPD, presentationTimeSec, durationSec int) *mpd.MPD {
	t.Helper()
	var buf strings.Builder
	if err := mpd.Write(&buf, content); err != nil {
		t.Fatalf("mpd.Write(content) error = %v", err)
	}
	xmlStr := buf.String()

	periodOpenTag := fmt.Sprintf(`<Period id=%q`, content.Periods[0].ID)
	idx := strings.Index(xmlStr, periodOpenTag)
	if idx == -1 {
		t.Fatalf("could not find Period open tag %q in:\n%s", periodOpenTag, xmlStr)
	}
	tagEnd := strings.Index(xmlStr[idx:], ">")
	if tagEnd == -1 {
		t.Fatalf("malformed Period open tag in:\n%s", xmlStr)
	}
	insertAt := idx + tagEnd + 1

	cue := fmt.Sprintf(`
    <EventStream timescale="1" schemeIdUri="urn:scte:scte35:2013:xml">
      <Event presentationTime="%d" duration="%d">
        <scte35:SpliceInfoSection protocolVersion="0">
          <scte35:TimeSignal><scte35:SpliceTime ptsTime="0"/></scte35:TimeSignal>
        </scte35:SpliceInfoSection>
      </Event>
    </EventStream>`, presentationTimeSec, durationSec)

	injected := xmlStr[:insertAt] + cue + xmlStr[insertAt:]
	out, err := mpd.Parse(strings.NewReader(injected))
	if err != nil {
		t.Fatalf("re-parsing MPD with injected cue: %v\n---\n%s", err, injected)
	}
	return out
}

// prefixAdMediaPaths rewrites every Representation's Media/Initialization
// template in m's first Period to live under prefix, so the ad's segment
// files (copied into outDir/<prefix>) don't collide with content's own
// (both encoded via the same FFmpeg dash muxer, so both default to the
// same "chunk-stream0-...", "init-stream0.m4s" naming).
func prefixAdMediaPaths(m *mpd.MPD, prefix string) {
	for pi := range m.Periods {
		for ai := range m.Periods[pi].AdaptationSets {
			reps := m.Periods[pi].AdaptationSets[ai].Representations
			for ri := range reps {
				tpl := reps[ri].SegmentTemplate
				if tpl == nil {
					continue
				}
				tpl.Media = prefix + tpl.Media
				tpl.Initialization = prefix + tpl.Initialization
			}
		}
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(filepath.Join(dst, e.Name()))
		if err != nil {
			in.Close()
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		in.Close()
		out.Close()
	}
}
