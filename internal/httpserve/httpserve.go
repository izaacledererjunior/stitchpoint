// Package httpserve runs an *http.Server with graceful shutdown on
// SIGINT/SIGTERM, shared by every stitchpoint HTTP entry point so none of
// them drop in-flight requests on a routine restart/deploy.
package httpserve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// shutdownTimeout bounds how long Serve waits for in-flight requests to
// finish before forcing the listener closed.
const shutdownTimeout = 10 * time.Second

// Serve runs an HTTP server on addr with handler until SIGINT or SIGTERM,
// then shuts it down gracefully before returning.
func Serve(addr string, handler http.Handler, readHeaderTimeout time.Duration) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	return serve(ctx, srv, ln, func() { stop() })
}

// serve is Serve's logic, factored out to be testable against a
// directly-cancellable context and listener. restoreSignals runs before
// the drain starts, so a second signal kills the process immediately.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, restoreSignals func()) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		restoreSignals()
	}

	fmt.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("httpserve: graceful shutdown: %w", err)
	}
	return nil
}
