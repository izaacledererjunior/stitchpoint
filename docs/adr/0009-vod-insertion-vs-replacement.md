# ADR 0009: VOD ad splicing has two real models — replace a placeholder, or insert without loss

## Status

Accepted (requested explicitly: a VOD asset with a break should not lose
any of its own runtime — "no minuto 12 deve continuar exatamente
daonde o vídeo parou aos 10 minutos").

## Context

`internal/stitch.Splice` originally had one model: a break is a
pre-authored placeholder range (bracketed by `#EXT-X-CUE-OUT`/
`#EXT-X-CUE-IN`) that gets *replaced* by the ad — the placeholder's own
content is discarded, never shown. This is a real, correct model for
content that simulates a broadcast stream with an ad slot already
carved out by someone else (`testdata/vod/content`, this project's own
hand-authored Phase 1/2 test asset).

It is the *wrong* model for `internal/contentprep`'s actual job: turning
an arbitrary uploaded (or demo) video into content with a break inserted
at a viewer-chosen point. A 60-minute upload with a break requested at
minute 10 should still show all 60 original minutes — replacing minutes
10-11 with an ad silently discards content the person uploading never
asked to lose. The fix needs to be an *insertion*: the manifest grows by
(approximately) the ad's own duration, and playback resumes at exactly
the point it left off.

## Decision

`stitch.Options` gained `PreserveAllContent` (default `false`, preserving
the original replace behavior unchanged — `testdata/vod/content` and
every test built around it needed zero changes). When set:

- The `#EXT-X-CUE-OUT`-marked segment is understood as the *last real
  content segment before an insertion point*, not the first segment of a
  range to discard.
- The `#EXT-X-CUE-IN`-marked segment (the very next one — nothing sits
  between them once `internal/contentprep` authors this way; see below)
  is the *first real content segment after* the insertion — also kept,
  cue flag cleared, marked with a fresh discontinuity for the return to
  the original bitstream.
- Nothing in between is ever removed, because nothing is encoded there
  to remove — `internal/contentprep.InjectBreaks` no longer encodes a
  placeholder sub-range at all under this model. It just splits the
  source into contiguous spans at each break's insertion point, tagging
  the boundary segments — real content everywhere, no wasted encode, no
  discarded seconds.
- `Options.LoopAdToFillBreak` still works, targeting the insertion
  point's `CueOutDuration` (an authored *target* length, not a real
  range's measured duration, which doesn't exist under this model) —
  the same "loop until covered" math as the replace model, just against
  a different source of truth for the target.
- `AllowDurationMismatch`/`DurationMismatchError` don't apply under
  `PreserveAllContent` — there's no pre-existing duration to mismatch
  against when nothing is being replaced; the manifest simply grows by
  however long the (possibly looped) ad actually is.

**Rejected alternative**: make "insert, don't replace" the *only*
behavior, retiring the replace model entirely. Rejected because
`testdata/vod/content`'s own scenario (content authored *with* a
pre-existing ad slot, e.g. simulating a real broadcast feed) is equally
real and specifically what Phase 1/2's SCTE-35 test assets model —
collapsing both into one model would either break that scenario or
require re-authoring it to pretend to be something it isn't.

**Rejected alternative**: infer which model applies from the content
itself (e.g., "if the CueOut segment's content looks like real
programming, preserve it; if it looks like a slate/filler, replace it").
Rejected as fundamentally unreliable — there's no general way to
distinguish "real content temporarily standing in for an ad slot" from
"an authored placeholder" by inspecting a video segment. Making it an
explicit `Options` field is the same "refuse rather than guess" instinct
the rest of this package already applies to duration mismatches.

## Consequences

- `internal/playground.spliceAndFinish` (shared by the demo and upload
  paths — both always feed it `internal/contentprep`-produced content)
  now unconditionally passes `PreserveAllContent: true`.
- `internal/contentprep.InjectBreaks` gained a real constraint as a
  result: every break's `Start` must be strictly after the previous
  break's (or after 0) — there must be at least one real segment to
  attach `#EXT-X-CUE-OUT` to before an insertion point. A break
  requested at the very start of a video (`Start == 0`) is no longer
  expressible and returns a clear error, a real behavior change from the
  package's previous "no before-interval" edge case (which used to be
  valid because the old model didn't need a real segment to anchor the
  tag to — the CueOut segment itself *was* the discarded placeholder).
- `testdata/demo-content/` regenerated: total duration now genuinely
  equals the source's own duration (~143.9s) plus both ad insertions
  (~60s combined after `LoopAdToFillBreak`'s 3x loop) — ~203.9s, verified
  by summing every `#EXTINF` in the real stitched output from a live
  `POST /api/demo` run, not just inspecting the unspliced manifest.
