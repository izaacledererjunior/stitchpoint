package playground

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/live"
	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
	"github.com/izaacledererjunior/stitchpoint/internal/ssrf"
)

// LiveStatus is a live session's lifecycle state — just two values, since
// a live.Watcher has no "ready" state: it polls/serves continuously from
// New until stopped or swept by MaxLiveDuration.
type LiveStatus string

// LiveStatus values.
const (
	LiveStatusRunning LiveStatus = "running"
	LiveStatusStopped LiveStatus = "stopped"
)

// LiveSession is one watched channel's state, as reported by
// GET /api/live/{id}.
type LiveSession struct {
	ID          string     `json:"id"`
	UpstreamURL string     `json:"upstreamURL"`
	Status      LiveStatus `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`

	watcher *live.Watcher // nil once Status is LiveStatusStopped
}

// registerLiveRoutes wires the live-channel endpoints into s.mux.
func (s *Server) registerLiveRoutes() {
	s.mux.Handle("POST /api/live", s.rateLimiter.Middleware(http.HandlerFunc(s.handleCreateLive)))
	s.mux.HandleFunc("GET /api/live/{id}", s.handleGetLive)
	s.mux.HandleFunc("DELETE /api/live/{id}", s.handleStopLive)
	// Alias reachable via navigator.sendBeacon on beforeunload (a closed
	// tab should free the slot immediately) — sendBeacon only sends POST,
	// and DELETE is CORS-preflighted, which proved unreliable during page
	// unload in testing.
	s.mux.HandleFunc("POST /api/live/{id}/stop", s.handleStopLive)
	s.mux.HandleFunc("GET /api/live/{id}/stitched.m3u8", s.handleLiveManifest)
	// live.Watcher's segment URIs are already "/live/ads/{gen}/{file}";
	// handleLiveManifest rewrites them with this session's prefix, so
	// this route matches that exact tail.
	s.mux.HandleFunc("GET /api/live/{id}/live/ads/{gen}/{file}", s.handleLiveAdSegment)
}

