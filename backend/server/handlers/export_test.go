package handlers

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zpooi/ProxyForge/backend/internal/proxy"
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
		{Name: "proxy1", Username: "proxy1", Password: "pw", ProxyHost: "1.2.3.4", ProxyPort: 7843, SupportsUDP: true},
		{Name: "日本 节点", Username: "node-abc123", Password: "pw", ProxyHost: "1.2.3.4", ProxyPort: 7843, IsAgent: true},
	})
	out := rec.Body.String()

	// agent 节点显示名仍是地区，但 Trojan 只下发协议专用派生密码，
	// 不再暴露或复用 HTTP/SOCKS5 的 username/password 字段。
	if !strings.Contains(out, `- name: "日本 节点"`) {
		t.Errorf("agent node should use region as name:\n%s", out)
	}
	wantCredential := `password: ` + clashScalar(proxy.TrojanCredential("node-abc123", "pw"))
	if !strings.Contains(out, wantCredential) {
		t.Errorf("agent node should use its derived Trojan credential:\n%s", out)
	}
	if strings.Contains(out, `username:`) || strings.Contains(out, `password: "pw"`) {
		t.Errorf("Trojan export leaked legacy proxy credentials:\n%s", out)
	}
	for _, want := range []string{"type: trojan", "network: ws", "udp: true", "udp: false", "client-fingerprint: chrome", "alpn:", "- http/1.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Trojan agent export missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "    udp: true\n") != 1 || strings.Count(out, "    udp: false\n") != 1 {
		t.Errorf("UDP capability must be exported per node:\n%s", out)
	}
	// 四个组都应引用节点显示名（带引号）。
	if n := strings.Count(out, `- "日本 节点"`); n != 4 {
		t.Errorf("agent node should appear in 4 groups (stable/url-test/fallback/select), got %d:\n%s", n, out)
	}
	// 会话稳定、自动选择与故障转移组都存在。
	for _, want := range []string{
		"name: 🔒 会话稳定",
		"type: load-balance",
		"strategy: consistent-hashing",
		"type: url-test",
		"type: fallback",
		"url: https://www.gstatic.com/generate_204",
		"DOMAIN,ipv6.msftconnecttest.com,REJECT",
		"DOMAIN,ipv6.msftncsi.com,REJECT",
		"MATCH,PROXYFORGE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}

	// PROXYFORGE 默认选中会话稳定组，而不是某个写死的 pf 编号或会动态换 IP 的 url-test。
	groupAt := strings.LastIndex(out, "  - name: PROXYFORGE\n")
	if groupAt < 0 {
		t.Fatalf("missing PROXYFORGE group:\n%s", out)
	}
	group := out[groupAt:]
	stableAt := strings.Index(group, "      - 🔒 会话稳定\n")
	autoAt := strings.Index(group, "      - ♻️ 自动选择\n")
	if stableAt < 0 || autoAt < 0 || stableAt > autoAt {
		t.Errorf("stable group should be the first PROXYFORGE choice:\n%s", group)
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
		Name:           "pf-001",
		Username:       "pf-001",
		Password:       "pw",
		ProxyHost:      "proxy.example.com",
		ProxyDialHost:  "203.0.113.9",
		ProxyPort:      7843,
		TLS:            true,
		TrojanHost:     "proxy.example.com",
		TrojanDialHost: "203.0.113.9",
		TrojanPort:     443,
		TrojanWSPath:   "/api/v1/connect/token123",
		SupportsUDP:    true,
	}})
	out := rec.Body.String()
	for _, want := range []string{
		"type: trojan",
		`server: "203.0.113.9"`,
		`sni: "proxy.example.com"`,
		"port: 443",
		"network: ws",
		"udp: true",
		`path: "/api/v1/connect/token123"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("resolved Clash export missing %q:\n%s", want, out)
		}
	}
	for _, want := range []string{
		"dns:",
		"https://dns.alidns.com/dns-query",
		"https://doh.pub/dns-query",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("carrier-safe DNS output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "nameserver-policy:") {
		t.Errorf("IP-based Trojan server should not need a bootstrap domain policy:\n%s", out)
	}
	if strings.Contains(out, "skip-cert-verify") || strings.Contains(out, "type: http") {
		t.Errorf("Trojan export must verify nginx TLS and must not downgrade to HTTP:\n%s", out)
	}
}

func TestWriteClashDerivesCredentialAndQuotesScalars(t *testing.T) {
	rec := httptest.NewRecorder()
	writeClash(rec, []*proxyExport{{
		Name:         "special",
		Username:     "node:user",
		Password:     "p:# yes",
		ProxyHost:    "proxy.example.com",
		ProxyPort:    7843,
		TLS:          true,
		TrojanHost:   "proxy.example.com",
		TrojanPort:   443,
		TrojanWSPath: "/api/v1/connect/a token",
	}})
	out := rec.Body.String()
	wantCredential := proxy.TrojanCredential("node:user", "p:# yes")
	for _, want := range []string{
		`server: "proxy.example.com"`,
		`password: "` + wantCredential + `"`,
		`sni: "proxy.example.com"`,
		`path: "/api/v1/connect/a token"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing safely quoted scalar %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "node:user") || strings.Contains(out, "p:# yes") {
		t.Errorf("Clash output leaked the legacy username/password:\n%s", out)
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
		"https://dns.alidns.com/dns-query",
		"https://doh.pub/dns-query",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("domain fallback missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"223.5.5.5,119.29.29.29`) {
		t.Errorf("nameserver-policy must be a YAML list, not a comma-joined resolver:\n%s", out)
	}
}
