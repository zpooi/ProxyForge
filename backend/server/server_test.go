package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseHeadersProtectSensitiveRoutes(t *testing.T) {
	h := responseHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/api/settings/json", "/sub/clash?token=test", "/login"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
			t.Errorf("%s Cache-Control = %q", path, got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s Referrer-Policy = %q", path, got)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s X-Frame-Options = %q", path, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got == "" {
			t.Errorf("%s missing Content-Security-Policy", path)
		}
		if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
			t.Errorf("%s missing Strict-Transport-Security", path)
		}
	}
}

func TestResponseHeadersRevalidateStaticAssets(t *testing.T) {
	h := responseHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/main.js", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("static Cache-Control = %q", got)
	}
}
