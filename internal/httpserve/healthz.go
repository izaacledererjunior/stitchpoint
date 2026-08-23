package httpserve

import "net/http"

// Healthz is a trivial liveness/readiness handler — 200 "ok", no
// dependency checks (every service here is ready the instant New returns).
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
