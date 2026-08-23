package httpserve

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/safego"
)

// RateLimiter is a per-client-IP fixed-window request limiter, meant to
// wrap specific expensive/abuse-prone routes (a job-creation endpoint
// that triggers a full transcode pipeline, an ad-decision endpoint) —
// not a server's entire mux. Wrapping segment/status-polling routes with
// the same limit would throttle a real, legitimate playback session
// (a player fetches many segments in quick succession); this is
// deliberately applied selectively by each caller instead.
type RateLimiter struct {
	mu     sync.Mutex
	counts map[string]*bucket
	max    int
	window time.Duration
	done   chan struct{}
}

type bucket struct {
	count       int
	windowStart time.Time
}

// NewRateLimiter allows up to maxRequests per client IP every window,
// answering 429 to the rest. Starts a background sweep to drop IPs that
// have gone quiet, so the tracking map doesn't grow unbounded under
// sustained traffic from many distinct addresses; call Close to stop it.
func NewRateLimiter(maxRequests int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		counts: make(map[string]*bucket),
		max:    maxRequests,
		window: window,
		done:   make(chan struct{}),
	}
	go rl.sweepLoop()
	return rl
}

// Close stops the background sweep goroutine.
func (rl *RateLimiter) Close() {
	close(rl.done)
}

func (rl *RateLimiter) sweepLoop() {
	ticker := time.NewTicker(rl.window)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.sweep()
		case <-rl.done:
			return
		}
	}
}

func (rl *RateLimiter) sweep() {
	defer safego.Recover("httpserve.ratelimiter.sweep")
	cutoff := time.Now().Add(-rl.window)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for ip, b := range rl.counts {
		if b.windowStart.Before(cutoff) {
			delete(rl.counts, ip)
		}
	}
}

func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b, ok := rl.counts[ip]
	if !ok || now.Sub(b.windowStart) >= rl.window {
		rl.counts[ip] = &bucket{count: 1, windowStart: now}
		return true
	}
	if b.count >= rl.max {
		return false
	}
	b.count++
	return true
}

// Middleware answers 429 Too Many Requests once the calling IP exceeds
// the configured limit, otherwise delegates to next.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			http.Error(w, "rate limit exceeded, try again shortly", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP reads r.RemoteAddr, not any X-Forwarded-For-style header —
// those are trivially spoofable by the client itself unless a specific
// trusted reverse proxy is known to set (and strip incoming copies of)
// one, which this project's deployment doesn't assume.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
