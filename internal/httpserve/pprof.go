package httpserve

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	// Registers /debug/pprof/* onto http.DefaultServeMux — see ServePprof.
	_ "net/http/pprof"
)

// ServePprof starts a pprof debug server on addr in the background if
// addr is non-empty. Deliberately a separate listener from the main
// application server, never merged into its mux — pprof shouldn't be
// reachable on whatever port faces the public internet.
func ServePprof(addr string) {
	if addr == "" {
		return
	}
	go func() {
		slog.Info("pprof: listening", "addr", addr)
		srv := &http.Server{Addr: addr, ReadHeaderTimeout: 10 * time.Second}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("pprof: server failed", "err", err)
		}
	}()
}
