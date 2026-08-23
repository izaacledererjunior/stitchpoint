// Package adfixture is a small, deterministic VAST ad server for local
// development/CI — never a real ad-decisioning system. It always fills
// from a self-hosted, checked-in creative (see README "Test assets"),
// avoiding real ad servers' no-fill behavior and signed CDN URLs that
// expire. See docs/adr/0004 for the full rationale.
package adfixture

import (
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
)

// Config configures a fixture Server.
type Config struct {
	// CreativePath is the local path to the progressive MP4 this server
	// serves as the ad creative. Required.
	CreativePath string

	// CreativeWidth, CreativeHeight, CreativeBitrateKbps describe
	// CreativePath, reported verbatim in the VAST MediaFile element.
	CreativeWidth       int
	CreativeHeight      int
	CreativeBitrateKbps int

	// CreativeDuration is the creative's real playable duration,
	// reported in the VAST <Duration> element.
	CreativeDuration time.Duration

	// BaseURL is the scheme+host for URLs in response bodies. Leave unset
	// to derive it per-request (see baseURLFor).
	BaseURL string

	// RateLimitPerMinute caps how many /vast requests (the "ad decision"
	// endpoint) a single client IP may make per minute; further ones get
	// 429. Defaults to 20.
	RateLimitPerMinute int
}

// DefaultConfig matches testdata/demo-ad/advertising.mp4 as checked in.
var DefaultConfig = Config{
	CreativeWidth:       960,
	CreativeHeight:      540,
	CreativeBitrateKbps: 2267,
	CreativeDuration:    10 * time.Second,
}

// Server is a ready-to-serve VAST fixture.
type Server struct {
	cfg         Config
	rateLimiter *httpserve.RateLimiter
}

// New validates cfg and returns a Server. Call Close when done, to stop
// the rate limiter's background sweep.
func New(cfg Config) (*Server, error) {
	if cfg.CreativePath == "" {
		return nil, fmt.Errorf("adfixture: CreativePath is required")
	}
	if _, err := os.Stat(cfg.CreativePath); err != nil {
		return nil, fmt.Errorf("adfixture: creative file: %w", err)
	}
	if cfg.CreativeWidth == 0 || cfg.CreativeHeight == 0 {
		cfg.CreativeWidth, cfg.CreativeHeight = DefaultConfig.CreativeWidth, DefaultConfig.CreativeHeight
	}
	if cfg.CreativeBitrateKbps == 0 {
		cfg.CreativeBitrateKbps = DefaultConfig.CreativeBitrateKbps
	}
	if cfg.CreativeDuration == 0 {
		cfg.CreativeDuration = DefaultConfig.CreativeDuration
	}
	if cfg.RateLimitPerMinute <= 0 {
		cfg.RateLimitPerMinute = 20
	}
	return &Server{cfg: cfg, rateLimiter: httpserve.NewRateLimiter(cfg.RateLimitPerMinute, time.Minute)}, nil
}

// Close stops the rate limiter's background sweep.
func (s *Server) Close() {
	s.rateLimiter.Close()
}

// Handler returns the fixture's HTTP routes:
//
//	GET /vast                       the VAST 4.2 InLine XML response
//	GET /creative.mp4               the real, checked-in ad creative
//	GET /omid-verification.js       a stub OMID verification script
//	GET /track                      tracking-pixel sink (204, logs the event)
//
// handleVAST is the entire "decision" — a real deployment would replace
// its body with a call to an actual ad-decision endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserve.Healthz)
	mux.Handle("/vast", s.rateLimiter.Middleware(http.HandlerFunc(s.handleVAST)))
	mux.HandleFunc("/creative.mp4", s.handleCreative)
	mux.HandleFunc("/omid-verification.js", s.handleOMIDScript)
	mux.HandleFunc("/track", s.handleTrack)
	return httpserve.SecurityHeaders(mux)
}

// handleVAST is the "ad decision." See Handler's doc comment.
func (s *Server) handleVAST(w http.ResponseWriter, r *http.Request) {
	base := s.baseURLFor(r)
	doc := buildVAST(base, s.cfg)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("X-Stitchpoint-Ad-Decision", "fixture")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, xml.Header); err != nil {
		slog.Error("adfixture: writing VAST response", "err", err)
		return
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		slog.Error("adfixture: encoding VAST response", "err", err)
	}
}

func (s *Server) handleCreative(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, s.cfg.CreativePath)
}

// handleOMIDScript serves a stub so the OMID <JavaScriptResource> URL in
// the VAST response resolves instead of 404ing; not a real integration.
func (s *Server) handleOMIDScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	if _, err := fmt.Fprintln(w, "// stitchpoint adfixture: stub OMID verification script.\n// Not a real Open Measurement SDK integration — see internal/adfixture's package doc."); err != nil {
		slog.Error("adfixture: writing OMID script stub", "err", err)
	}
}

// handleTrack is a generic tracking-pixel sink for VAST tracking URLs.
func (s *Server) handleTrack(w http.ResponseWriter, r *http.Request) {
	// r.URL is attacker-controlled; strip control chars before logging.
	slog.Info("adfixture: tracking event",
		"event", sanitizeForLog(r.URL.Query().Get("event")),
		"query", sanitizeForLog(r.URL.RawQuery))
	w.WriteHeader(http.StatusNoContent)
}

// sanitizeForLog strips control characters that could forge a log line.
func sanitizeForLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// baseURLFor returns the scheme+host to use for URLs in this response,
// deriving it from the request if Config.BaseURL isn't set.
func (s *Server) baseURLFor(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// creativeFilename returns CreativePath's base name, used only for the
// filename attribute mirrored into the VAST response for readability.
func creativeFilename(cfg Config) string {
	return filepath.Base(cfg.CreativePath)
}
