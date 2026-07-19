package handlers

import (
	"net/url"
	"strings"
	"testing"

	"github.com/zpooi/ProxyForge/backend/internal/proxy"
)

func TestShareURIMatchesClashTrojanParams(t *testing.T) {
	p := &proxyExport{
		Name:         "pf-001",
		Username:     "pf-001",
		Password:     "legacy-secret",
		TrojanHost:   "pf.example.com",
		TrojanPort:   443,
		TrojanWSPath: "/api/v1/connect/deadbeef",
	}
	raw := p.ShareURI()
	if !strings.HasPrefix(raw, "trojan://") {
		t.Fatalf("ShareURI = %q, want trojan scheme", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse ShareURI: %v", err)
	}
	if u.User == nil || u.User.Username() != proxy.TrojanCredential("pf-001", "legacy-secret") {
		t.Fatalf("password = %q, want derived Trojan credential", u.User)
	}
	if u.Hostname() != "pf.example.com" || u.Port() != "443" {
		t.Fatalf("host = %s:%s, want pf.example.com:443", u.Hostname(), u.Port())
	}
	q := u.Query()
	for key, want := range map[string]string{
		"type":          "ws",
		"security":      "tls",
		"path":          "/api/v1/connect/deadbeef",
		"host":          "pf.example.com",
		"sni":           "pf.example.com",
		"fp":            "chrome",
		"alpn":          "http/1.1",
		"allowInsecure": "0",
		"udp":           "0",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
	if u.Fragment != "pf-001" {
		t.Errorf("fragment = %q, want node name", u.Fragment)
	}
	// Legacy HTTP password must not appear in the share link.
	if strings.Contains(raw, "legacy-secret") {
		t.Errorf("share link leaked legacy password: %s", raw)
	}
}

func TestWriteShareLinksOnePerLine(t *testing.T) {
	var b strings.Builder
	writeShareLinks(&b, []*proxyExport{
		{Name: "a", Username: "a", Password: "p", TrojanHost: "h.example", TrojanPort: 443, TrojanWSPath: "/p"},
		{Name: "b", Username: "b", Password: "p", TrojanHost: "h.example", TrojanPort: 443, TrojanWSPath: "/p"},
		{Name: "empty", Username: "", Password: "p", TrojanHost: "h.example", TrojanPort: 443, TrojanWSPath: "/p"},
	})
	out := b.String()
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "trojan://") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 trojan links, got %d:\n%s", count, out)
	}
	items := shareLinkItems([]*proxyExport{
		{Name: "a", Username: "a", Password: "p", TrojanHost: "h.example", TrojanPort: 443, TrojanWSPath: "/p"},
	})
	if len(items) != 1 || items[0]["name"] != "a" || !strings.HasPrefix(items[0]["uri"], "trojan://") {
		t.Fatalf("shareLinkItems = %#v", items)
	}
}
