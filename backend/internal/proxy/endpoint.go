package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const defaultWarpEndpointPort = 2408

var (
	knownWarpEndpointHosts = []string{
		"162.159.192.1",
		"2606:4700:d0::a29f:c001",
		"162.159.193.1",
		"162.159.193.7",
		"162.159.193.10",
		"162.159.193.43",
		"162.159.197.1",
		"162.159.197.10",
		"188.114.96.1",
		"188.114.97.1",
	}
	knownWarpEndpointPorts = []int{2408, 443, 500, 1701, 4500, 4443, 8095, 8443}
)

type endpointCandidate struct {
	addr   netip.AddrPort
	source string
}

func (c endpointCandidate) String() string {
	return c.addr.String()
}

// resolveEndpoint 把 WARP 端点主机解析成 IP:port 形式。
// wireguard-go 的 UAPI endpoint 需要具体 IP（不接受主机名），
// 且这次解析走系统 DNS（隧道外），不能走隧道内 netstack。
func resolveEndpoint(host string, port int) (string, error) {
	candidates, err := endpointCandidates(host, port)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("resolve endpoint %q: no addresses", host)
	}
	return candidates[0].String(), nil
}

func endpointCandidates(host string, port int) ([]endpointCandidate, error) {
	host, port = normalizeEndpoint(host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var resolved []netip.Addr
	if ip := net.ParseIP(stripIPv6Brackets(host)); ip != nil {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return nil, fmt.Errorf("parse endpoint ip %q failed", host)
		}
		resolved = append(resolved, addr.Unmap())
	} else {
		resolver := net.Resolver{PreferGo: true}
		ips, err := resolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve endpoint %q: %w", host, err)
		}
		for _, ip := range ips {
			addr, ok := netip.AddrFromSlice(ip)
			if ok {
				resolved = append(resolved, addr.Unmap())
			}
		}
	}

	var out []endpointCandidate
	seen := map[string]bool{}
	ipv6OK := hasUsableIPv6()
	add := func(addr netip.Addr, p int, source string) {
		if !addr.IsValid() || p <= 0 || p > 65535 {
			return
		}
		if addr.Is6() && !ipv6OK {
			return
		}
		ap := netip.AddrPortFrom(addr, uint16(p))
		key := ap.String()
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, endpointCandidate{addr: ap, source: source})
	}

	for _, fallbackPort := range endpointPortsInOrder(port) {
		for _, addr := range preferIPv4(resolved) {
			add(addr, fallbackPort, "configured")
		}
	}
	for _, fallbackPort := range endpointPortsInOrder(port) {
		for _, fallbackHost := range knownWarpEndpointHosts {
			if addr, err := netip.ParseAddr(fallbackHost); err == nil {
				add(addr, fallbackPort, "known-ip")
			}
		}
	}
	if len(out) > maxEndpointCandidates() {
		out = out[:maxEndpointCandidates()]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("resolve endpoint %q: no addresses", host)
	}
	return out, nil
}

func endpointPortsInOrder(primary int) []int {
	out := []int{primary}
	seen := map[int]bool{primary: true}
	for _, p := range knownWarpEndpointPorts {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func maxEndpointCandidates() int { return 48 }

func normalizeEndpoint(host string, port int) (string, int) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "engage.cloudflareclient.com"
	}
	if port <= 0 {
		port = defaultWarpEndpointPort
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			port = parsed
		}
	}
	return stripIPv6Brackets(host), port
}

func stripIPv6Brackets(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	return host
}

func preferIPv4(addrs []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if addr.Is4() {
			out = append(out, addr)
		}
	}
	for _, addr := range addrs {
		if !addr.Is4() {
			out = append(out, addr)
		}
	}
	return out
}

func hasUsableIPv6() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			addr, ok := netip.AddrFromSlice(ip)
			if ok && addr.Is6() && addr.IsGlobalUnicast() && !addr.IsPrivate() {
				return true
			}
		}
	}
	return false
}
