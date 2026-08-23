// Command stitchpoint is the CLI entry point for the stitchpoint SSAI
// stitcher project. Each subcommand corresponds to one phase or facet of
// the pipeline (see the package docs it delegates to for the real logic):
// scte35 (Phase 1 decoding), stitch/serve (Phase 2 VOD SSAI), probe/
// abr-bench (Phase 3 encode-path tooling), and live (Phase 4 live SSAI).
//
// This file only owns dispatch and top-level usage text; each subcommand's
// flag parsing and implementation lives in its own cmd_*.go file.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "scte35":
		err = runSCTE35(os.Args[2:])
	case "stitch":
		err = runStitch(os.Args[2:])
	case "dash-stitch":
		err = runDashStitch(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "probe":
		err = runProbe(os.Args[2:])
	case "abr-bench":
		err = runABRBench(os.Args[2:])
	case "live":
		err = runLive(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "stitchpoint: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "stitchpoint:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  stitchpoint scte35 [-file <path>] [cue ...]
  stitchpoint scte35 -manifest <path.m3u8>
  stitchpoint scte35 -mpd <path.mpd>
  stitchpoint scte35 -segment <path.m4s>
  stitchpoint stitch -content <path.m3u8> -ad <path.m3u8> -out <dir>
  stitchpoint stitch -content <path.m3u8> -vast <url> -out <dir>
  stitchpoint dash-stitch -content <path.mpd> -ad <path.mpd> -out <dir>
  stitchpoint dash-stitch -content <path.mpd> -vast <url> -out <dir>
  stitchpoint serve -content <path.m3u8> [flags]
  stitchpoint probe <file>
  stitchpoint abr-bench -out <dir> <input>
  stitchpoint live -upstream <url> [flags]

scte35 decodes SCTE-35 cue messages and prints their ad-break type, timing,
and duration, one line per cue.

Cues may be given as base64 or hex strings, either as positional arguments,
one per line in a file (-file), or one per line on stdin if neither is given.

-manifest reads an HLS playlist instead and extracts every SCTE-35 cue it
carries (#EXT-OATCLS-SCTE35, #EXT-X-CUE-OUT-CONT, #EXT-X-DATERANGE,
#EXT-X-SCTE35), reporting the source line for each.

-mpd reads a DASH MPD instead and extracts every SCTE-35 cue carried in
its EventStream elements (urn:scte:scte35:2013:xml scheme, out-of-band —
see internal/mpd's package doc for scope).

-segment reads a DASH media segment (.m4s) instead and extracts every
SCTE-35 cue carried inband via its emsg boxes (urn:scte:scte35:2013:bin
scheme — see internal/mpd's emsg.go doc). This is the DASH inband
counterpart to -manifest's HLS #EXT-OATCLS-SCTE35/etc. tags: a cue
discovered only as the segment carrying it is fetched, not visible from
the manifest alone.

stitch splices an ad into the content playlist (-content) at its
#EXT-X-CUE-OUT/#EXT-X-CUE-IN break, writing a self-contained stitched
manifest plus copies of every referenced segment into -out. The ad comes
from exactly one of:
  -ad <path.m3u8>   a pre-encoded HLS ad asset (must match the break's
                     duration, within a small tolerance, or splicing is
                     refused rather than risking a broken manifest)
  -vast <url>       a VAST tag URL (e.g. a Google Ad Manager ad tag);
                     stitchpoint resolves it (following Wrapper redirects),
                     downloads the selected creative, and encodes it via
                     FFmpeg to match the content. Real ad decisioning can't
                     guarantee an exact duration match, so -vast splices
                     regardless of duration — the manifest grows or shrinks
                     to fit, per this project's VOD architecture notes.

dash-stitch is stitch's DASH equivalent: splices an ad into -content's
Period at its first SCTE-35 EventStream cue by splitting that Period and
inserting a new one for the ad, writing a self-contained spliced MPD plus
copies of every segment file into -out. Genuinely different from stitch's
segment-list rewriting, not a reskin of it — see internal/dashsplice's
package doc for why, and its scope: SegmentTimeline-based content only,
break start read from Event/@presentationTime, one cue spliced per call.
Ad source is exactly one of -ad/-vast, same shape as stitch's.

serve runs stitchpoint as an HTTP server implementing real dynamic SSAI:
every GET /vod/manifest[?vast=<url>] runs a fresh VAST decision, downloads
and encodes that session's ad, splices it into -content, and redirects to
a session-scoped stitched manifest — a new ad per session/request, not a
single static file. See 'stitchpoint serve -h' for its flags.

probe <file> prints a media file's duration and video keyframe timestamps,
read directly via libavformat (cgo) rather than by shelling out to ffprobe
— see internal/probe and docs/adr/0002-cgo-libavformat-for-boundary-detection.md.
Useful standalone for inspecting a creative before deciding how to segment
it; also what internal/transcode.EncodeHLS uses internally to compute
even segment boundaries.

abr-bench -out <dir> <input> encodes input at each rung of a standard ABR
ladder (see internal/abrbench.DefaultLadder) and reports each rung's
actual output bitrate against its target — a ladder-design sanity check,
not a quality metric (no VMAF/PSNR; see README "Future ideas" for why).
-out must come before <input> (a Go flag-parsing constraint, not a choice).

live -upstream <url> [-vast <url>] runs real dynamic SSAI for a live
channel: polls upstream on an interval, detects #EXT-X-CUE-OUT breaks as
they appear, resolves an ad in the background (real VAST; for a fully
local demo with no real ad network, point -vast at cmd/vastfixture), and
splices it into a continuously-served output window at
/live/stitched.m3u8. Genuinely different from 'serve' — see
internal/live's package doc for why (exact-duration matching instead of
VOD's grow-to-fit, real-time ad resolution instead of a batch job, a
fail-open design for when the ad isn't ready in time). See
'stitchpoint live -h' for its flags.`)
}
