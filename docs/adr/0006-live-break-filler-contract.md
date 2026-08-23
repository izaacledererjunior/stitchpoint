# ADR 0006: `BreakFiller` — a pluggable contract for covering an underfilled live break

## Status

Accepted (Phase 4). Supersedes ADR 0003's "trim, not pad" decision for
under-length creatives specifically — over-length trimming (`transcode
.Params.MaxDuration`) is unchanged.

## Context

ADR 0003 chose not to pad an under-length ad, logging an underfill and
splicing the short creative as-is. In practice, against a real live
channel with a genuine multi-minute break (`docs/playground-plan.md`'s
local test setup surfaced this directly), that produced a real, visible
problem beyond the logged underfill: once the ad's single short segment
was spliced, `processSegment` correctly suppresses further original
content until the upstream signals `#EXT-X-CUE-IN` (see `live.go`) — so
for the remainder of a break far longer than the ad, the output window
gains no new segments at all. A real player watching that eventually
gives up waiting on a live stream that appears stalled and doesn't
recover on its own once content resumes, even though the *server-side*
break-end handling is working exactly as designed.

So "don't pad" isn't just a scope cut — it produces a materially worse
viewer experience than a short pad would, once a break is long enough
(this project's real test channel runs ~2-minute breaks against ad
creatives around 6s). Some form of covering the gap is worth doing now.

At the same time, the real target for this isn't padding — it's genuine
ad-pod filling, the way production DAI actually covers a break: resolving
multiple ads (either multiple VAST calls, or a single VAST response's
pod-style sequenced `<Ad>` entries) and stitching distinct creatives
together until the break's duration is covered. That's real, deferred
scope (parsing VAST ad pods isn't implemented — `internal/vast` resolves
a single ad today) — not something to build in this pass. The goal here
is specifically to not have to redo `resolveAd`'s call site when that
lands later.

## Decision

Introduce `live.BreakFiller` (`internal/live/fill.go`), a one-method
interface:

```go
type BreakFiller interface {
    Fill(segs []manifest.Segment, actual, target time.Duration) []manifest.Segment
}
```

`resolveAd` calls it exactly once, only when the resolved ad underfills
the break, and splices whatever it returns — `resolveAd` itself has no
opinion on *how* the gap gets covered. `live.Config.Filler` selects the
implementation, defaulting to `LoopFiller{}` if unset.

`LoopFiller` is today's only implementation: it repeats the resolved
creative's own segments (each repeat marked with a fresh
`#EXT-X-DISCONTINUITY`, since looping the same encode restarts its PTS
timeline) until the target duration is covered or a repeat cap
(`maxLoopRepeats = 200`) is hit, whichever comes first.

**Rejected alternative**: keep "no padding" (ADR 0003) unchanged and
treat the stalled-player symptom as a live-test-setup artifact (a
6-second creative against a 2-minute break is an unusually large
mismatch a production ad-decision server wouldn't produce). Rejected
because the underlying gap — a viewer-visible frozen stream — is real
regardless of how large a mismatch a production system would typically
produce, and covering it doesn't cost meaningful complexity once
expressed as this seam.

**Rejected alternative**: implement freeze-frame + silence generation
(the padding approach ADR 0003 originally named) instead of looping.
Rejected for this pass as strictly more implementation work (a real
FFmpeg filter graph generating synthetic filler content) for a result
that's just as obviously synthetic to a viewer as a loop, while not
being any closer to the real target (ad-pod filling) than looping is —
both are placeholders for the same future work.

## Consequences

- A break's output window keeps growing throughout the break even when
  the resolved ad underfills it significantly, instead of going silent
  until `#EXT-X-CUE-IN` — the stalled-player symptom this was written to
  fix.
- The loop is an obviously synthetic stand-in (a viewer will notice a
  6-second clip repeating for two minutes) — not hidden or described as
  a finished feature anywhere in code or docs; README's "Future ideas"
  names the real ad-pod filler this is meant to be replaced by.
- Replacing `LoopFiller` with a real ad-pod filler later is scoped to:
  writing a new `BreakFiller` implementation and wiring it into
  `live.Config.Filler`. `resolveAd`, `spliceAd`, `processSegment`, and
  the output-window/trim logic need no changes — this is the seam the
  contract exists to provide.
- `maxLoopRepeats` bounds output size for a pathological
  very-short-creative-vs-very-long-break input; hitting it is logged
  (`LoopFiller: hit the N-repeat cap...`) rather than failing silently,
  matching this project's general "log loudly on a degraded path" pattern
  (ADR 0003's underfill logging, `internal/contentprep`'s mismatch
  handling).