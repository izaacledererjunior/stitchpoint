# ADR 0001: Shell out to the FFmpeg CLI, not cgo/libav bindings, for v1

## Status

Accepted (v1). Revisit in Phase 3.

## Context

Stitchpoint needs to do media-critical-path work — inspecting segment/
keyframe boundaries, and later transcoding/muxing the ad asset to match the
main content — alongside orchestration work (HTTP, manifest parsing, session
state, concurrency). Two ways to get FFmpeg's capabilities into a Go
program:

1. Shell out to the `ffmpeg`/`ffprobe` binaries as subprocesses.
2. Bind directly to `libavformat`/`libavcodec` via cgo.

## Decision

Use the FFmpeg CLI as a subprocess for v1. Defer cgo/libav bindings to a
possible Phase 3.

## Rationale

- **Matches how real systems split this problem.** Production SSAI systems
  commonly pair an orchestration language (Go, Java, Node) with FFmpeg or a
  dedicated transcode farm for the media path — this isn't a toy shortcut,
  it's the actual industry pattern for this layer of the stack.
- **Builds real Go fluency.** The author is deliberately building up Go
  proficiency for the job market; sinking early effort into cgo build
  complexity (cross-compilation, linking against libav, CGO_ENABLED
  toggling) trades Go time for C-binding-plumbing time, with the SSAI logic
  itself gaining nothing from it at this stage.
- **No technical benefit at this scale.** v1 processes VOD assets one splice
  at a time in a portfolio-scale demo, not a high-throughput transcode farm.
  cgo's main payoff — avoiding process-spawn and stdout-parsing overhead —
  doesn't matter until there's a performance story to tell.
- **Keeps the interesting problem in view.** The hard, differentiated part
  of this project is SCTE-35 timing and manifest/segment splicing logic —
  not FFmpeg integration mechanics. Subprocess + stdout/stderr parsing is
  the fastest path to spending time on the former.

## Consequences

- FFmpeg/ffprobe must be present on the host running stitchpoint (documented
  as a prerequisite in the README).
- Output parsing means depending on FFmpeg's CLI/JSON output format
  (`ffprobe -print_format json`), which is far more stable than depending on
  libav's internal API surface, but still an external contract to test
  against.
- A future v2/Phase 3 can replace specific hot-path calls (most likely
  keyframe/segment boundary detection, since that's called most frequently)
  with direct `cgo` + `libavformat` bindings as a deliberate "I optimized
  the critical path and can explain the tradeoff" story — a good technical
  narrative for later, not a v1 requirement.
