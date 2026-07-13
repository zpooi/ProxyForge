package server

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authFailureWindow     = 10 * time.Minute
	authBlockDuration     = 15 * time.Minute
	authMaxFailures       = 8
	authRequestsPerSecond = 3.0
	authRequestBurst      = 10.0

	clientRequestsPerSecond = 10.0
	clientRequestBurst      = 60.0
	globalRequestsPerSecond = 300.0
	globalRequestBurst      = 600.0
	abuseStrikeWindow       = 10 * time.Minute
	abuseBanDuration        = 30 * time.Minute
	abuseMaxStrikes         = 20
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

type clientRequestState struct {
	bucket      tokenBucket
	strikeStart time.Time
	lastSeen    time.Time
	strikes     int
	bannedUntil time.Time
}

type adaptiveRequestGuard struct {
	mu      sync.Mutex
	clients map[string]clientRequestState
	global  tokenBucket
	now     func() time.Time
}

func newAdaptiveRequestGuard() *adaptiveRequestGuard {
	return &adaptiveRequestGuard{clients: make(map[string]clientRequestState), now: time.Now}
}

func (g *adaptiveRequestGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := securityClientIP(r)
		now := g.now()
		if retry, status, allowed := g.allow(ip, now); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds()))))
			http.Error(w, http.StatusText(status), status)
			return
		}

		if obviouslySuspiciousPath(r.URL.Path) {
			g.addStrikes(ip, now, 5)
			http.NotFound(w, r)
			return
		}

		// WebSocket agent handlers are long-lived and must keep the original
		// ResponseWriter interfaces. They are still subject to the token bucket.
		if r.URL.Path == "/agent/link" || strings.HasPrefix(r.URL.Path, "/api/v1/connect/") {
			next.ServeHTTP(w, r)
			return
		}

		rec := &authResponseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		switch rec.status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
			g.addStrikes(ip, now, 1)
		}
	})
}

func (g *adaptiveRequestGuard) allow(ip string, now time.Time) (time.Duration, int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.clients[ip]
	state.lastSeen = now
	if state.bannedUntil.After(now) {
		g.clients[ip] = state
		return state.bannedUntil.Sub(now), http.StatusForbidden, false
	}
	if !state.bannedUntil.IsZero() {
		state = clientRequestState{lastSeen: now}
	}
	if !g.global.take(now, globalRequestsPerSecond, globalRequestBurst) {
		g.clients[ip] = state
		g.pruneLocked(now)
		return time.Second, http.StatusServiceUnavailable, false
	}
	if !state.bucket.take(now, clientRequestsPerSecond, clientRequestBurst) {
		wasBanned := state.bannedUntil.After(now)
		state = addClientStrikes(state, now, 2)
		g.clients[ip] = state
		if !wasBanned && state.bannedUntil.After(now) {
			log.Printf("[security] temporarily banned rate-abusing client %s for %s", ip, abuseBanDuration)
		}
		g.pruneLocked(now)
		return time.Second, http.StatusTooManyRequests, false
	}
	g.clients[ip] = state
	g.pruneLocked(now)
	return 0, 0, true
}

func (g *adaptiveRequestGuard) addStrikes(ip string, now time.Time, count int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.clients[ip]
	state.lastSeen = now
	wasBanned := state.bannedUntil.After(now)
	state = addClientStrikes(state, now, count)
	g.clients[ip] = state
	if !wasBanned && state.bannedUntil.After(now) {
		log.Printf("[security] temporarily banned suspicious client %s for %s", ip, abuseBanDuration)
	}
}

func addClientStrikes(state clientRequestState, now time.Time, count int) clientRequestState {
	if state.strikeStart.IsZero() || now.Sub(state.strikeStart) >= abuseStrikeWindow {
		state.strikeStart = now
		state.strikes = 0
	}
	state.strikes += count
	if state.strikes >= abuseMaxStrikes {
		state.bannedUntil = now.Add(abuseBanDuration)
	}
	return state
}

