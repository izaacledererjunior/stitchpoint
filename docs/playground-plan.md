# Playground — planning notes

Status: **Milestone 1 is functionally done, end to end** — backend
(`internal/contentprep`, `internal/playground`, `cmd/playground-api`) and
frontend (`stitchpoint-playground` — plain HTML/CSS/JS, no build step)
both built and verified together against a real backend, in a real
browser. **Milestone 3 (DASH) is done** — both Period-split/insert
splicing (ADR 0007) and inband `emsg` signaling (ADR 0008), see its
section below. **Milestone 2 (deployment) is done**: both services run on
a real public host behind Caddy/TLS — see the live demo linked from the
README ([stitchpoint.izaac.site](https://stitchpoint.izaac.site)) and
[deploy/README.md](../deploy/README.md) for how. This is a roadmap, not an ADR — it
records what's planned and why it's sequenced this way, so it can be
picked back up without re-deriving context, and revised as reality
(and priorities) change. Once a piece of this actually ships, its real
architecture decisions get their own ADR, same as every other part of
this project (see ADR 0005 for this round's).

## Goal

A publicly hosted, interactive version of stitchpoint: a visitor uploads
a video (or an already-cued HLS/DASH manifest), marks where they want an
ad break, and watches the real stitching pipeline — VAST fetch, download,
transcode, splice — run and produce a playable result in the browser. This
turns the pending "proof artifact" recording (see README) into a live,
always-available demo instead of a static file, and is a stronger
portfolio signal than either alone.

## Constraints already decided

- **Open access, no login**, but with real technical limits (upload
  size/duration caps, a bounded job queue, aggressive TTL cleanup on
  uploaded content) — not full auth, not fully unbounded either. Public
  video upload + server-side transcoding is a real cost/abuse surface;
  these limits exist specifically to bound that, not as a stylistic
  choice.
- **Both HLS and DASH are in scope**, but not built together — DASH is
  its own milestone (see below), sequenced after the HLS path ships,
  because it is architecturally a second manifest/cue engine, not an
  incremental addition to the first.
- **This is two repos, not one**, split along a real Go constraint: this
  module's `internal/` packages (`internal/probe`, `internal/transcode`,
  `internal/manifest`, etc.) are only importable by code inside this same
  module tree. Anything that needs to call them directly — the ad-break
  injection work below — has to live *here*, as its own `cmd/` binary,
  the same pattern already used for `cmd/vastfixture` (see ADR 0004).
  Everything else — the frontend, upload UI, deployment orchestration of
  the whole stack — lives in the sibling repo `stitchpoint-playground`
  (not yet pushed publicly), which talks to this repo's binaries only
  over HTTP, never as a Go import. Keeps this repo scoped to SSAI
  engineering; the product/web layer doesn't dilute it.

## Milestone 1 — HLS playground

The bulk of the actual SSAI engine already exists (`stitchpoint serve`,
`cmd/vastfixture`); this milestone is the web/product layer around it,
plus one genuinely new piece of stitching capability.

**Backend: done.** What shipped, against the plan below:

- [x] `internal/probe.Video` — dimensions/bitrate probing, closing the
      "dynamic encode-parameter probing" README "Future ideas" item.
- [x] `transcode.Params.StartOffset` — `-ss`-based input seeking,
      alongside the existing `MaxDuration` (`-t`).
- [x] `internal/contentprep.InjectBreak` — the ad-break injector
      described below, implemented as three independent encodes rather
      than one continuous encode with extra forced keyframes; see
      [ADR 0005](adr/0005-content-prep-three-pass-encode.md) for why.
- [x] `internal/playground` + `cmd/playground-api` — the async job API
      (upload path and no-upload demo path), with the abuse/cost limits
      below enforced from day one, not added later.
- [x] Verified end-to-end against a real `cmd/vastfixture` instance, not
      just unit-tested in isolation: a real upload, with a real
      caller-chosen break timestamp, produces a real playable stitched
      result over HTTP.

**Frontend: also done** (`stitchpoint-playground`) — plain HTML/CSS/
vanilla JS, no build step or framework (deliberate: this page is a
handful of API calls, a `<video>`, and polling, which doesn't need a
toolchain). Two entry points sharing one job-polling flow — the no-upload
demo button and the upload form — both wired to `playground-api` via
CORS (`Config.AllowedOrigin`, added alongside this). Verified in a real
browser via Puppeteer, not just by inspection or unit tests: real file
upload, real polling, real `hls.js` playback, real manifest highlighting.
A genuine bug was caught this way — disabling the upload form's inputs
for the loading state (before building the outgoing `FormData`) silently
dropped every field, since disabled inputs are excluded from `FormData`
per the HTML spec. Fixed by building `FormData` before disabling
anything, not after.

**Not done yet**: the "already-cued manifest upload" path isn't built as
its own upload flow — the demo endpoint exercises the same underlying
already-cued code path (`runCuedContentJob`), just against a fixed
checked-in file rather than an arbitrary upload. (Public deployment is
Milestone 2, done — see below.)

### New: ad-break injection for raw uploaded video

Today, `stitch`/`serve` require a `.m3u8` that already carries
`#EXT-X-CUE-OUT`/`#EXT-X-CUE-IN`. A visitor uploading a plain MP4 and
picking a timestamp needs a new step in front of that: segment the
upload into HLS and place a real cue at the chosen time. This reuses
existing machinery rather than starting from zero:

- `internal/probe.Duration()`/`Keyframes()` (Phase 3, cgo/libavformat) —
  already reads a media file's real duration and keyframe positions.
- `internal/transcode.EncodeHLS`'s `evenSegmentPlan` — already computes
  segment boundaries and forces keyframes at explicit timestamps via
  `-force_key_frames`; the new work is forcing one of those timestamps to
  land exactly at the user-chosen ad-break point, and using probed
  parameters (width/height/bitrate) instead of the fixed
  `transcode.DefaultParams` this project's own test content currently
  relies on — the "dynamic encode-parameter probing" item already listed
  in README "Future ideas".
- Cue insertion itself (`#EXT-X-CUE-OUT:<duration>` / `#EXT-X-CUE-IN` at
  the forced boundary) is the same tag-writing `manifest.Write` already
  does — no new manifest logic needed, just a new caller.

### New: upload path for an already-cued manifest

Simpler than the above: accept a `.m3u8` (+ segments, or a single-file
upload the backend segments the same way) that already carries valid
cue tags, and hand it straight to the existing `stitch`/`serve` pipeline
unchanged.

### New: web layer

- **`cmd/playground-api`** (this repo — needs direct access to
  `internal/probe`/`internal/transcode`/`internal/manifest`): accepts
  uploads, runs the pipeline as an async job (transcode time is real
  wall-clock seconds to minutes — this cannot be a blocking HTTP
  request), reports job status, returns a session URL when ready.
- **Storage**: uploaded source video and generated segments need to
  survive across requests and (likely) container restarts — a persistent
  volume is enough for v1; no need to design an object-storage
  abstraction before there's a reason to.
- **Frontend** (`stitchpoint-playground` repo, talks to
  `playground-api` over HTTP): upload form, a timeline control to mark
  the ad-break point (not a raw seconds input), an embedded hls.js player
  for the result, and a "quick demo" mode that runs the pipeline against
  the checked-in `testdata`/`vastfixture` with no upload required — the
  default first experience, not the upload path.
- **Nice-to-have, real portfolio value**: show the decoded SCTE-35 cue
  and the raw stitched manifest alongside the player, turning the
  playground into something that demonstrates understanding, not just a
  black-box "it works" button.

### Abuse/cost limits (required for v1, not a later hardening pass)

- Max upload duration/size (suggest matching `testdata`'s own scale,
  ~60-90s) — bounds worst-case transcode time and storage per upload.
- Bounded concurrent job queue — prevents unbounded parallel `ffmpeg`
  processes from a burst of uploads.
- TTL-based cleanup of uploaded content and its generated output,
  separate from (but the same mechanism as) `server.Config.SessionTTL`'s
  existing session cleanup.

## Milestone 2 — Deployment

**Done.** Ended up on a `t3.small` **AWS EC2** instance (free-tier
credits) rather than the Lightsail plan originally sketched below — same
AWS-native rationale, but EC2's free-tier credits made it the cheaper
option once actually compared; see [deploy/README.md](../deploy/README.md)
for the real setup (Caddy for automatic HTTPS, two domains — one for the
API, one for the frontend — and a GitHub Actions pipeline that deploys on
every push to `main` in both repos).

**Local Docker Compose stack: done**, and it's deliberately the same
artifacts the Lightsail deploy will use, not a separate local-only setup:

- [x] `Dockerfile.vastfixture` (stitchpoint repo) — `CGO_ENABLED=0`
      static build, no ffmpeg/cgo dependency (see `internal/adfixture`'s
      package doc for why it doesn't need either), minimal runtime image.
- [x] `Dockerfile.playground-api` (stitchpoint repo) — full cgo build
      (`internal/probe`) with the same `libavformat-dev`/`libavcodec-dev`/
      `libavutil-dev` packages the CI workflow already installs, kept in
      sync with it rather than drifting; `ffmpeg` in the runtime stage
      for `internal/transcode`. A named volume mount point (`/data`) for
      job storage, per the plan's own storage note above.
- [x] `Dockerfile` (frontend, stitchpoint-playground repo) — nginx
      serving the static files, no build step (matching the frontend
      itself having none).
- [x] `docker-compose.yml` (stitchpoint-playground repo) — all three
      services wired together, `vastfixture` reachable only from
      `playground-api` inside the compose network (never exposed to the
      host — the browser never talks to it directly), `playground-api`'s
      `-allowed-origin` matched to the frontend's host port.
- [x] **Verified for real**: built both stitchpoint images and the
      frontend image, brought the full stack up with `docker compose up`,
      and drove it with the same Puppeteer-based real-browser test used
      for the plain-process setup — real demo click and real file upload,
      both through to real `hls.js` playback, against the actual
      containers and the actual Docker network, not a mocked stand-in.

**Still not part of the deploy**: `stitchpoint serve`/`live` aren't in the
compose stack (the playground only exercises `playground-api`'s own splice
path, not the standalone CLI server). Job concurrency limits are enforced
by `internal/playground` itself (`Config.MaxConcurrentJobs`); nothing
extra is enforced at the container/host level yet — worth revisiting now
that real `t3.small` instance sizing is known.

## Milestone 3 — DASH

Deliberately last, and deliberately scoped as its own body of work. The
first slice — parsing plus cue extraction, the DASH equivalent of what
Phase 1 did for SCTE-35/HLS before Phase 2 built the splicer — is done:

- [x] **`internal/mpd`**: parses MPD documents (Period/AdaptationSet/
      Representation/SegmentTemplate/SegmentTimeline) — a different
      format from HLS's `.m3u8`, not an extension of `internal/manifest`.
- [x] **SCTE-35 cue extraction via MPD-level `EventStream`/`Event`**,
      specifically the fully-XML-modeled `urn:scte:scte35:2013:xml`
      scheme — the form real production DAI actually publishes (AWS
      MediaTailor's own DASH documentation gives both a `SpliceInsert`
      and a `TimeSignal` example using exactly this scheme; both are
      `internal/mpd`'s test fixtures, copied verbatim, and one is also
      checked in as `testdata/dash/content.mpd`, reachable via
      `stitchpoint scte35 -mpd`). Reuses `internal/scte35` entirely for
      the actual cue semantics — the XML form is decoded into a real
      `scte35.SpliceInfoSection`, so `scte35.Describe()` and every other
      consumer works identically regardless of which manifest format a
      cue arrived in.
- [x] **Splicing** (`internal/dashsplice`, ADR 0007) — Period split/
      insert, the mechanism named above as the biggest remaining
      architectural difference from the HLS splice engine. Real content
      isn't re-encoded (matches `internal/stitch`'s VOD philosophy):
      the content Period's `SegmentTimeline` is split at the break
      (down to individual segments within a repeated `S/@r` run, not
      just at `S`-array boundaries), the ad's own Period (from the new
      `transcode.EncodeDASH`) is inserted between the two halves, and
      the "after" half's `$Number$`-templated URLs are kept pointed at
      the same, already-existing files via an advanced `StartNumber`.
      `mpd.Write` (new — the package was parse-only before) and
      `mpd.ParseDuration`/`FormatDuration` (new) support this. CLI:
      `stitchpoint dash-stitch -content <mpd> -ad <mpd>|-vast <url> -out <dir>`.
      Verified end-to-end for real: real FFmpeg-encoded DASH content and
      ad, spliced, materialized to a real directory, decoded clean by
      FFmpeg with zero warnings (`TestSplice_RealDASHAssets`) — and the
      same proven separately by running the actual compiled CLI binary
      against real generated fixtures, not just the Go test.
      Building this also surfaced and fixed a second real bug: FFmpeg's
      dash muxer segments audio independently of video, so even a
      cleanly-cut video track could leave a short spurious trailing
      audio segment — the same bug class Phase 3 already fixed for HLS,
      recurring for a different structural reason. Fixed via
      `mpd.SegmentTemplate.MergeTrailingShortSegment`.
- [x] **`emsg`-box inband signaling** (`internal/mpd/emsg.go`, ADR 0008)
      — the one remaining item, closing Milestone 3. A minimal ISOBMFF
      box walker (not a general MP4 demuxer — same scope cut
      `internal/hls` already makes for MPEG-TS PID demuxing, applied
      consistently here) reads `.m4s` segments directly for `emsg` boxes
      (version 0 and 1) carrying `urn:scte:scte35:2013:bin` — verified
      against SCTE 214-1/214-3 via web search before hardcoding, not
      assumed from the XML scheme's naming pattern. Decodes
      `message_data` via the *existing* `scte35.Parse` unchanged (it's a
      raw `splice_info_section`, identical to MPEG-TS/HLS's). CLI:
      `stitchpoint scte35 -segment <path.m4s>`. Tested the same way
      `internal/scte35` tests itself: hand-packed binary vectors built
      byte-for-byte against the spec, reusing a real, previously
      externally-validated cue payload as `message_data` — plus a real
      FFmpeg-produced `.m4s` from `testdata/dash/` with a hand-packed
      `emsg` prepended, proving the walker correctly skips a genuine
      `moof`/`mdat` it doesn't parse, and the actual compiled CLI binary
      run against that same technique.

**Milestone 3 is done.** Both pieces `internal/mpd`'s original package
doc named as deferred — Period-split/insert splicing (ADR 0007) and
inband `emsg` signaling (ADR 0008) — are implemented, tested for real
(unit tests, integration tests against genuine FFmpeg output, and the
actual compiled CLI binaries), and documented.

## Explicitly deferred / out of scope for this plan

- Authentication/accounts — the abuse limits above are meant to make
  open access viable without needing this.
- Object storage / multi-region — a persistent volume is enough until
  there's a concrete reason it isn't.
- Per-viewer ad personalization in the playground — `internal/live`'s
  existing one-window-per-channel simplification (ADR 0003) applies here
  too; no reason to solve a harder problem for a demo than the core
  engine already solves.
