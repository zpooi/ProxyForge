package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestGuardGloballyThrottlesDistributedFlood(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	guard := newRequestGuard()
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

func TestRequestGuardDoesNotBanIPForErrors(t *testing.T) {
	guard := newRequestGuard()
	h := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/.env", "/wp-admin", "/cgi-bin/test", "/index.php"} {
		if rec := serveFromIP(h, path, "203.0.113.20"); rec.Code != http.StatusNotFound {
			t.Fatalf("path %s status = %d", path, rec.Code)
		}
	}
	if rec := serveFromIP(h, "/", "203.0.113.20"); rec.Code != http.StatusNoContent {
		t.Fatalf("client was blocked after errors: status = %d", rec.Code)
	}
}

func TestAuthRequestGuardDoesNotBlockIPAfterFailures(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	guard := newAuthRequestGuard()
	guard.now = func() time.Time { return now }
	h := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, httptest.NewRequest(http.MethodGet, "/", nil), "/login?error=bad", http.StatusFound)
	}))

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "198.51.100.30:5000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("failure %d status = %d", i+1, rec.Code)
		}
		now = now.Add(time.Second)
	}
}

func serveFromIP(h http.Handler, path, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":5000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