func (g *adaptiveRequestGuard) pruneLocked(now time.Time) {
	if len(g.clients) <= 4096 {
		return
	}
	for ip, state := range g.clients {
		if now.Sub(state.lastSeen) > time.Hour && !state.bannedUntil.After(now) {
			delete(g.clients, ip)
		}
	}
	if len(g.clients) > 8192 {
		for ip, state := range g.clients {
			if !state.bannedUntil.After(now) {
				delete(g.clients, ip)
				if len(g.clients) <= 4096 {
					break
				}
			}
		}
	}
}

func obviouslySuspiciousPath(path string) bool {
	p := strings.ToLower(path)
	if p == "/.well-known" || strings.HasPrefix(p, "/.well-known/") {
		return false
	}
	return strings.Contains(p, "..") ||
		strings.HasPrefix(p, "/.") ||
		strings.HasPrefix(p, "/wp-") ||
		strings.HasPrefix(p, "/phpmyadmin") ||
		strings.HasPrefix(p, "/cgi-bin") ||
		strings.HasPrefix(p, "/actuator") ||
		strings.HasSuffix(p, ".php")
}

type authAttempt struct {
	windowStart  time.Time
	lastSeen     time.Time
	failures     int
	blockedUntil time.Time
}

type authAttemptGuard struct {
	mu       sync.Mutex
	attempts map[string]authAttempt
	global   tokenBucket
	now      func() time.Time
}

func newAuthAttemptGuard() *authAttemptGuard {
	return &authAttemptGuard{attempts: make(map[string]authAttempt), now: time.Now}
}

func (g *authAttemptGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/login" && r.URL.Path != "/setup") {
			next.ServeHTTP(w, r)
			return
		}

		ip := securityClientIP(r)
		now := g.now()
		if !g.allowGlobal(now) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "authentication rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		if retry, blocked := g.retryAfter(ip, now); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds()))))
			http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
			return
		}

		rec := &authResponseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		location := rec.Header().Get("Location")
		failed := rec.status >= http.StatusBadRequest || strings.Contains(location, "?error=")
		g.record(ip, now, failed)
	})
}

func (g *authAttemptGuard) allowGlobal(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.global.take(now, authRequestsPerSecond, authRequestBurst)
}

func (g *authAttemptGuard) retryAfter(ip string, now time.Time) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	a := g.attempts[ip]
	if a.blockedUntil.After(now) {
		return a.blockedUntil.Sub(now), true
	}
	if !a.blockedUntil.IsZero() {
		delete(g.attempts, ip)
	}
	return 0, false
}

func (g *authAttemptGuard) record(ip string, now time.Time, failed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !failed {
		delete(g.attempts, ip)
		return
	}
	a := g.attempts[ip]
	if a.windowStart.IsZero() || now.Sub(a.windowStart) >= authFailureWindow {
		a.windowStart = now
		a.failures = 0
	}
	a.failures++
	a.lastSeen = now
	if a.failures >= authMaxFailures {
		a.blockedUntil = now.Add(authBlockDuration)
	}
	g.attempts[ip] = a
	if len(g.attempts) > 1024 {
		for key, item := range g.attempts {
			if now.Sub(item.lastSeen) > time.Hour && !item.blockedUntil.After(now) {
				delete(g.attempts, key)
			}
		}
		if len(g.attempts) > 2048 {
			for key := range g.attempts {
				delete(g.attempts, key)
				if len(g.attempts) <= 1024 {
					break
				}
			}
		}
	}
}

type authResponseRecorder struct {
	http.ResponseWriter
	status int
}

func (w *authResponseRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *authResponseRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func securityClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.Trim(r.RemoteAddr, "[]")
	}
	remote := net.ParseIP(host)
	// Trust forwarding headers only from a loopback reverse proxy. Direct
	// internet clients cannot spoof another address to bypass the limiter.
	if remote != nil && remote.IsLoopback() {
		for _, candidate := range []string{
			r.Header.Get("X-Real-IP"),
			strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0],
		} {
			candidate = strings.TrimSpace(candidate)
			if ip := net.ParseIP(candidate); ip != nil {
				return ip.String()
			}
		}
	}
	if remote != nil {
		return remote.String()
	}
	return "unknown"
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
