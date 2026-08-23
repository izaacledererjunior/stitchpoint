# ADR 0004: A self-hosted VAST fixture server, as a separate binary

## Status

Accepted.

## Context

Every real VAST endpoint used against this project so far has caused a
real friction point, both documented in progress.md and README:

1. **Real ad servers no-fill for reasons unrelated to stitching.** A real
   Google Ad Manager tag returned a valid `<VAST version="4.0"/>` no-fill
   almost every time it was requested from this project's development
   network, purely because of geo/inventory targeting — not a bug in
   `internal/vast`, but a real blocker to developing and demoing the
   VAST-driven path.
2. **A captured real response's creative URL expires.** The dev-only
   fallback (`-dev-fallback-vast`, a real captured VAST 4.0 response saved
   under `local/`) worked, but its `MediaFile` is a signed, time-limited
   CDN link. It expired once during real Phase 4 live testing (see
   progress.md's Phase 4 section) — the pipeline's fail-open design
   handled that correctly, but it's still a real dependency on something
   outside this project's control, and it can't be committed to the repo
   at all (the URL carries session-scoped tracking identifiers).

Both are solvable by not depending on any real ad network for local
development: a fixture server that always fills, that hosts its own
creative at a URL that never expires, and that returns a realistically
shaped VAST 4.2 response (including an `AdVerifications`/OMID block, since
real responses commonly carry one and a client should tolerate it).

This does not relitigate README's existing non-goal — "building
VAST-serving/ad-decision-server logic from scratch" is still out of scope
in the sense that means "implementing a real ad exchange/auction/targeting
engine." A deterministic fixture that always returns the same static
response is a different, much narrower thing — the same role Eyevinn's
`test-adserver` already fills for this ecosystem (see the comparison this
decision was based on: actively maintained, but Node.js-based and without
OMID support, which conflicts with this project's "one Go binary, no other
runtime" story and this specific requirement).

## Decision

### 1. `internal/adfixture` + `cmd/vastfixture`, not a subcommand of `stitchpoint`

The fixture is a distinct binary (`cmd/vastfixture`), not a
`stitchpoint fixture` subcommand, and its logic (`internal/adfixture`) is
not imported by, and does not import, any of the core SSAI packages
(`internal/vast`, `internal/stitch`, `internal/server`, `internal/live`).

**Rejected alternative**: a `stitchpoint vastfixture` subcommand, next to
`serve`/`live`. Rejected because this is a genuinely different role — an
ad *server*, not an SSAI *stitcher* — and the project's own plan is to
eventually deploy a hosted playground where the fixture and the stitcher
are realistically two separate deployed services (a playground UI talking
to a `stitchpoint serve`/`live` instance, which in turn talks to this
fixture, or to a real ad network). Keeping them as separate binaries now
means no restructuring is needed to deploy them separately later.

### 2. The creative is real, checked-in, and self-hosted — not a link to anything external

`testdata/vastfixture/creative.mp4` is a short, self-authored MP4 (same
`testsrc`-style synthetic generation approach as `testdata/vod/`), served
directly by `cmd/vastfixture` at a URL rooted at whatever host the fixture
is actually reached at. This directly removes both problems from
"Context": no real ad network involved, and no external URL that can
expire.

### 3. The response's `<Extensions>` block explicitly discloses where a real decision would go

Every VAST response includes an `Extension type="stitchpoint-fixture"`
element whose text names the exact function
(`adfixture.Server.handleVAST`) a production ad-decisioning call — an
SSP/exchange endpoint, a Prebid Server auction, or Google Ad Manager —
would replace. This is deliberately visible in the response itself, not
only in source comments, so anyone inspecting the raw VAST (not just
reading the code) sees the same disclosure.

### 4. Base URL is derived per-request, not hardcoded

`adfixture.Server` reads the incoming request's `Host` (and
`X-Forwarded-Proto`, for behind a load balancer) to build every URL in its
response, rather than requiring a fixed configured hostname.
`Config.BaseURL` exists as an override for when that's not right, but the
default is what makes the same binary correct unchanged whether it's
reached at `localhost:9090` today or a real cloud domain once deployed —
directly serving this project's stated plan to deploy it and build a
playground around it.

## Consequences

- `stitchpoint stitch/serve/live -vast http://localhost:9090/vast` now
  produces a fully working, fully local demo with zero real ad-network
  dependency and zero risk of an expiring URL — verified end-to-end
  (`stitch` and `serve` both tested against a running `vastfixture`
  instance; the fixture's own VAST response was fed through
  `internal/vast.Fetch`, the project's real client, not just asserted
  well-formed in isolation).
- `-dev-fallback-vast` (`Config.DevFallbackVASTPath` on both
  `server.Config` and `live.Config`, and the file it pointed at,
  `local/dev-fallback-vast.xml`) was removed once this landed, rather than
  kept alongside it. Once `vastfixture` covers "always fills, nothing
  expires" strictly better, keeping a second mechanism whose whole reason
  to exist was routing around the same two problems added maintenance
  surface without a remaining distinct purpose — the "validate against a
  specific captured real response" case it also nominally served was
  never actually exercised as its primary use.
- The OMID block is structural only — `handleOMIDScript` serves a stub,
  not a real Open Measurement SDK integration. This is enough to prove
  `internal/vast`'s client tolerates a real-shaped `AdVerifications`
  element without choking on it; it is not, and isn't meant to be, a real
  viewability-measurement implementation.
