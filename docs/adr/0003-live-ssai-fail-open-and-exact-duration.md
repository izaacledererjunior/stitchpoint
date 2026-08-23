# ADR 0003: Live SSAI — fail-open ad resolution, exact-duration matching, single shared window

## Status

Accepted (Phase 4).

## Context

The project plan names Phase 4 (Live SSAI) as a separate, later milestone,
explicitly because live "adds real-time constraints and exact
ad-break-duration matching that VOD doesn't require." Two design
questions are new here that didn't exist for the VOD path
(`internal/server`, ADR-less because its decisions were smaller and
covered inline):

1. **Ad resolution takes real wall-clock time** (VAST fetch, creative
   download, FFmpeg transcode — seconds, not milliseconds) that can't be
   hidden behind a batch job the way VOD's `stitch`/`serve` does. A live
   poll loop has to decide, every interval, whether to forward the
   original break content or hold — while the ad might still be
   resolving.
2. **A live timeline is real and already playing.** VOD's
   `stitch.Options.AllowDurationMismatch` lets the manifest grow or
   shrink around whatever duration an ad actually is. Live can't do that
   — viewers are watching *now*; the ad has to fill the exact signaled
   break (`#EXT-X-CUE-OUT:<duration>`) or the stream drifts out of sync
   with the rest of the broadcast.

## Decisions

### 1. Fail open, splice mid-break once ready

When `#EXT-X-CUE-OUT` appears, ad resolution starts immediately in the
background (a goroutine, not blocking the poll loop). Until that
resolution finishes, the watcher forwards the *original* upstream break
content unchanged. As soon as the ad is ready, it's spliced in on the
next poll (with a discontinuity marker), suppressing further original
break content until `#EXT-X-CUE-IN`. If the ad is never ready before
`#EXT-X-CUE-IN` arrives, the break simply ends having shown only original
content — no ad, no error, no stall.

**Rejected alternative**: buffer/hold the live edge until the ad is
ready. Rejected because it stalls every viewer's playback for however
long ad resolution takes (unbounded from the viewer's perspective) —
strictly worse than occasionally missing an insertion opportunity. A real
production DAI system avoids this dilemma entirely by pre-fetching ads
ahead of the avail using early cue signaling (SCTE-35 commonly signals
breaks several seconds before they start); that's not implemented here —
see README "Future ideas".

### 2. Exact-duration matching via trim, not pad

`transcode.Params.MaxDuration` (added for this) caps an over-length
creative to the exact break duration via FFmpeg's `-t`. An under-length
creative is **not** padded — it's logged as an underfill and used as-is,
shorter than the signaled break.

**Rejected alternative**: generate filler (freeze-frame + silence, or a
second filler asset) to pad an under-length ad to the exact break. This
is real, meaningful scope (filler asset management, seamless
freeze-frame generation, ad-pod-style multi-ad filling) that a real DAI
product does implement — deliberately deferred, not silently dropped; see
README "Future ideas".

### 3. One shared output window per channel, not per-viewer personalization

`Watcher` maintains a single stitched output window, served identically
to every viewer of `/live/stitched.m3u8`. Real DAI products
(Google's included) personalize live ad insertion per viewer session —
different viewers can see different ads for the same break.

**Rejected for this pass**: per-viewer live personalization requires a
materially larger design (session-scoped live windows instead of one
shared poller, an ad decision per session instead of per break, N times
the VAST/transcode load for N concurrent viewers instead of one). Given
this project's portfolio scope and that the VOD path (`internal/server`)
already demonstrates per-session ad decisioning, duplicating that same
personalization concept for live wasn't judged worth the added
complexity right now — see README "Future ideas".

## Consequences

- A break can play with no ad at all (if resolution is slow or fails) —
  a real, expected outcome of the fail-open design, not a bug. Verified
  in `internal/live/live_test.go` implicitly (the "ad not ready" path is
  what every poll before splicing exercises) and explicitly documented
  here so it isn't mistaken for one later.
- An ad can be shorter than its signaled break (underfill), which a real
  broadcast operator would care about (dead air / early return to
  content) — logged loudly (`log.Printf` at resolution time) rather than
  silently accepted, so this is visible during operation, not just in
  the source code's doc comments.
- Every viewer of a given channel sees the same ad for the same break —
  a real, known simplification versus production SSAI, not an oversight.
- `manifest.Segment.CueOutDuration` (added for this ADR) is unused by the
  VOD path (`internal/stitch`) — VOD infers the break length from real
  segments already present between `CUE-OUT` and `CUE-IN`, which a live
  stream doesn't have yet at cue-out time. This is why the field lives on
  `Segment` generically rather than being live-package-specific: both
  packages parse the same manifest format, they just use different parts
  of it.
