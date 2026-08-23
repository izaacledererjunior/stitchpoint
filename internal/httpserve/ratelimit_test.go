package httpserve

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_BlocksAfterMax(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	t.Cleanup(rl.Close)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Middleware(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:12345"

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d", i, rec.Code, http.StatusOK)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request: status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

func TestRateLimiter_TracksIPsIndependently(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	t.Cleanup(rl.Close)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Middleware(next)

	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "203.0.113.1:1"
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "203.0.113.2:1"

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("ip1 first request: status = %d, want %d", rec1.Code, http.StatusOK)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("ip2 first request: status = %d, want %d (should not share ip1's budget)", rec2.Code, http.StatusOK)
	}
}

func TestRateLimiter_ResetsAfterWindow(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)
	t.Cleanup(rl.Close)

	if !rl.allow("203.0.113.1") {
		t.Fatal("first request should be allowed")
	}
	if rl.allow("203.0.113.1") {
		t.Fatal("second request within the window should be blocked")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.allow("203.0.113.1") {
		t.Error("request after the window elapsed should be allowed again")
	}
}
