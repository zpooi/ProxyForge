package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdaptiveRequestGuardRateLimitsOneIP(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	guard := newAdaptiveRequestGuard()
	guard.now = func() time.Time { return now }
	h := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < int(clientRequestBurst); i++ {
		rec := serveFromIP(h, "/", "198.51.100.10")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d", i+1, rec.Code)
		}
	}
	if rec := serveFromIP(h, "/", "198.51.100.10"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit status = %d, want 429", rec.Code)
	}
	now = now.Add(time.Second)
	if rec := serveFromIP(h, "/", "198.51.100.10"); rec.Code != http.StatusNoContent {
		t.Fatalf("refilled status = %d", rec.Code)
	}
}

func TestAdaptiveRequestGuardGloballyThrottlesDistributedFlood(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	guard := newAdaptiveRequestGuard()
	guard.now = func() time.Time { return now }
	h := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < int(globalRequestBurst); i++ {
		ip := fmt.Sprintf("198.18.%d.%d", i/256, i%256)
		if rec := serveFromIP(h, "/", ip); rec.Code != http.StatusNoContent {
			t.Fatalf("distributed request %d status = %d", i+1, rec.Code)
		}
	}
	if rec := serveFromIP(h, "/", "198.19.0.1"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("global over-limit status = %d, want 503", rec.Code)
	}
}

func TestAdaptiveRequestGuardBansScanner(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	guard := newAdaptiveRequestGuard()
	guard.now = func() time.Time { return now }
	h := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/.env", "/wp-admin", "/cgi-bin/test", "/index.php"} {
		if rec := serveFromIP(h, path, "203.0.113.20"); rec.Code != http.StatusNotFound {
			t.Fatalf("scanner path %s status = %d", path, rec.Code)
		}
	}
	if rec := serveFromIP(h, "/", "203.0.113.20"); rec.Code != http.StatusForbidden {
		t.Fatalf("banned scanner status = %d, want 403", rec.Code)
	}
	if rec := serveFromIP(h, "/", "203.0.113.21"); rec.Code != http.StatusNoContent {
		t.Fatalf("different IP status = %d", rec.Code)
	}
}

func TestAuthAttemptGuardBlocksRepeatedFailures(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	guard := newAuthAttemptGuard()
	guard.now = func() time.Time { return now }
	h := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, httptest.NewRequest(http.MethodGet, "/", nil), "/login?error=bad", http.StatusFound)
	}))

	for i := 0; i < authMaxFailures; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "198.51.100.30:5000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("failure %d status = %d", i+1, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "198.51.100.30:5000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked login status = %d, want 429", rec.Code)
	}
}

func TestSecurityClientIPTrustsOnlyLoopbackProxy(t *testing.T) {
	loopback := httptest.NewRequest(http.MethodGet, "/", nil)
	loopback.RemoteAddr = "127.0.0.1:5000"
	loopback.Header.Set("X-Real-IP", "198.51.100.40")
	if got := securityClientIP(loopback); got != "198.51.100.40" {
		t.Fatalf("loopback proxy IP = %q", got)
	}

	direct := httptest.NewRequest(http.MethodGet, "/", nil)
	direct.RemoteAddr = "203.0.113.40:5000"
	direct.Header.Set("X-Real-IP", "198.51.100.40")
	if got := securityClientIP(direct); got != "203.0.113.40" {
		t.Fatalf("spoofed direct IP = %q", got)
	}
}

func serveFromIP(h http.Handler, path, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":5000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
