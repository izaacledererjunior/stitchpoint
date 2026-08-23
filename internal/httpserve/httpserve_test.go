package httpserve

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServe_GracefulShutdownOnContextCancel(t *testing.T) {
	var gotRequest bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gotRequest = true
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	srv := &http.Server{Handler: handler}

	ctx, cancel := context.WithCancel(context.Background())
	restoreCalled := false

	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, srv, ln, func() { restoreCalled = true })
	}()

	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET before shutdown: %v", err)
	}
	_ = resp.Body.Close()
	if !gotRequest {
		t.Fatal("handler was not invoked before shutdown")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve() error = %v, want nil (clean shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return after context cancellation")
	}

	if !restoreCalled {
		t.Error("restoreSignals was not called before draining — a second signal wouldn't force-kill during shutdown")
	}

	// The listener should be closed post-shutdown: a new connection
	// attempt must fail, not hang or succeed.
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		t.Error("listener still accepting connections after shutdown")
	}
}

func TestServe_PropagatesListenError(t *testing.T) {
	// A listener closed before serve() ever gets to use it makes
	// srv.Serve return immediately with a non-ErrServerClosed error,
	// exercising the select's serveErr branch (the server dying on its
	// own) rather than the shutdown-via-context path.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("closing listener early: %v", err)
	}

	srv := &http.Server{Handler: http.NotFoundHandler()}
	if err := serve(context.Background(), srv, ln, func() {}); err == nil {
		t.Fatal("serve() error = nil, want error from Serve on a closed listener")
	}
}
