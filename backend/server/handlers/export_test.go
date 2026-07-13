package handlers

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClashScalarQuotesSpecials(t *testing.T) {
	cases := map[string]string{
		"proxy1":     `"proxy1"`,
		"日本 节点":      `"日本 节点"`,
		`a"b`:        `"a\"b"`,
		`back\slash`: `"back\\slash"`,
	}
	for in, want := range cases {
		if got := clashScalar(in); got != want {
			t.Errorf("clashScalar(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNodeNameFallsBackToUsername(t *testing.T) {
	if got := (&proxyExport{Name: "", Username: "proxy1"}).NodeName(); got != "proxy1" {
		t.Errorf("empty Name should fall back to Username, got %q", got)
	}
	if got := (&proxyExport{Name: "日本 节点", Username: "node-abc"}).NodeName(); got != "日本 节点" {
		t.Errorf("NodeName should prefer Name, got %q", got)
	}
}

// writeClash 必须为 agent 节点用地区名作 Clash 节点名，但鉴权用户名仍是 node-<id>，
// 且节点名进入自动选择 / 故障转移 / 手选三个组。
func TestWriteClashAgentNode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeClash(rec, []*proxyExport{
		{Name: "proxy1", Username: "proxy1", Password: "pw", ProxyHost: "1.2.3.4", ProxyPort: 7843},
		{Name: "日本 节点", Username: "node-abc123", Password: "pw", ProxyHost: "1.2.3.4", ProxyPort: 7843, IsAgent: true},
	})
	out := rec.Body.String()

	// agent 节点：显示名是地区，username 是 node-<id>。
	if !strings.Contains(out, `- name: "日本 节点"`) {
		t.Errorf("agent node should use region as name:\n%s", out)
	}
	if !strings.Contains(out, "username: node-abc123") {
		t.Errorf("agent node should authenticate as node-<id>:\n%s", out)
	}
	// 三个组都应引用节点显示名（带引号）。
	if n := strings.Count(out, `- "日本 节点"`); n != 3 {
		t.Errorf("agent node should appear in 3 groups (url-test/fallback/select), got %d:\n%s", n, out)
	}
	// 自动选择与故障转移组都存在。
	for _, want := range []string{"type: url-test", "type: fallback", "MATCH,PROXYFORGE"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

// 没有任何节点时必须输出合法的 DIRECT 兜底组，否则整份订阅加载失败。
func TestWriteClashEmptyFallsBackToDirect(t *testing.T) {
	rec := httptest.NewRecorder()
	writeClash(rec, nil)
	out := rec.Body.String()
	if !strings.Contains(out, "type: select") || !strings.Contains(out, "- DIRECT") {
		t.Errorf("empty export should emit a DIRECT select group:\n%s", out)
	}
}

func TestResolveProxyDialHostUsesIPv4AndFallsBackToDomain(t *testing.T) {
	lookup := func(_ context.Context, network, host string) ([]net.IP, error) {
		if network != "ip4" || host != "proxy.example.com" {
			t.Fatalf("unexpected lookup %q %q", network, host)
		}
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}
	if got := resolveProxyDialHostWith(context.Background(), "proxy.example.com", lookup); got != "203.0.113.9" {
		t.Fatalf("resolved Clash server = %q, want IPv4", got)
	}

	failing := func(context.Context, string, string) ([]net.IP, error) {
		return nil, errors.New("dns unavailable")
	}
	if got := resolveProxyDialHostWith(context.Background(), "proxy.example.com", failing); got != "proxy.example.com" {
		t.Fatalf("failed lookup should keep domain, got %q", got)
	}
}

func TestWriteClashUsesResolvedServerAndKeepsTLSSNI(t *testing.T) {
	rec := httptest.NewRecorder()
	writeClash(rec, []*proxyExport{{
		Name:          "pf-001",
		Username:      "pf-001",
		Password:      "pw",
		ProxyHost:     "proxy.example.com",
		ProxyDialHost: "203.0.113.9",
		ProxyPort:     7843,
		TLS:           true,
	}})
	out := rec.Body.String()
	for _, want := range []string{
		"server: 203.0.113.9",
		"sni: proxy.example.com",
		"skip-cert-verify: true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("resolved Clash export missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dns:") {
		t.Errorf("IP-based Clash export should not depend on a DNS section:\n%s", out)
	}
}

func TestWriteClashDomainFallbackUsesMihomoProxyNameservers(t *testing.T) {
	rec := httptest.NewRecorder()
	writeClash(rec, []*proxyExport{{
		Name:      "pf-001",
		Username:  "pf-001",
		Password:  "pw",
		ProxyHost: "proxy.example.com",
		ProxyPort: 7843,
	}})
	out := rec.Body.String()
	for _, want := range []string{
		"proxy-server-nameserver:",
		`"proxy.example.com":`,
		"      - 223.5.5.5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("domain fallback missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"223.5.5.5,119.29.29.29`) {
		t.Errorf("nameserver-policy must be a YAML list, not a comma-joined resolver:\n%s", out)
	}
}
