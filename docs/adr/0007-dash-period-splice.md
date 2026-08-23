# ADR 0007: DASH ad splicing via Period split/insert, not segment-list rewriting

## Status

Accepted (Milestone 3, DASH — completing the piece `internal/mpd`'s
package doc named as explicitly deferred when the parsing/cue-extraction
slice shipped).

## Context

`internal/stitch` splices HLS by finding the segment range bracketed by
`#EXT-X-CUE-OUT`/`#EXT-X-CUE-IN` in a flat segment list and replacing it
— valid because HLS VOD playback is one continuous segment list. DASH
structures a presentation as a sequence of independently-addressable
`Period` elements instead (each with its own `AdaptationSet`/
`Representation`/`SegmentTemplate`, and its own `start` on the overall
timeline); real DASH SSAI systems insert an ad by splitting the content's
Period at the break and inserting a new Period between the two halves —
a materially different mechanism, not a reskin of `internal/stitch`'s
segment-list replacement.

Three sub-problems specific to DASH needed real decisions, none of which
HLS splicing faced:

1. **Where does the break's start/end actually come from?** SCTE-35
   carries a legitimate timing ambiguity in DASH specifically: the real
   AWS MediaTailor reference example (`internal/mpd`'s own test fixture)
   signals the break via the legacy `SpliceInsert/SpliceTime/@ptsTime`
   value — a PTS on the *original broadcast's* clock, not directly a
   Period-relative presentation time. Correlating that PTS into the
   Period's own timeline needs additional out-of-band information (the
   original stream's PTS-to-wall-clock mapping) this project doesn't
   have and has no way to fabricate correctly.
2. **How does a Period's SegmentTimeline get split?** DASH's `S`/`@r`
   run-length encoding means a break boundary can legitimately fall
   *inside* a repeated run of otherwise-identical segments, not just at
   an `S`-array element boundary.
3. **How does the "after" content Period stay correctly addressed?**
   Its `$Number$`-templated segment URLs need to keep resolving to the
   same, already-existing physical files, and its `SegmentTimeline`
   needs to report correct Period-relative timing once it's a separate
   Period with its own `start`.

## Decisions

### 1. Read the break from `Event/@presentationTime`, not PTS correlation

`dashsplice.SpliceWithOptions` uses `mpd.ExtractCues`'s already-converted
`CueRef.PresentationTime`/`Duration` (from `Event/@presentationTime` and
`Event/@duration`) directly as the break's `[start, start+duration)`
window in the Period's own timeline.

**Rejected alternative**: attempt PTS-based correlation (the AWS
reference example's own mechanism) to support ingesting that exact kind
of manifest for splicing. Rejected because there's no principled way to
derive the correlation without additional signaling this project doesn't
model, and because a manifest generated *specifically for programmatic ad
insertion* — this package's actual intended input, as opposed to a
general "ingest anyone's live broadcast MPD" tool — can simply set
`presentationTime` directly; many real DASH-DAI systems already do.
**Consequence**: the real AWS reference example remains a
cue-*decoding*-validation fixture only (already proven correct in
`internal/mpd`'s own tests) — it was never a candidate splicing input
under this decision, which is fine since that was never its role.

### 2. Split at the tick level, not the `S`-array-element level

`dashsplice`'s `expandTimeline`/`compactTimeline` (`timeline.go`) fully
expand a `SegmentTimeline` into one entry per real segment before
locating the break, then re-compact each half back into `S`/`@r` form.

**Rejected alternative**: require the break to align with an existing
`S`-array element boundary (simpler code — no expand/compact step).
Rejected after weighing it against the real reference example's own
shape (its video track's break falls mid-run of a 30-segment repeated
group) — supporting mid-run splits was a bounded amount of extra code
(the expand/compact functions, independently unit-tested in
`TestSplitTemplate`'s "break spans a repeat group partway through" case)
for a real correctness gap, not speculative robustness.

### 3. Keep tick values, shift `presentationTimeOffset`; advance `startNumber`

The "after" Period keeps its `SegmentTimeline`'s original tick values
unchanged (nothing is rebased) and instead sets a new
`presentationTimeOffset` equal to its first segment's original tick
value — making that segment's Period-relative time exactly 0, matching
the new Period's `start` (`content period's start + breakEnd`).
`StartNumber` is advanced by however many segments precede the break, so
`$Number$`-templated URLs keep resolving to the same physical files
already on disk — no re-encoding, no renumbering of real files, matching
`internal/stitch`'s "manifest-level splice, not a re-encode" philosophy
for HLS.

**Consequence**: this only works because content isn't re-encoded for the
"after" half (same as HLS) — the ad Period's own `SegmentTemplate` is
used as-is from `transcode.EncodeDASH`'s output (self-contained,
independent timeline), needing no correlation with content's tick space
at all, which is exactly the structural flexibility Periods are for.

## Consequences

- `SegmentTimeline`-based content only (not a plain fixed-`@duration`
  `SegmentTemplate`) — matches `internal/mpd`'s existing scope and what
  this project's own `EncodeDASH` output and the real reference example
  both use. A clear error names which Representation lacks one, rather
  than guessing.
- The break must land exactly on a tick boundary in *every* affected
  Representation — refuses with a clear error otherwise, the same
  "refuse rather than silently approximate" policy
  `internal/stitch.findBreak` already applies to HLS.
- **DASH's independent per-track segmentation surfaced a second,
  related bug while building this**: FFmpeg's dash muxer segments audio
  independently of video (no equivalent of `-force_key_frames` for
  audio), so even a source whose *video* track cuts exactly on target
  can leave a short, spurious trailing *audio* segment — the same class
  of bug this project already fixed once for HLS's video track
  (`evenSegmentPlan`), showing up again for a structurally different
  reason. Fixed via `mpd.SegmentTemplate.MergeTrailingShortSegment`,
  applied to every Representation inside `transcode.EncodeDASH` before
  it returns. Real, aligning-independently-segmented-tracks-to-an-
  arbitrary-break-point is a separate, harder problem production
  packaging tools solve with SAP-aligned segmentation guarantees this
  project's FFmpeg-CLI-based `EncodeDASH` doesn't provide — the
  end-to-end integration test (`TestSplice_RealDASHAssets`) sidesteps it
  by using video-only test assets specifically to isolate proving the
  Period-splice mechanism from that separate concern.
- One cue, one ad Period, per `Splice` call — mirrors
  `internal/stitch.findBreak` only locating the first CUE-OUT/CUE-IN
  pair for HLS. A content MPD with multiple real breaks needs multiple
  calls.
- `mpd.Write` (new) and `mpd.ParseDuration`/`FormatDuration` (new) exist
  specifically to support this — `internal/mpd` was parse-only before.