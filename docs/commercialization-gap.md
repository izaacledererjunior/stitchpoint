# Path to commercialization — gap analysis

Status: **not commercially viable today, by design** — this project is
explicitly scoped as a portfolio reference implementation (see
`CLAUDE.md`'s Non-goals and README's "Non-goals"), not a
production-revenue-ready system. This document exists to answer a
different question than the rest of the repo's docs: not "what's left
to finish the demo" (see `progress.md`, `docs/playground-plan.md`) but
"what would it actually take to run a real, revenue-generating channel
on this." The two lists overlap in places but aren't the same — some
items here (real ad-pod filling, per-viewer personalization) are already
named as future work elsewhere; this document is what pulls them
together against one question and adds the categories that were never in
scope at all (ops, security posture, commercial integration).

Nothing here is a criticism of the current state — every gap below is a
deliberate, documented scope cut (see the ADRs and README "Non-goals"),
not an oversight. This is the honest map of the distance between "proves
the engineering" and "sells ad inventory."

## What's already solid enough to build on

- **SCTE-35 parsing** (binary + XML + inband `emsg`) — validated against
  real published references (AWS MediaTailor, AWS MediaConvert), not
  invented fixtures. This layer is close to commercial-grade already.
- **HLS and DASH splice engines** — both manifest-correct, both proven
  against real FFmpeg encodes/decodes end to end, both documented via
  ADR (0003 fail-open/exact-duration, 0007 DASH Period-split).
