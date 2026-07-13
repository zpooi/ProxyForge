package proxy

import (
	"context"
	"net"
	"testing"
	"time"
)

// fakeEgress 是一个只用于测试的最小 Egress，Tag 唯一即可。
type fakeEgress struct{ tag string }

func (f *fakeEgress) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (f *fakeEgress) Tag() string                   { return f.tag }
func (f *fakeEgress) Kind() string                  { return "agent" }
func (f *fakeEgress) NoteDial(time.Duration, error) {}
func (f *fakeEgress) AddTx(int64)                   {}
func (f *fakeEgress) AddRx(int64)                   {}

// fakeResolver 返回一组固定的在线 agent 出口。
type fakeResolver struct{ egresses []Egress }

func (r *fakeResolver) ResolveEgress(string) Egress { return nil }
func (r *fakeResolver) OnlineEgresses() []Egress    { return r.egresses }

func newRotateManager(tags ...string) *Manager {
	m := NewManager(nil)
	m.password = "secret"
	egs := make([]Egress, 0, len(tags))
	for _, t := range tags {
		egs = append(egs, &fakeEgress{tag: t})
	}
	m.agentResolver = &fakeResolver{egresses: egs}
	return m
}

// 不同客户端连续请求应 round-robin 落到不同节点（错开、不挤在一处）。
func TestRotateRoundRobinAcrossClients(t *testing.T) {
	m := newRotateManager("node-a", "node-b", "node-c")

	got := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		// 用不同 clientIP，避免粘滞，纯看游标轮转。
		out := m.resolve(RotateUsername, "secret", "client-"+string(rune('0'+i)))
		if len(out) == 0 {
			t.Fatalf("resolve returned no egress")
		}
		got = append(got, out[0].Tag())
	}

	// 6 次请求 3 个节点，应恰好每个节点被选为主出口两次。
	counts := map[string]int{}
	for _, g := range got {
		counts[g]++
	}
	for _, tag := range []string{"node-a", "node-b", "node-c"} {
		if counts[tag] != 2 {
			t.Errorf("node %s picked %d times, want 2 (round-robin uneven): %v", tag, counts[tag], got)
		}
	}
}

// 同一客户端在粘滞窗口内应固定用同一节点（不乱飘）。
func TestRotateStickyPerClient(t *testing.T) {
	m := newRotateManager("node-a", "node-b", "node-c")

	first := m.resolve(RotateUsername, "secret", "1.2.3.4")
	if len(first) == 0 {
		t.Fatal("no egress")
	}
	want := first[0].Tag()

	for i := 0; i < 5; i++ {
		out := m.resolve(RotateUsername, "secret", "1.2.3.4")
		if out[0].Tag() != want {
			t.Errorf("sticky broken: got %s, want %s", out[0].Tag(), want)
		}
	}
}

// 粘滞窗口过期后，同一客户端应轮到下一个节点。
func TestRotateStickyExpires(t *testing.T) {
	m := newRotateManager("node-a", "node-b", "node-c")

	first := m.resolve(RotateUsername, "secret", "1.2.3.4")
	firstTag := first[0].Tag()

	// 手动把该客户端的粘滞分配时间往前拨，超出窗口。
	m.mu.Lock()
	a := m.rotateSticky["1.2.3.4"]
	a.assigned = time.Now().Add(-rotateStickyWindow - time.Minute)
	m.rotateSticky["1.2.3.4"] = a
	m.mu.Unlock()

	second := m.resolve(RotateUsername, "secret", "1.2.3.4")
	if second[0].Tag() == firstTag {
		t.Errorf("after window expiry expected a different node, still %s", firstTag)
	}
}

// 选中节点之外的其余节点应作为故障转移兜底跟在候选链后面。
func TestRotateIncludesFailoverChain(t *testing.T) {
	m := newRotateManager("node-a", "node-b", "node-c")

	out := m.resolve(RotateUsername, "secret", "1.2.3.4")
	if len(out) != 3 {
		t.Fatalf("expected 3 egresses (1 primary + 2 fallback), got %d", len(out))
	}
	// 候选链里不应有重复节点。
	seen := map[string]bool{}
	for _, eg := range out {
		if seen[eg.Tag()] {
			t.Errorf("duplicate egress %s in failover chain", eg.Tag())
		}
		seen[eg.Tag()] = true
	}
}

// 密码不匹配时 auto 应拒绝。
func TestRotateRejectsWrongPassword(t *testing.T) {
	m := newRotateManager("node-a")
	m.password = "secret"
	if out := m.resolve(RotateUsername, "wrong", "1.2.3.4"); out != nil {
		t.Errorf("expected nil for wrong password, got %v", out)
	}
	if out := m.resolve(RotateUsername, "secret", "1.2.3.4"); len(out) == 0 {
		t.Error("expected egress for correct password")
	}
}

// 无任何在线节点时 auto 返回空，交给客户端层切换。
func TestRotateNoNodes(t *testing.T) {
	m := NewManager(nil)
	m.password = "secret"
	m.agentResolver = &fakeResolver{}
	if out := m.resolve(RotateUsername, "secret", "1.2.3.4"); out != nil {
		t.Errorf("expected nil with no nodes, got %v", out)
	}
}
