# ADR 0002: cgo + libavformat for keyframe/segment boundary detection

## Status

Accepted (Phase 3).

## Context

ADR 0001 deferred all FFmpeg integration to CLI subprocess calls for v1,
explicitly naming keyframe/segment boundary detection as the step most
likely to benefit from a future move to direct `cgo` + `libavformat`
bindings — "the specific step that benefits most" (ADR 0001's
consequences section) — once v1 shipped.

That future arrived with a concrete, observed reason, not just a phase
number coming up: `internal/transcode.EncodeHLS` asked FFmpeg's HLS muxer
to cut segments at a fixed interval (`-force_key_frames
"expr:gte(t,n_forced*N)"`, `-hls_time N`). When a downloaded ad
creative's real duration didn't divide evenly by `N`, this left a
spurious, sub-second trailing segment (observed: a 6.0064s creative at a
10s target correctly collapsed to one segment, but other real durations —
e.g. ~10.03s at a 4s target — would leave a ~2s remainder segment).
Sub-second HLS segments are a known source of playback issues in some
clients. The fix requires knowing the creative's *exact* duration before
encoding starts, so segment boundaries can be computed as even divisions
of the real duration rather than trusting a fixed interval to land
cleanly.

## Decision

Add `internal/probe`, a small package of direct cgo bindings to
`libavformat`/`libavcodec`/`libavutil`, and use its `Duration()` function
in `internal/transcode.EncodeHLS` to compute evenly-divided segment
boundaries (`evenSegmentPlan`) before invoking FFmpeg. The package also
exposes `Keyframes()` — walking a video stream's packets for real
`AV_PKT_FLAG_KEY` positions — as the more literal "keyframe boundary
detection" capability named in ADR 0001, exposed standalone via
`stitchpoint probe <file>` for inspection/diagnostic use (the "optional
benchmarking tool as a side artifact" the project plan calls out for Phase 3).

FFmpeg itself is still invoked as a CLI subprocess for the actual encode
(ADR 0001's core call stands — cgo-binding the *encoder* is a much larger
surface with little payoff here). Only the boundary-detection step moved
to cgo, matching the ADR 0001 consequence this was always expected to be.

## Rationale

- **Fixes a real bug, not a hypothetical one.** The sub-second trailing
  segment was observed in this project's own end-to-end testing, not
  invented to justify the cgo work.
- **`ffprobe`-via-subprocess-and-parse-JSON was the other option and was
  rejected.** It would have solved the immediate duration problem with
  zero new build complexity, but ADR 0001 specifically named this as the
  step worth doing in cgo eventually, and shelling out to `ffprobe` just
  to parse its JSON back into Go is arguably *more* code (subprocess
  management + JSON schema handling) than a narrowly-scoped 130-line cgo
  package that talks to the same library `ffprobe` itself is built on —
  and the cgo version is also the version that demonstrates the skill
  this project exists to demonstrate.
- **Genuinely exercises libavformat's C API**, not just FFmpeg's CLI
  surface: `avformat_open_input`, `avformat_find_stream_info`,
  `av_find_best_stream`, `av_read_frame`, packet flag inspection,
  `av_q2d` time-base conversion, and explicit resource cleanup
  (`avformat_close_input`, `av_packet_free`) — the kind of C-interop
  work a video-pipeline role actually involves.

## Consequences

- **Building stitchpoint now requires `libavformat-dev`, `libavcodec-dev`,
  and `libavutil-dev`** (plus a C compiler and `CGO_ENABLED=1`), not just
  the `ffmpeg` binary on `PATH`. This is a real cost ADR 0001 anticipated
  ("Consequences" section) when deferring this to a later phase: the
  project is less portable and harder to cross-compile than it was as
  pure Go + subprocess calls. Documented in README "How to run it".
- `internal/probe`'s tests run against real checked-in test media
  (`testdata/vod/ad/seg_000.ts`), cross-checked against `ffprobe`'s own
  output independently — not just internal consistency (see
  `internal/probe/probe_test.go`). This caught a real, if mundane,
  finding along the way: that file's video PTS doesn't start at 0 (a
  MPEG-TS muxing property, confirmed by `ffprobe` too, not a bug) — the
  test originally assumed otherwise and had to be corrected.
- `Keyframes()` is currently exposed for diagnostics (`stitchpoint
  probe`) but not yet used inside `EncodeHLS` itself — only `Duration()`
  is. A further refinement (snapping forced split points to the nearest
  real *source* keyframe rather than an arbitrary even division) is
  possible but wasn't needed to fix the observed bug, since FFmpeg's
  encoder inserts fresh keyframes at whatever output timestamps
  `-force_key_frames` requests regardless of the source's own GOP
  structure.