- **Live SSAI mechanics** — fail-open design, correct discontinuity
  placement, real-channel-validated (see progress.md's Phase 4 section).
- **Engineering discipline itself** — test coverage, ADRs, real
  end-to-end verification over unit tests alone. This is a real asset
  for a commercialization effort, not something to rebuild.

## 1. Ad decisioning — the largest gap

- **Per-viewer personalization.** `internal/live` and `internal/server`
  both serve one shared ad decision per break/session pool, not one per
  viewer. Real DAI (Google Ad Manager DAI, FreeWheel, SpringServe) picks
  a different ad per viewer for the same break — this is the core value
  proposition of "addressable" advertising, and it's not attempted here
  (see ADR 0003's explicit scope cut).
- **Real ad-pod filling.** `live.LoopFiller` (ADR 0006) repeats one
  creative to cover an underfilled break — a placeholder, not something
  that could ship commercially. A real system resolves multiple ads (or
  parses a VAST pod's sequenced `<Ad>` entries) to fill a break with
  distinct, separately-billable spots. `live.BreakFiller` is the seam
  this would plug into; the pod-resolution logic itself doesn't exist.
- **Pre-fetching ahead of the avail.** Ad resolution starts only at
  `#EXT-X-CUE-OUT`/the DASH equivalent, so the fail-open path (playing
  original content because the ad wasn't ready in time) is the common
  case, not the rare one. Real SCTE-35 signals breaks with lead time
  specifically so this doesn't happen.
- **No real ad-server integration exercised end to end.** `internal/vast`
  is a real, spec-conformant VAST client (redirect-following, MediaFile
  selection), but a real fill through Google Ad Manager was explicitly
  deferred (see `progress.md`). Every demo path uses the self-hosted
  `cmd/vastfixture` — deterministic and useful for development, but zero
  evidence yet against a real ad exchange's actual response shapes,
  latency, or failure modes at any volume.
- **No frequency capping, competitive separation, or pacing** — table
  stakes for any commercial ad server, not attempted here (out of scope
  for `cmd/vastfixture`, which is explicitly a fixture, not an ad
  decision engine — see its ADR 0004).

## 2. Scale, reliability, and operations

- **No load testing at any level** — segment throughput, concurrent
  session count, FFmpeg process contention under real concurrency are
  all unmeasured. `internal/playground.Config.MaxConcurrentJobs` and
  `internal/live.Config.MaxConcurrentLive`-equivalent limits exist as
  guardrails, not as numbers derived from actual capacity testing.
- **No redundancy or failover.** `internal/live.Watcher` is one poller
  per channel with no hot standby — a crashed process is a channel
  outage until something restarts it. No leader election, no
  multi-instance coordination.
- **No horizontal scaling design.** `internal/server`'s per-session
  state and `internal/live`'s per-channel poller both assume a single
  process owns the whole channel; nothing here has been designed for (or
  tested against) running N instances behind a load balancer.
- **No real observability.** `log.Printf` lines are the entire signal
  surface — no metrics (fill rate, splice latency, ad-resolution p99, HTTP
  error rates), no tracing, no alerting. A commercial ad-serving system
  lives or dies on being able to see fill rate and error rate in
  real time; that doesn't exist here.
- **No graceful degradation strategy beyond fail-open for a single ad
  resolution.** No circuit breaker on a chronically-failing VAST
  endpoint, no backoff/retry policy, no dead-letter handling for
  segments that fail to encode.
- **No origin resilience or CDN integration story.** Everything assumes
  one origin server; there's no discussion of how stitched output would
  actually get distributed to real viewer scale (CDN cache behavior
  around per-session/per-break manifest variance is itself a real,
  unaddressed problem — dynamic per-session manifests, as
  `internal/server` produces, are notoriously CDN-cache-hostile at
  volume).

## 3. Security posture

- **SSRF protection is explicitly "basic."** `internal/playground`'s
  `validatePublicHTTPURL` (used for the live-channel `upstream` field)
  documents its own gap: it doesn't defend against DNS rebinding
  (resolve to a public IP at validation time, a private one on the
  actual fetch). Closing this needs re-validating on every poll, not
  just at session creation — noted as real, deferred scope, not fixed.
- **No authentication on any API** — `cmd/playground-api`,
  `internal/server`'s dynamic SSAI endpoints, and `cmd/vastfixture` are
  all open by design (appropriate for a public demo, not for a
  commercial control plane serving real ad spend).
- **No rate limiting beyond coarse concurrency caps** — nothing prevents
  a single client from consuming a disproportionate share of
  `MaxConcurrentJobs`/`MaxConcurrentLive`.
- **No secrets/credentials handling story at all** — a real ad-exchange
  integration needs API keys, signed request auth, or OAuth; none of
  that infrastructure exists yet since nothing here has needed to
  authenticate outbound to a real, access-controlled ad server.

## 4. Encoding/packaging precision

- **DASH's independent audio/video segmentation isn't production-grade.**
  `transcode.EncodeDASH`'s FFmpeg-CLI-based muxing needed
  `mpd.MergeTrailingShortSegment` just to avoid a spurious trailing
  segment (see ADR 0007's consequences) — real production DASH packaging
  (Shaka Packager, Bento4, or a commercial encoder's own DASH output)
  provides SAP-aligned segmentation guarantees across tracks that this
  project's tooling doesn't.
- **`probe.Keyframes()` isn't wired into split-point selection** — splits
  land at computed even intervals, not snapped to the source's actual
  GOP structure (named as a possible refinement in README "Future
  ideas", not done).
- **No ABR ladder awareness in the splice path itself** —
  `internal/abrbench` benchmarks bitrate accuracy as a standalone tool,
  but `stitch`/`dashsplice` don't reason about matching an ad against a
  full multi-rendition ladder beyond whatever `internal/transcode`
  was told to encode to; a real content asset with 5+ renditions needs
  every rendition's ad creative matched, which hasn't been exercised.
- **No dynamic encode-parameter probing for arbitrary content** —
  `transcode.DefaultParams` are fixed constants matching this project's
  own test content, not derived from whatever real asset would be
  pointed at it in production (see README "Future ideas").

## 5. Compliance, measurement, and commercial integration

Explicitly out of scope per README's "Non-goals" — not gaps to close
casually, but a materially separate body of work each:

- MRC/viewability accreditation
- Real ad-exchange auction integration (an SSP/exchange engine)
- Client-side player SDK integration (IMA SDK, VAST-in-player) — this
  project deliberately stays server-side only
- Billing, reporting, and reconciliation against delivered impressions
- Content ID / rights management for ad creative

## 6. What's simply never been proven at production scale

- The AWS Lightsail deployment (Milestone 2,
  `docs/playground-plan.md`) never actually shipped — everything has
  only run in local Docker Compose. Zero evidence of behavior under a
  real network path, real DNS, a real TLS terminator, or a real CDN in
  front of it.
- No live channel has been watched for longer than a manual test
  session — multi-hour/24-hour channel stability (memory growth,
  segment-window correctness over thousands of polls, log volume) is
  unverified.
- DASH live SSAI doesn't exist at all — `internal/live` (the real-time
  poller/splicer) is HLS-only; `internal/dashsplice` is VOD-only. A real
  production channel commercializing DASH delivery would need DASH's
  own live-splicing engine, which is a separate, unbuilt body of work
  the same size as `internal/live` itself.

## Rough sequencing, if this were pursued for real

Not a commitment, just the order the gaps above would likely need to be
tackled — each bullet above is real, standalone scope, not a checklist
item:

1. **Ops foundations first**: observability, load testing, redundancy —
   without these, nothing above them can be validated safely.
2. **Security hardening**: auth, real SSRF closure, rate limiting —
   before any real ad spend or real viewer traffic touches this.
3. **Ad decisioning depth**: pre-fetch, real ad-pod filling, per-viewer
   personalization — the actual commercial differentiator, but only
   safe to build once 1–2 are in place.
4. **Encoding precision**: production packaging tools, ABR-ladder-aware
   splicing — needed once real, varied content assets (not this
   project's own test clips) are involved.
5. **Everything in §5** — compliance/measurement/commercial
   integration — is its own workstream, likely requiring dedicated
   product/legal involvement beyond engineering scope entirely.