// handleCreateLive starts watching r.FormValue("upstream"), the same
// capability `stitchpoint live -upstream` gives as a CLI tool.
func (s *Server) handleCreateLive(w http.ResponseWriter, r *http.Request) {
	// The frontend always submits multipart/form-data via fetch(FormData)
	// even with no file field; r.ParseForm alone leaves it unparsed. Try
	// multipart first, fall back to ParseForm for a plain urlencoded POST.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "malformed request: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	upstream := r.FormValue("upstream")
	if upstream == "" {
		http.Error(w, "missing \"upstream\" field", http.StatusBadRequest)
		return
	}
	if err := validateUpstreamURL(upstream); err != nil {
		http.Error(w, "invalid upstream: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.liveMu.Lock()
	// Counts only LiveStatusRunning: stopped sessions stay in
	// s.liveSessions indefinitely (see stopLiveSession's doc — a client
	// asking later still sees "stopped"), so counting every entry here
	// would make this a lifetime-sessions-ever-created cap instead of a
	// concurrency cap, permanently wedging new sessions once that many
	// have ever been created.
	running := 0
	for _, sess := range s.liveSessions {
		if sess.Status == LiveStatusRunning {
			running++
		}
	}
	if running >= s.cfg.MaxConcurrentLive {
		s.liveMu.Unlock()
		http.Error(w, fmt.Sprintf("too many live sessions running (limit %d) — stop one first", s.cfg.MaxConcurrentLive), http.StatusTooManyRequests)
		return
	}
	s.liveMu.Unlock()

	id, err := randomID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	watcher, err := live.New(live.Config{
		UpstreamURL:    upstream,
		DefaultVASTURL: s.cfg.VASTURL,
		WorkDir:        filepath.Join(s.cfg.WorkDir, "live-"+id),
		HTTPClient:     s.cfg.HTTPClient,
	})
	if err != nil {
		http.Error(w, "starting watcher: "+err.Error(), http.StatusBadGateway)
		return
	}

	session := &LiveSession{
		ID:          id,
		UpstreamURL: upstream,
		Status:      LiveStatusRunning,
		CreatedAt:   time.Now(),
		watcher:     watcher,
	}
	s.liveMu.Lock()
	s.liveSessions[id] = session
	s.liveMu.Unlock()

	writeJSON(w, http.StatusAccepted, s.liveSnapshot(session))
}

func (s *Server) handleGetLive(w http.ResponseWriter, r *http.Request) {
	session, ok := s.liveSession(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.liveSnapshot(session))
}

// handleStopLive stops the watcher but keeps the session record so a
// later GET returns "stopped" instead of a 404.
func (s *Server) handleStopLive(w http.ResponseWriter, r *http.Request) {
	session, ok := s.liveSession(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.stopLiveSession(session)
	w.WriteHeader(http.StatusNoContent)
}

// handleLiveManifest serves the watcher's current stitched window,
// rewriting ad segment URIs with this session's prefix so concurrent
// sessions don't collide on live.Watcher's shared /live/ads/ path.
func (s *Server) handleLiveManifest(w http.ResponseWriter, r *http.Request) {
	session, ok := s.liveSession(r.PathValue("id"))
	if !ok || session.watcher == nil {
		http.NotFound(w, r)
		return
	}

	pl := session.watcher.CurrentManifest()
	scoped := &manifest.Playlist{
		Version:        pl.Version,
		TargetDuration: pl.TargetDuration,
		MediaSequence:  pl.MediaSequence,
		PlaylistType:   pl.PlaylistType,
		EndList:        pl.EndList,
		Segments:       make([]manifest.Segment, len(pl.Segments)),
	}
	prefix := "/api/live/" + session.ID
	for i, seg := range pl.Segments {
		if strings.HasPrefix(seg.URI, "/live/ads/") {
			seg.URI = prefix + seg.URI
		}
		scoped.Segments[i] = seg
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	if err := manifest.Write(w, scoped); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLiveAdSegment(w http.ResponseWriter, r *http.Request) {
	session, ok := s.liveSession(r.PathValue("id"))
	if !ok || session.watcher == nil {
		http.NotFound(w, r)
		return
	}
	path, ok := session.watcher.AdSegmentPath(r.PathValue("gen"), r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) liveSession(id string) (*LiveSession, bool) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	session, ok := s.liveSessions[id]
	return session, ok
}

func (s *Server) liveSnapshot(session *LiveSession) LiveSession {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return LiveSession{
		ID:          session.ID,
		UpstreamURL: session.UpstreamURL,
		Status:      session.Status,
		CreatedAt:   session.CreatedAt,
	}
}

// stopLiveSession closes the watcher and marks the session stopped;
// idempotent (live.Watcher.Close guards against a double-close panic).
func (s *Server) stopLiveSession(session *LiveSession) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if session.watcher != nil {
		session.watcher.Close()
		session.watcher = nil
	}
	session.Status = LiveStatusStopped
}

// sweepExpiredLive stops sessions running longer than
// Config.MaxLiveDuration — unlike sweepExpiredJobs, it doesn't delete the
// record or remove WorkDir, so a client asking later still sees "stopped".
func (s *Server) sweepExpiredLive() {
	cutoff := time.Now().Add(-s.cfg.MaxLiveDuration)

	s.liveMu.Lock()
	var expired []*LiveSession
	for _, session := range s.liveSessions {
		if session.Status == LiveStatusRunning && session.CreatedAt.Before(cutoff) {
			expired = append(expired, session)
		}
	}
	s.liveMu.Unlock()

	for _, session := range expired {
		s.stopLiveSession(session)
	}
}

// validateUpstreamURL is a package-level var (not a direct call) so this
// package's own tests can swap in a permissive stand-in to point at an
// httptest.Server (127.0.0.1, which the real check correctly rejects) —
// no production config can disable it.
var validateUpstreamURL = ssrf.ValidatePublicHTTPURL
