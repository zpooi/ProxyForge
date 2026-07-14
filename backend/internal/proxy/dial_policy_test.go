package proxy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type policyEgress struct {
	tag   string
	err   error
	wait  bool
	calls int
}

func (e *policyEgress) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	e.calls++
	if e.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, e.err
}
func (e *policyEgress) Tag() string                   { return e.tag }
func (e *policyEgress) Kind() string                  { return "test" }
func (e *policyEgress) NoteDial(time.Duration, error) {}
func (e *policyEgress) AddTx(int64)                   {}
func (e *policyEgress) AddRx(int64)                   {}

func TestDialViaLimitsFailoverAttempts(t *testing.T) {
	egresses := make([]Egress, 0, 8)
	items := make([]*policyEgress, 8)
	for i := range items {
		items[i] = &policyEgress{tag: "egress", err: errors.New("temporary transport failure")}
		egresses = append(egresses, items[i])
	}

	_, _, err := (&mixedServer{}).dialViaWithPolicy(egresses, "example.com:443", 3, time.Second, time.Second)
	if err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	for i, item := range items {
		want := 0
		if i < 3 {
			want = 1
		}
		if item.calls != want {
			t.Fatalf("egress %d calls = %d, want %d", i, item.calls, want)
		}
	}
}

func TestDialViaStopsOnPermanentTargetError(t *testing.T) {
	missing := &policyEgress{tag: "missing", err: &net.DNSError{
		Err:        "no such host",
		Name:       "ipv6-only.example",
		IsNotFound: true,
	}}
	fallback := &policyEgress{tag: "fallback", err: errors.New("should not be reached")}

	_, _, err := (&mixedServer{}).dialViaWithPolicy(
		[]Egress{missing, fallback}, "ipv6-only.example:443", 3, time.Second, time.Second,
	)
	if err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if missing.calls != 1 || fallback.calls != 0 {
		t.Fatalf("calls = missing:%d fallback:%d, want 1/0", missing.calls, fallback.calls)
	}
}

func TestDialViaHonorsOverallTimeout(t *testing.T) {
	items := []*policyEgress{
		{tag: "one", wait: true},
		{tag: "two", wait: true},
		{tag: "three", wait: true},
	}
	egresses := make([]Egress, len(items))
	for i := range items {
		egresses[i] = items[i]
	}

	started := time.Now()
	_, _, err := (&mixedServer{}).dialViaWithPolicy(egresses, "example.com:443", 3, 80*time.Millisecond, 120*time.Millisecond)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial error = %v, want context deadline exceeded", err)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("overall timeout took %s, want <= 400ms", elapsed)
	}
	calls := 0
	for _, item := range items {
		calls += item.calls
	}
	if calls < 1 || calls > 2 {
		t.Fatalf("blocking egress calls = %d, want 1 or 2 within overall budget", calls)
	}
}

func TestPermanentTargetErrorDoesNotMarkTunnelUnhealthy(t *testing.T) {
	tunnel := &Tunnel{}
	err := &net.DNSError{Err: "no such host", Name: "ipv6-only.example", IsNotFound: true}
	for range tunnelRebuildAfterFailures + 2 {
		tunnel.noteDial(time.Millisecond, err)
	}
	if got := tunnel.dialFailures.Load(); got != 0 {
		t.Fatalf("dial failures = %d, want 0", got)
	}
}

func TestHealthCheckConfirmsTunnelBeforeRebuild(t *testing.T) {
	manager := NewManager(nil)
	tunnel := &Tunnel{cfg: Config{Tag: "warp-test"}}
	tunnel.dialFailures.Store(tunnelRebuildAfterFailures)
	manager.tunnels[tunnel.cfg.Tag] = tunnel
	probeCalls := 0
	manager.healthProbe = func(context.Context, *Tunnel) error {
		probeCalls++
		return nil
	}

	if rebuilt := manager.HealthCheck(); rebuilt != 0 {
		t.Fatalf("rebuilt = %d, want 0", rebuilt)
	}
	if probeCalls != 1 {
		t.Fatalf("health probe calls = %d, want 1", probeCalls)
	}
	if got := tunnel.dialFailures.Load(); got != 0 {
		t.Fatalf("dial failures after successful health probe = %d, want 0", got)
	}
}
