# ADR 0008: Inband SCTE-35 via `emsg` boxes — minimal ISOBMFF box walker, not a demuxer

## Status

Accepted (Milestone 3, DASH — the one remaining piece named when the
Period-splice work landed, ADR 0007).

## Context

DASH carries SCTE-35 two ways (SCTE 214-1): out-of-band, as MPD-level
`EventStream`/`Event` elements (`internal/mpd`'s original scope, `urn:
scte:scte35:2013:xml`), and inband, as `emsg` boxes embedded directly in
`.m4s` media segments (`urn:scte:scte35:2013:bin`). These aren't two
encodings of the same mechanism: an out-of-band cue is visible from the
manifest alone, before a player fetches anything; an inband cue is only
discoverable once the specific segment carrying it is actually fetched —
a real, materially different signaling path, not a formatting detail.

The exact scheme string wasn't assumed from the XML scheme's naming
pattern — verified via web search against SCTE 214-1/214-3's own
description and corroborated by a second independent source
(docs.unified-streaming.com), the same "validate against a real
published reference before hardcoding it" discipline this project's
other SCTE-35 scheme constants already follow (`internal/mpd`'s
`SchemeSCTE35XML`, `internal/scte35`'s AWS MediaConvert-validated test
vector).

## Decision

Added `internal/mpd/emsg.go`: a minimal ISOBMFF box walker
(`readBoxHeader`, handling both the 32-bit and 64-bit "largesize" forms)
that reads only enough to iterate a segment's *top-level* boxes and
decode `'emsg'` specifically (version 0 and 1, per ISO/IEC 23009-1
§5.10.3.3) — not a general MP4 demuxer. `moof`/`mdat`/any other top-level
box is skipped by its declared size without being parsed at all.
`decodeEmsg` extracts `scheme_id_uri`, and when it's `SchemeSCTE35Bin`,
decodes `message_data` via the *existing* `scte35.Parse` unchanged — the
binary payload is a raw `splice_info_section`, identical to what
`internal/scte35` already parses from MPEG-TS/HLS, so no new decoding
logic was needed there at all.

**Rejected alternative**: a general-purpose ISOBMFF/fMP4 parser (full
`moof`/`traf`/`trun` box trees, sample tables, etc.). Rejected as real,
unbounded scope disproportionate to what this project needs — the same
reasoning `internal/hls` already applied to skip MPEG-TS PID demuxing
(README "Future ideas"), now applied consistently to DASH's inband
carriage instead of being treated as a special case.

**Rejected alternative**: only version 1 (the field layout addressing
version 0's timestamp-rollover ambiguity, and the one SCTE
214-1/214-3-oriented sources emphasize). Implemented both because the
spec difference is small (field order plus `presentation_time_delta` vs.
absolute `presentation_time`) and a real segment produced by an older
packager can still legitimately use version 0 — refusing it outright
would be a real, avoidable gap for a small amount of extra code, not
speculative robustness.

## Consequences

- `EmsgCue.PresentationTime`'s meaning depends on `Version` (absolute for
  v1, segment-relative delta for v0) — documented on the field rather
  than normalized into one interpretation, since normalizing v0's delta
  into an absolute time would require knowing the segment's own
  presentation-time offset, which this function — reading one segment in
  isolation — doesn't have.
- `event_duration == 0xFFFFFFFF` (the spec's reserved "unknown duration"
  sentinel) is surfaced as `EventDurationUnknown`, not silently converted
  into a nonsensically large `time.Duration` — the same "a sentinel isn't
  a real value" handling `TimeSpecifiedFlag`/absent-`presentationTime`
  already get elsewhere in this project's SCTE-35 code.
- Tested the same way `internal/scte35`'s own tests are: hand-packed
  binary vectors built byte-for-byte against the spec's syntax tables
  (not opaque checked-in blobs), reusing a real, previously
  externally-validated SCTE-35 payload (the same cue string README's own
  top-level usage example verifies) as `message_data` — proving the box
  layer against a real cue, not an invented one. Additionally verified
  against a genuine FFmpeg-produced `.m4s` segment
  (`TestExtractEmsgCues_RealSegment`, `testdata/dash/content/`) with a
  hand-packed `emsg` box prepended, confirming the walker correctly
  skips a real `moof`/`mdat` structure it doesn't otherwise understand —
  and via the actual compiled CLI (`stitchpoint scte35 -segment`)
  against the same technique, not just the Go test suite.
- `internal/mpd`'s package doc no longer names splicing or emsg as
  deferred — both pieces Milestone 3 originally scoped are done.