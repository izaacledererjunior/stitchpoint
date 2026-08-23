// Package playground is the backend for stitchpoint's hosted demo (see
// docs/playground-plan.md): a visitor uploads a video, marks an ad break,
// and gets back a real stitched HLS result — running internal/contentprep,
// internal/vast, internal/transcode, and internal/stitch, driven by an
// upload instead of a pre-existing content asset. Config.VASTURL is fixed
// at startup, not per-request (a public API accepting an arbitrary fetch
// URL is a real SSRF vector) — point it at cmd/vastfixture (ADR 0004) for
// the hosted demo. Job concurrency, upload size/duration, and job TTL are
// all bounded, since public upload + server-side FFmpeg is a real
// resource-exhaustion surface.
package playground

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/contentprep"
	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
	"github.com/izaacledererjunior/stitchpoint/internal/probe"
	"github.com/izaacledererjunior/stitchpoint/internal/safego"
	"github.com/izaacledererjunior/stitchpoint/internal/stitch"
	"github.com/izaacledererjunior/stitchpoint/internal/transcode"
	"github.com/izaacledererjunior/stitchpoint/internal/vast"
)

// Config configures a Server.
type Config struct {
	// VASTURL is the only ad source this server ever requests from.
	VASTURL string

	// DemoContentPath, if set, is an already-cued HLS playlist used by
	// the no-upload "quick demo" path (GET /api/demo). Disabled (404) if
	// left empty.
	DemoContentPath string

	// WorkDir is the base directory job uploads and output are written
	// under, one subdirectory per job. Defaults to
	// os.TempDir()/stitchpoint-playground.
	WorkDir string

	// MaxUploadBytes caps request body size for uploads. Defaults to
	// 100MB.
	MaxUploadBytes int64

	// MaxUploadDuration caps how long an uploaded video may be, checked
	// after probing it (before the expensive transcode work starts).
	// Defaults to 90s.
	MaxUploadDuration time.Duration

	// MaxConcurrentJobs bounds how many jobs run their FFmpeg pipeline at
	// once; further jobs queue (still accepted, just processed once a
	// slot frees up). Defaults to 2.
	MaxConcurrentJobs int

	// MaxConcurrentLive bounds how many live channels can be watched at
	// once — a separate limit from MaxConcurrentJobs since a live watcher
	// runs continuously, not as a bounded batch. Defaults to 2.
	MaxConcurrentLive int

	// MaxLiveDuration caps how long a live session runs before the
	// janitor stops it — a watcher has no natural "done" state, so
	// without this a public demo accumulates unbounded pollers. Defaults
	// to 15 minutes.
	MaxLiveDuration time.Duration

	// JobTTL is how long a completed (or failed) job's output is kept
	// before the janitor removes it. Defaults to 30 minutes.
	JobTTL time.Duration

	// JanitorInterval is how often expired jobs are swept. Defaults to
	// 5 minutes.
	JanitorInterval time.Duration

	// HTTPClient is used for VAST requests and creative downloads.
	// Defaults to a client with a 30s timeout.
	HTTPClient *http.Client

	// AllowedOrigin is the Access-Control-Allow-Origin value; the
	// frontend runs on a different origin by design. Defaults to "*" —
	// no cookies/auth here for an open CORS policy to weaken. Set
	// explicitly if that stops being true later.
	AllowedOrigin string

	// RateLimitPerMinute caps how many job/demo/live-creation requests
	// (POST /api/jobs, /api/demo, /api/live — each triggers a real
	// upload/transcode/watch pipeline) a single client IP may make per
	// minute; further ones get 429. Doesn't apply to status polling or
	// file serving. Defaults to 20.
	RateLimitPerMinute int
}

