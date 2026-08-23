# ADR 0005: Ad-break injection via three independent encodes, not one continuous encode with forced cuts

## Status

Accepted.

## Context

The playground (docs/playground-plan.md, Milestone 1) needs to turn an
arbitrary uploaded video plus a caller-chosen ad-break timestamp into an
HLS playlist carrying real `#EXT-X-CUE-OUT`/`#EXT-X-CUE-IN` tags at
exactly that point — the same shape as this project's own checked-in
`testdata/vod/content`, but for content nobody hand-authored.

`internal/transcode.EncodeHLS` already forces keyframes at computed
timestamps (`evenSegmentPlan`, added to fix the Phase 3 trailing-segment
bug — see ADR 0002) so segments land where asked rather than wherever
FFmpeg's HLS muxer happens to cut. The natural-looking extension: force
two more keyframes, at the break's start and end, in the middle of one
continuous encode of the whole upload.

## Decision

`internal/contentprep.InjectBreak` does not do that. It encodes the same
source file three separate times — `[0, breakStart)`,
`[breakStart, breakEnd)`, and `[breakEnd, end)` — each via
`transcode.EncodeHLS` with `Params.StartOffset`/`MaxDuration` set to
carve out just that sub-range, then concatenates the three resulting
playlists (renaming segment files to avoid the same collision
`internal/server` already had to solve — see its package doc) and tags
the two join points.

**Rejected alternative**: one continuous encode with `evenSegmentPlan`'s
existing forced-keyframe list extended to also include the two break
boundaries. Rejected because `evenSegmentPlan` and FFmpeg's HLS muxer
interaction were designed and tuned around fairly uniform segment
spacing (`-hls_time` matched to the computed even interval) — a
caller-chosen break timestamp is not uniform with anything, and there is
no way to *verify* after the fact that the muxer actually cut exactly at
an arbitrary forced time rather than at the next keyframe it happened to
reach, short of re-probing the output. Three independent encodes make
the join points exact by construction instead of by trusting muxer
behavior: each sub-range's encode starts fresh at its own t=0, so "does a
segment start exactly at breakStart" is true by definition.

## Consequences

- Three FFmpeg invocations instead of one, for what is conceptually a
  single operation — real overhead (encoder startup cost paid three
  times, not one), accepted because correctness at the break boundary
  matters more than shaving encode time for a demo-scale upload
  (Config.MaxUploadDuration caps this at 90s by default; see
  `internal/playground`).
- The "before" and "after" sub-ranges are each independently
  even-segmented by `evenSegmentPlan` (via `EncodeHLS`), not evenly
  segmented *across* the whole source — a segment immediately before the
  break can be a different length than one immediately after it. This is
  correct, not a bug: the two ranges have no reason to share a grid
  ("even" is relative to each range's own duration, same as any other
  `EncodeHLS` call).
- A break that starts at t=0, or runs to the exact end of the source, is
  a real, valid input — `InjectBreak` skips the corresponding empty
  sub-range's encode entirely rather than erroring, and (for the
  runs-to-the-end case) the result has no resuming `#EXT-X-CUE-IN`
  segment at all. `stitch.Splice` tolerates a break with nothing after
  it — this is documented in `InjectBreak`'s own doc comment, not just
  discovered by whoever hits it first.
- Encode parameters (resolution, bitrate) come from
  `internal/probe.Video` (added alongside this), not a fixed constant —
  unlike `transcode.DefaultParams`, which exists specifically for this
  project's own known test content and was never meant to match an
  arbitrary upload. This is the "dynamic encode-parameter probing" item
  README "Future ideas" already named.
