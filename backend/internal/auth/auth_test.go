package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionCookieSecureFollowsRequestScheme(t *testing.T) {
	svc := &Service{}
	tests := []struct {
		name   string
		setup  func() *http.Request
		secure bool
	}{
		{name: "plain HTTP", setup: plainCookieRequest, secure: false},
		{name: "direct HTTPS", setup: tlsCookieRequest, secure: true},
		{name: "forwarded HTTPS", setup: forwardedCookieRequest, secure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := tt.setup()
			svc.SetSessionCookie(rec, req, "token")
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("cookies = %d", len(cookies))
			}
			if cookies[0].Secure != tt.secure || !cookies[0].HttpOnly {
				t.Fatalf("cookie Secure=%v HttpOnly=%v", cookies[0].Secure, cookies[0].HttpOnly)
			}
		})
	}
}

func plainCookieRequest() *http.Request {
	return httptest.NewRequest("GET", "http://example.test/", nil)
}

func tlsCookieRequest() *http.Request {
	r := httptest.NewRequest("GET", "https://example.test/", nil)
	r.TLS = &tls.ConnectionState{}
	return r
}

func forwardedCookieRequest() *http.Request {
	r := httptest.NewRequest("GET", "http://example.test/", nil)
	r.Header.Set("X-Forwarded-Proto", "https, http")
	return r
}

func TestRandomTokenIsStrongAndValidatesSize(t *testing.T) {
	a, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 || len(b) != 64 || a == b {
		t.Fatalf("unexpected tokens len=%d/%d equal=%v", len(a), len(b), a == b)
	}
	if strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("token is not lowercase hex: %q", a)
	}
	if _, err := randomToken(0); err == nil {
		t.Fatal("zero token size should fail")
	}
}