func (c *Config) setDefaults() {
	if c.WorkDir == "" {
		c.WorkDir = filepath.Join(os.TempDir(), "stitchpoint-playground")
	}
	if c.MaxUploadBytes == 0 {
		c.MaxUploadBytes = 100 << 20 // 100MB
	}
	if c.MaxUploadDuration == 0 {
		c.MaxUploadDuration = 90 * time.Second
	}
	if c.MaxConcurrentJobs == 0 {
		c.MaxConcurrentJobs = 2
	}
	if c.MaxConcurrentLive == 0 {
		c.MaxConcurrentLive = 2
	}
	if c.MaxLiveDuration == 0 {
		c.MaxLiveDuration = 15 * time.Minute
	}
	if c.JobTTL == 0 {
		c.JobTTL = 30 * time.Minute
	}
	if c.JanitorInterval == 0 {
		c.JanitorInterval = 5 * time.Minute
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.AllowedOrigin == "" {
		c.AllowedOrigin = "*"
	}
	if c.RateLimitPerMinute <= 0 {
		c.RateLimitPerMinute = 20
	}
}

// Status is a job's lifecycle state.
type Status string

// Status values.
const (
	StatusPending    Status = "pending"    // accepted, waiting for a concurrency slot
	StatusProcessing Status = "processing" // running the pipeline now
	StatusReady      Status = "ready"      // stitched.m3u8 + segments available
	StatusFailed     Status = "failed"     // see the job's Error
)

// Job is one upload/demo run's state, as reported by GET /api/jobs/{id}.
type Job struct {
	ID        string    `json:"id"`
	Status    Status    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`

	dir string // output session dir; not exposed in the JSON view
}

// Server is the playground's HTTP API.
type Server struct {
	cfg         Config
	sem         chan struct{} // bounds MaxConcurrentJobs
	rateLimiter *httpserve.RateLimiter

	mu   sync.Mutex
	jobs map[string]*Job

	// liveMu/liveSessions are separate from mu/jobs: a live session is a
	// different shape from a Job, not a variant of one.
	liveMu       sync.Mutex
	liveSessions map[string]*LiveSession

	mux         *http.ServeMux
	janitorDone chan struct{}
}

// New builds a Server for the given Config and starts its background
// janitor.
func New(cfg Config) (*Server, error) {
	cfg.setDefaults()
	if cfg.VASTURL == "" {
		return nil, fmt.Errorf("playground: VASTURL is required")
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o750); err != nil {
		return nil, fmt.Errorf("playground: creating work dir: %w", err)
	}

	s := &Server{
		cfg:          cfg,
		sem:          make(chan struct{}, cfg.MaxConcurrentJobs),
		rateLimiter:  httpserve.NewRateLimiter(cfg.RateLimitPerMinute, time.Minute),
		jobs:         make(map[string]*Job),
		liveSessions: make(map[string]*LiveSession),
		janitorDone:  make(chan struct{}),
	}

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /healthz", httpserve.Healthz)
	s.mux.Handle("POST /api/jobs", s.rateLimiter.Middleware(http.HandlerFunc(s.handleCreateJob)))
	s.mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	s.mux.HandleFunc("GET /api/jobs/{id}/{file}", s.handleJobFile)
	s.mux.Handle("POST /api/demo", s.rateLimiter.Middleware(http.HandlerFunc(s.handleDemo)))
	s.registerLiveRoutes()

	go s.runJanitor()
	return s, nil
}

// Handler returns the playground's HTTP routes wrapped with CORS and
// baseline security headers.
func (s *Server) Handler() http.Handler {
	return httpserve.SecurityHeaders(corsMiddleware(s.cfg.AllowedOrigin, s.mux))
}

// corsMiddleware sets Access-Control-Allow-Origin on every response and
// answers CORS preflight (OPTIONS) requests directly.
func corsMiddleware(allowedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Close stops the background janitor and every running live watcher
// (each polls forever otherwise). Doesn't remove job directories or
// cancel in-flight batch jobs.
func (s *Server) Close() {
	close(s.janitorDone)
	s.rateLimiter.Close()
	s.liveMu.Lock()
	sessions := make([]*LiveSession, 0, len(s.liveSessions))
	for _, session := range s.liveSessions {
		sessions = append(sessions, session)
	}
	s.liveMu.Unlock()
	for _, session := range sessions {
		s.stopLiveSession(session)
	}
}

// handleCreateJob accepts a multipart upload (field "video") plus
// "break_start"/"break_duration" form fields, and starts a job running
// internal/contentprep.InjectBreak followed by the same
// VAST → download → transcode → splice pipeline `stitchpoint stitch` runs.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	if err := r.ParseMultipartForm(s.cfg.MaxUploadBytes); err != nil {
		// Only a genuine *http.MaxBytesError is 413; anything else
		// (bad Content-Type, malformed body) is a 400.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "upload too large: "+err.Error(), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "malformed upload: "+err.Error(), http.StatusBadRequest)
		}
		return
	}

	breakStart, err := parseSeconds(r.FormValue("break_start"))
	if err != nil {
		http.Error(w, "invalid break_start: "+err.Error(), http.StatusBadRequest)
		return
	}
	breakDuration, err := parseSeconds(r.FormValue("break_duration"))
	if err != nil {
		http.Error(w, "invalid break_duration: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		http.Error(w, "missing \"video\" file field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	job, err := s.newJob()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// header.Filename is attacker-controlled; sanitize before it touches
	// a path (filepath.Ext alone doesn't reject "x.mp4/../../evil").
	ext := sanitizeExt(header.Filename)
	uploadPath := filepath.Join(job.dir, "upload"+ext)
	if err := saveUpload(uploadPath, file); err != nil {
		http.Error(w, "saving upload: "+err.Error(), http.StatusInternalServerError)
		return
	}

	go s.runUploadJob(job, uploadPath, breakStart, breakDuration)

	w.Header().Set("Location", "/api/jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, s.snapshot(job))
}

// handleDemo starts a job against Config.DemoContentPath, so a visitor
// can see a real result with no upload — same job API as an upload.
func (s *Server) handleDemo(w http.ResponseWriter, _ *http.Request) {
	if s.cfg.DemoContentPath == "" {
		http.Error(w, "demo mode not configured", http.StatusNotFound)
		return
	}

	job, err := s.newJob()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	go s.runCuedContentJob(job, s.cfg.DemoContentPath)

	w.Header().Set("Location", "/api/jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, s.snapshot(job))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	job, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.snapshot(job))
}

// snapshot returns a copy of job's fields taken under s.mu — the
// background job goroutine mutates Status/Error through setStatus under
// the same lock, so reading them for a JSON response has to go through
// this rather than touching the shared *Job directly (a real data race
// caught by `go test -race`, not a hypothetical one).
func (s *Server) snapshot(job *Job) Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *job
}

func (s *Server) handleJobFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	job, ok := s.jobs[r.PathValue("id")]
	var status Status
	var dir string
	if ok {
		status, dir = job.Status, job.dir
	}
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if status != StatusReady {
		http.Error(w, "job is "+string(status)+", not ready", http.StatusConflict)
		return
	}
	http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.PathValue("file"))))
}

func (s *Server) newJob() (*Job, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.cfg.WorkDir, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	job := &Job{ID: id, Status: StatusPending, CreatedAt: time.Now(), dir: dir}

	s.mu.Lock()
	s.jobs[id] = job
	s.mu.Unlock()
	return job, nil
}

// runUploadJob is the upload path: probe+validate duration, inject the
// break, resolve+splice the ad, same as runCuedContentJob from the
// "already cued" step onward.
func (s *Server) runUploadJob(job *Job, uploadPath string, breakStart, breakDuration time.Duration) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	defer s.recoverJob(job, "playground.runUploadJob")
	s.setStatus(job, StatusProcessing, "")

	duration, err := probe.Duration(uploadPath)
	if err != nil {
		s.fail(job, fmt.Errorf("reading uploaded video: %w", err))
		return
	}
	if duration > s.cfg.MaxUploadDuration {
		s.fail(job, fmt.Errorf("upload is %v, longer than the %v limit", duration, s.cfg.MaxUploadDuration))
		return
	}

	contentDir := filepath.Join(job.dir, "content")
	content, err := contentprep.InjectBreak(uploadPath, contentDir, breakStart, breakDuration)
	if err != nil {
		s.fail(job, fmt.Errorf("preparing content: %w", err))
		return
	}

	s.spliceAndFinish(job, content, contentDir)
}

// runCuedContentJob is the demo path: skip contentprep entirely, since
// contentPath is already cued.
func (s *Server) runCuedContentJob(job *Job, contentPath string) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	defer s.recoverJob(job, "playground.runCuedContentJob")
	s.setStatus(job, StatusProcessing, "")

	f, err := os.Open(contentPath)
	if err != nil {
		s.fail(job, fmt.Errorf("opening demo content: %w", err))
		return
	}
	content, err := manifest.Parse(f)
	_ = f.Close()
	if err != nil {
		s.fail(job, fmt.Errorf("parsing demo content: %w", err))
		return
	}

	s.spliceAndFinish(job, content, filepath.Dir(contentPath))
}

// spliceAndFinish runs the shared back half of both job types: resolve
// the ad, download/encode it to match content, splice it in, and
// materialize the result into job.dir.
func (s *Server) spliceAndFinish(job *Job, content *manifest.Playlist, contentDir string) {
	resolved, err := vast.Fetch(s.cfg.HTTPClient, s.cfg.VASTURL)
	if err != nil {
		s.fail(job, fmt.Errorf("resolving VAST: %w", err))
		return
	}
	mediaFile, ok := resolved.SelectMediaFile()
	if !ok {
		s.fail(job, fmt.Errorf("VAST ad %q has no usable progressive MP4 MediaFile", resolved.AdTitle))
		return
	}

	creativePath := filepath.Join(job.dir, "creative.mp4")
	if err := transcode.DownloadFile(s.cfg.HTTPClient, mediaFile.URL, creativePath); err != nil {
		s.fail(job, fmt.Errorf("downloading creative: %w", err))
		return
	}
	adDir := filepath.Join(job.dir, "ad")
	ad, err := transcode.EncodeHLS(creativePath, adDir, transcode.DefaultParams)
	if err != nil {
		s.fail(job, fmt.Errorf("encoding creative: %w", err))
		return
	}

	// Ad segment filenames commonly collide with content's (both default
	// to seg_NNN.ts) — rename up front.
	adSourceFile := make(map[string]string, len(ad.Segments))
	renamedAd := &manifest.Playlist{Segments: make([]manifest.Segment, len(ad.Segments))}
	for i, seg := range ad.Segments {
		newURI := "ad_" + seg.URI
		adSourceFile[newURI] = seg.URI
		seg.URI = newURI
		renamedAd.Segments[i] = seg
	}

	// PreserveAllContent: content from internal/contentprep authors every
	// break as a pure insertion point, so there's no placeholder range to
	// duration-match against. LoopAdToFillBreak: a real VAST creative
	// rarely matches the break's target duration exactly, and this is the
	// path most reviewers actually watch play — see
	// stitch.Options.LoopAdToFillBreak.
	out, err := stitch.SpliceWithOptions(content, renamedAd, stitch.Options{PreserveAllContent: true, LoopAdToFillBreak: true})
	if err != nil {
		s.fail(job, fmt.Errorf("splicing: %w", err))
		return
	}

	contentURIs := make(map[string]bool, len(content.Segments))
	for _, seg := range content.Segments {
		contentURIs[seg.URI] = true
	}
	copied := make(map[string]bool)
	for _, seg := range out.Segments {
		if copied[seg.URI] {
			continue
		}
		var src string
		switch {
		case contentURIs[seg.URI]:
			src = filepath.Join(contentDir, seg.URI)
		case adSourceFile[seg.URI] != "":
			src = filepath.Join(adDir, adSourceFile[seg.URI])
		default:
			s.fail(job, fmt.Errorf("internal error: segment %q traces to neither content nor ad", seg.URI))
			return
		}
		if err := copyFile(src, filepath.Join(job.dir, seg.URI)); err != nil {
			s.fail(job, fmt.Errorf("copying segment %q: %w", seg.URI, err))
			return
		}
		copied[seg.URI] = true
	}

	mf, err := os.Create(filepath.Join(job.dir, "stitched.m3u8"))
	if err != nil {
		s.fail(job, err)
		return
	}
	writeErr := manifest.Write(mf, out)
	closeErr := mf.Close()
	if writeErr != nil {
		s.fail(job, writeErr)
		return
	}
	if closeErr != nil {
		s.fail(job, closeErr)
		return
	}

	s.setStatus(job, StatusReady, "")
}

func (s *Server) fail(job *Job, err error) {
	s.setStatus(job, StatusFailed, err.Error())
}

// recoverJob recovers a panic in a job goroutine, logs it, and marks the
// job failed — without this, a panic would leave the job stuck in
// "processing" forever from the client's point of view (the semaphore
// slot is still freed correctly; that release is deferred separately in
// the caller and runs regardless of a panic).
func (s *Server) recoverJob(job *Job, name string) {
	if r := recover(); r != nil {
		slog.Error("panic recovered", "goroutine", name, "job", job.ID, "panic", r, "stack", string(debug.Stack()))
		s.fail(job, fmt.Errorf("internal error"))
	}
}

func (s *Server) setStatus(job *Job, status Status, errMsg string) {
	s.mu.Lock()
	job.Status = status
	job.Error = errMsg
	s.mu.Unlock()
}

// runJanitor periodically removes job directories older than JobTTL, the
// same mechanism (and reasoning) as internal/server's session janitor.
func (s *Server) runJanitor() {
	ticker := time.NewTicker(s.cfg.JanitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.safeSweep()
		case <-s.janitorDone:
			return
		}
	}
}

// safeSweep runs both janitor sweeps with panic recovery, so a bug
// tripped by one sweep degrades that tick, not the janitor loop.
func (s *Server) safeSweep() {
	defer safego.Recover("playground.janitor.sweep")
	s.sweepExpiredJobs()
	s.sweepExpiredLive()
}

func (s *Server) sweepExpiredJobs() {
	cutoff := time.Now().Add(-s.cfg.JobTTL)

	s.mu.Lock()
	var expired []string
	for id, job := range s.jobs {
		if job.CreatedAt.Before(cutoff) {
			expired = append(expired, id)
			delete(s.jobs, id)
		}
	}
	s.mu.Unlock()

	for _, id := range expired {
		dir := filepath.Join(s.cfg.WorkDir, id)
		if err := os.RemoveAll(dir); err != nil {
			slog.Error("playground: removing expired job dir", "dir", dir, "err", err)
		}
	}
}

func parseSeconds(s string) (time.Duration, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	return time.Duration(f * float64(time.Second)), nil
}

// sanitizeExt returns a filesystem-safe extension for an uploaded file,
// rejecting anything but a short run of ASCII letters/digits — filename
// is client-controlled and filepath.Ext alone doesn't validate it.
func sanitizeExt(filename string) string {
	ext := filepath.Ext(filename)
	isSafe := func(r rune) bool {
		return r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	for _, r := range ext {
		if !isSafe(r) {
			return ""
		}
	}
	if len(ext) > 8 {
		return ""
	}
	return ext
}

func saveUpload(destPath string, r io.Reader) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
