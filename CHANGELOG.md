# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); see
[docs/adr](docs/adr) for the reasoning behind individual decisions.

## [Unreleased]

### Added
- `internal/httpserve`: shared graceful shutdown (SIGINT/SIGTERM →
  `http.Server.Shutdown`) for every HTTP command; `GET /healthz` on
  `internal/server`, `internal/playground`, and `internal/adfixture`;
  `net/http/pprof` behind an opt-in `-pprof-addr` flag, served on its own
  listener, never merged into a public-facing mux.
- Structured logging (`log/slog`) across `internal/server`,
  `internal/playground`, `internal/adfixture`, and `internal/live`,
  replacing ad hoc `log.Printf` calls.
- `FuzzParse` (`internal/scte35`) and benchmarks for the SCTE-35 bitreader,
  manifest parse/write, `evenSegmentPlan`, and `stitch.SpliceWithOptions`.
- `golangci-lint` (with `gosec`, `errorlint`, `exhaustive`, and others
  beyond the default set), `staticcheck`, and `govulncheck` wired into both
  CI and a local `Makefile` (`make check`).
- Docker images hardened: non-root users, `vastfixture` moved to a
  `distroless/static` base, `trivy image` scanning added to CI.
- `.github/dependabot.yml` (gomod, github-actions, docker) — the
  structural fix for the `go.mod` version drift below.

### Fixed
- `go.mod` was pinned to `go 1.22.2`, carrying 39 known CVEs already fixed
  in later Go releases (all in the standard library — this project has no
  third-party dependencies). Bumped to `1.26.7`.
- A path-traversal gap in the playground upload handler: an uploaded
  file's client-supplied filename extension was joined into a filesystem
  path without validation.

## Phase 1 — SCTE-35 parser
Bit-level `splice_info_section` decoding (`splice_insert`, `time_signal`),
including malformed/truncated input handling.

## Phase 2 — VOD SSAI stitcher
Manifest-level HLS splice at signaled ad breaks, plus a dynamic HTTP
ad-insertion server (VAST → download → transcode → splice per session).

## Phase 3 — cgo/libavformat optimization
Segment/keyframe boundary detection moved from shelling out to `ffprobe`
to direct `libavformat`/`libavcodec` bindings via cgo (`internal/probe`).
See [docs/adr/0002](docs/adr/0002-cgo-libavformat-for-boundary-detection.md).

## Phase 4 — Live SSAI
Real-time dynamic SSAI for a live channel: polls an upstream manifest,
detects breaks as they appear, and splices with a fail-open policy while
an ad resolves in the background. See
[docs/adr/0003](docs/adr/0003-live-ssai-fail-open-and-exact-duration.md).

## Playground
Hosted-demo backend (`internal/playground`, `cmd/playground-api`): upload
or quick-demo a video, get a real stitched HLS result back, including a
DASH slice (`internal/dashsplice`) and inband `emsg` SCTE-35 signaling
(`internal/mpd`). See `docs/playground-plan.md`.
