package server

import (
	"net/http"
	"sync"
	"time"
)

const (
	authRequestsPerSecond = 3.0
	authRequestBurst      = 10.0

	globalRequestsPerSecond = 300.0
	globalRequestBurst      = 600.0
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) take(now time.Time, rate, burst float64) bool {
	if b.last.IsZero() {
		b.tokens = burst
		b.last = now
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * rate
		if b.tokens > burst {
			b.tokens = burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// requestGuard only protects the process as a whole from an extreme request
// flood. It deliberately keeps no client-IP state: users behind a shared NAT
// must never be banned because another device requested a bad path or produced
// a burst of traffic.
type requestGuard struct {
	mu     sync.Mutex
	global tokenBucket
	now    func() time.Time
}

func newRequestGuard() *requestGuard {
	return &requestGuard{now: time.Now}
}

func (g *requestGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		allowed := g.global.take(g.now(), globalRequestsPerSecond, globalRequestBurst)
		g.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "1")
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authRequestGuard limits only the aggregate login/setup POST rate. Failed
// attempts are still rejected by authentication, but they no longer create an
// IP-based timeout that can lock every device behind the same router out.
type authRequestGuard struct {
	mu     sync.Mutex
	global tokenBucket
	now    func() time.Time
}

func newAuthRequestGuard() *authRequestGuard {
	return &authRequestGuard{now: time.Now}
}

func (g *authRequestGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/login" && r.URL.Path != "/setup") {
			next.ServeHTTP(w, r)
			return
		}

		g.mu.Lock()
		allowed := g.global.take(g.now(), authRequestsPerSecond, authRequestBurst)
		g.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "authentication rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func limitRequestBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
