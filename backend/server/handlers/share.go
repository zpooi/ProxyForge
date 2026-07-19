package handlers

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/zpooi/ProxyForge/backend/internal/proxy"
)

// ShareURI builds a single-line Trojan share link for clients that only accept
// ss/vmess/vless/trojan/hysteria2/tuic URIs (for example GRA / sing-box node lists).
// The parameters mirror the Clash Trojan+TLS+WebSocket export so the same nginx
// path and derived password work for both subscription and pasted links.
func (p *proxyExport) ShareURI() string {
	if p == nil {
		return ""
	}
	password := proxy.TrojanCredential(p.Username, p.Password)
	if password == "" {
		return ""
	}
	host := strings.TrimSpace(p.ClashServer())
	if host == "" {
		return ""
	}
	port := p.ClashPort()
	if port <= 0 {
		port = 443
	}
	path := p.ClashWebSocketPath()
	sni := strings.TrimSpace(p.ClashSNI())

	q := url.Values{}
	q.Set("type", "ws")
	q.Set("security", "tls")
	q.Set("path", path)
	if sni != "" {
		q.Set("host", sni)
		q.Set("sni", sni)
		q.Set("peer", sni)
	}
	q.Set("fp", "chrome")
	q.Set("alpn", "http/1.1")
	q.Set("allowInsecure", "0")
	if clashProxyUDPEnabled && p.SupportsUDP {
		q.Set("udp", "1")
	} else {
		q.Set("udp", "0")
	}

	u := url.URL{
		Scheme:   "trojan",
		User:     url.User(password),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		RawQuery: q.Encode(),
		Fragment: p.NodeName(),
	}
	return u.String()
}

func shareLinksText(list []*proxyExport) string {
	var b strings.Builder
	for _, p := range list {
		if uri := p.ShareURI(); uri != "" {
			b.WriteString(uri)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func writeShareLinks(w io.Writer, list []*proxyExport) {
	fmt.Fprint(w, "# ProxyForge - Trojan share links (one per line)\n")
	fmt.Fprint(w, "# Paste into clients that accept trojan:// node URIs (GRA / sing-box / v2rayN).\n")
	fmt.Fprint(w, "# Transport: Trojan + TLS + WebSocket on port 443 (same as Clash subscription).\n\n")
	fmt.Fprint(w, shareLinksText(list))
}

func shareLinkItems(list []*proxyExport) []map[string]string {
	items := make([]map[string]string, 0, len(list))
	for _, p := range list {
		uri := p.ShareURI()
		if uri == "" {
			continue
		}
		items = append(items, map[string]string{
			"name":     p.NodeName(),
			"username": p.Username,
			"uri":      uri,
		})
	}
	return items
}
