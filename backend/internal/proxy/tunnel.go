package proxy

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

type Config struct {
	Tag           string
	PrivateKey    string
	ClientID      string
	PeerPublicKey string
	LocalAddrV4   string
	LocalAddrV6   string
	EndpointHost  string
	EndpointPort  int
	MTU           int
	ListenPort    int
	IsKeeper      bool

	TransportMode string
	IPFamily      string
	DNSMode       string

	MasquePrivateKey     string
	MasqueEndpointPubKey string
	MasqueEndpointV4     string
	MasqueEndpointV6     string
}

type Tunnel struct {
	cfg       Config
	transport string
	endpoint  string
	dev       *device.Device
	tnet      *netstack.Net
	tunCloser io.Closer
	bind      *reservedBind
	logger    *device.Logger
	cancel    context.CancelFunc

	txBytes atomic.Int64
	rxBytes atomic.Int64

	lastDialLatencyMs atomic.Int64
	lastDialAtUnix    atomic.Int64
	dialFailures      atomic.Int64
}

const (
	tunnelProbeURL     = "https://www.cloudflare.com/cdn-cgi/trace"
	tunnelProbeTimeout = 4 * time.Second
)

var endpointPreference = struct {
	sync.Mutex
	value       string
	failedUntil time.Time
}{}

func newTunnel(cfg Config) (*Tunnel, error) {
	mode := normalizeTransportMode(cfg.TransportMode)
	if mode == "masque" {
		return newMasqueTunnel(cfg)
	}
	if mode == "auto" {
		if !cfg.hasMasqueConfig() {
			return nil, fmt.Errorf("tunnel %s: MASQUE config is missing; WireGuard UDP fallback is disabled in auto mode", cfg.Tag)
		}
		t, err := newMasqueTunnel(cfg)
		if err == nil {
			return t, nil
		}
		return nil, fmt.Errorf("tunnel %s: MASQUE failed: %w", cfg.Tag, err)
	}
	if mode == "wireguard" {
		return newWireGuardTunnel(cfg)
	}
	return nil, fmt.Errorf("tunnel %s: unsupported transport mode %q", cfg.Tag, cfg.TransportMode)
}

func normalizeTransportMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		return "auto"
	case "masque":
		return "masque"
	case "wireguard", "wg":
		return "wireguard"
	default:
		return mode
	}
}

func (cfg Config) hasMasqueConfig() bool {
	return strings.TrimSpace(cfg.MasquePrivateKey) != "" &&
		strings.TrimSpace(cfg.MasqueEndpointPubKey) != "" &&
		(strings.TrimSpace(cfg.MasqueEndpointV4) != "" || strings.TrimSpace(cfg.MasqueEndpointV6) != "")
}

func newWireGuardTunnel(cfg Config) (*Tunnel, error) {
	if wait := endpointProbeBackoff(); wait > 0 {
		return nil, fmt.Errorf("tunnel %s: WARP endpoints recently failed; retry probing in %.0fs", cfg.Tag, wait.Seconds())
	}
	candidates, err := endpointCandidates(cfg.EndpointHost, cfg.EndpointPort)
	if err != nil {
		return nil, err
	}
	candidates = prioritizeEndpointCandidates(candidates)

	failures := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		t, err := newTunnelAtEndpoint(cfg, candidate.String())
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s/%s: %v", candidate.String(), candidate.source, err))
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), tunnelProbeTimeout)
		err = t.probe(ctx)
		cancel()
		if err == nil {
			rememberEndpoint(candidate.String())
			return t, nil
		}

		status := t.wireGuardStatus()
		t.Close()
		log.Printf("[proxy] tunnel %s endpoint %s probe failed: %v (%s)", cfg.Tag, candidate.String(), err, status)
		failures = append(failures, fmt.Sprintf("%s/%s: %v (%s)", candidate.String(), candidate.source, err, status))
	}

	rememberEndpointFailure()
	return nil, fmt.Errorf("tunnel %s: no reachable WARP endpoint after %d attempt(s): %s",
		cfg.Tag, len(candidates), summarizeFailures(failures))
}

func newTunnelAtEndpoint(cfg Config, endpoint string) (*Tunnel, error) {
	mtu := defaultMTU(cfg.MTU)
	localAddrs, err := localAddrsFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	tunDev, tnet, err := netstack.CreateNetTUN(localAddrs, tunnelDNSAddrs(), mtu)
	if err != nil {
		return nil, fmt.Errorf("create netstack tun: %w", err)
	}

	bind := newReservedBind()
	if cfg.ClientID != "" {
		reserved, err := clientIDReserved(cfg.ClientID)
		if err != nil {
			_ = tunDev.Close()
			return nil, err
		}
		endpointAddr, err := netip.ParseAddrPort(endpoint)
		if err != nil {
			_ = tunDev.Close()
			return nil, fmt.Errorf("parse endpoint %q: %w", endpoint, err)
		}
		bind.SetReservedForEndpoint(endpointAddr, reserved)
	}

	logger := device.NewLogger(device.LogLevelError, fmt.Sprintf("[wg %s] ", cfg.Tag))
	dev := device.NewDevice(tunDev, bind, logger)

	uapi, err := buildUAPI(cfg, endpoint)
	if err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wg ipc set: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wg up: %w", err)
	}

	return &Tunnel{cfg: cfg, transport: "wireguard", endpoint: endpoint, dev: dev, tnet: tnet, bind: bind, logger: logger}, nil
}

func buildUAPI(cfg Config, endpoint string) (string, error) {
	privHex, err := base64ToHex(cfg.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private key: %w", err)
	}
	peerHex, err := base64ToHex(cfg.PeerPublicKey)
	if err != nil {
		return "", fmt.Errorf("peer public key: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", privHex)
	fmt.Fprintf(&b, "replace_peers=true\n")
	fmt.Fprintf(&b, "public_key=%s\n", peerHex)
	fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	fmt.Fprintf(&b, "replace_allowed_ips=true\n")
	fmt.Fprintf(&b, "allowed_ip=0.0.0.0/0\n")
	fmt.Fprintf(&b, "allowed_ip=::/0\n")
	fmt.Fprintf(&b, "persistent_keepalive_interval=25\n")
	return b.String(), nil
}

func (t *Tunnel) probe(ctx context.Context) error {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return t.DialContext(ctx, network, address)
		},
		ForceAttemptHTTP2: false,
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{Transport: tr}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tunnelProbeURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ProxyForge/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("trace returned HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "ip=") {
		return fmt.Errorf("trace response missing ip")
	}
	return nil
}

func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if t == nil || t.tnet == nil {
		return nil, fmt.Errorf("tunnel is not ready")
	}
	if t.useSystemDNS() {
		if resolved, ok := t.resolveSystemDialAddress(ctx, network, address); ok {
			conn, err := t.tnet.DialContext(ctx, network, resolved)
			if err == nil {
				return conn, nil
			}
		}
	}
	return t.tnet.DialContext(ctx, network, address)
}

func (t *Tunnel) useSystemDNS() bool {
	return strings.EqualFold(strings.TrimSpace(t.cfg.DNSMode), "system")
}

func (t *Tunnel) resolveSystemDialAddress(ctx context.Context, network, address string) (string, bool) {
	if !strings.HasPrefix(network, "tcp") && !strings.HasPrefix(network, "udp") {
		return "", false
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return "", false
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && ip.IsValid() {
		return "", false
	}
	dnsCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		dnsCtx, cancel = context.WithTimeout(ctx, 1200*time.Millisecond)
	}
	defer cancel()

	addrs, err := net.DefaultResolver.LookupIPAddr(dnsCtx, host)
	if err != nil || len(addrs) == 0 {
		return "", false
	}
	family := normalizeIPFamily(t.cfg.IPFamily)
	for _, addr := range addrs {
		ip, ok := netip.AddrFromSlice(addr.IP)
		if !ok {
			continue
		}
		if family == "ipv4" && !ip.Is4() {
			continue
		}
		if family == "ipv6" && !ip.Is6() {
			continue
		}
		return net.JoinHostPort(ip.String(), port), true
	}
	return "", false
}

func (t *Tunnel) noteDial(elapsed time.Duration, err error) {
	if t == nil {
		return
	}
	if err != nil {
		t.dialFailures.Add(1)
		return
	}
	ms := elapsed.Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	t.lastDialLatencyMs.Store(ms)
	t.lastDialAtUnix.Store(time.Now().Unix())
	t.dialFailures.Store(0)
}

func (t *Tunnel) status() string {
	if t == nil {
		return "closed"
	}
	if t.transport == "wireguard" {
		return t.wireGuardStatus()
	}
	return fmt.Sprintf("transport=%s endpoint=%s", t.transport, t.endpoint)
}

func (t *Tunnel) wireGuardStatus() string {
	if t.dev == nil {
		return "wg=closed"
	}
	state, err := t.dev.IpcGet()
	if err != nil {
		return "wg=unknown: " + err.Error()
	}
	var endpoint string
	var handshakeSec int64
	var tx, rx int64
	for _, line := range strings.Split(state, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "endpoint":
			endpoint = value
		case "last_handshake_time_sec":
			handshakeSec, _ = strconv.ParseInt(value, 10, 64)
		case "tx_bytes":
			tx, _ = strconv.ParseInt(value, 10, 64)
		case "rx_bytes":
			rx, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	handshake := "never"
	if handshakeSec > 0 {
		handshake = time.Unix(handshakeSec, 0).Format(time.RFC3339)
	}
	status := fmt.Sprintf("endpoint=%s handshake=%s wg_tx=%d wg_rx=%d", endpoint, handshake, tx, rx)
	if t.bind != nil {
		status += " " + t.bind.Stats()
	}
	return status
}

func prioritizeEndpointCandidates(candidates []endpointCandidate) []endpointCandidate {
	preferred := preferredEndpoint()
	if preferred == "" || len(candidates) < 2 {
		return candidates
	}
	out := make([]endpointCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.String() == preferred {
			out = append(out, c)
			break
		}
	}
	for _, c := range candidates {
		if c.String() != preferred {
			out = append(out, c)
		}
	}
	return out
}

func preferredEndpoint() string {
	endpointPreference.Lock()
	defer endpointPreference.Unlock()
	return endpointPreference.value
}

func rememberEndpoint(endpoint string) {
	endpointPreference.Lock()
	endpointPreference.value = endpoint
	endpointPreference.failedUntil = time.Time{}
	endpointPreference.Unlock()
}

func rememberEndpointFailure() {
	endpointPreference.Lock()
	defer endpointPreference.Unlock()
	if endpointPreference.value == "" {
		endpointPreference.failedUntil = time.Now().Add(60 * time.Second)
	}
}

func endpointProbeBackoff() time.Duration {
	endpointPreference.Lock()
	defer endpointPreference.Unlock()
	if endpointPreference.value != "" || endpointPreference.failedUntil.IsZero() {
		return 0
	}
	return time.Until(endpointPreference.failedUntil)
}

func summarizeFailures(failures []string) string {
	if len(failures) == 0 {
		return "no candidates"
	}
	if len(failures) > 4 {
		return strings.Join(failures[:4], "; ") + fmt.Sprintf("; ... %d more", len(failures)-4)
	}
	return strings.Join(failures, "; ")
}

func (t *Tunnel) Close() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.dev != nil {
		t.dev.Close()
	}
	if t.tunCloser != nil {
		_ = t.tunCloser.Close()
	}
}

func defaultMTU(mtu int) int {
	if mtu <= 0 {
		return 1280
	}
	return mtu
}

func tunnelDNSAddrs() []netip.Addr {
	return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
}

func localAddrsFromConfig(cfg Config) ([]netip.Addr, error) {
	var localAddrs []netip.Addr
	family := normalizeIPFamily(cfg.IPFamily)
	if cfg.LocalAddrV4 != "" && family != "ipv6" {
		a, err := parseAddrStripMask(cfg.LocalAddrV4)
		if err != nil {
			return nil, fmt.Errorf("parse local v4 %q: %w", cfg.LocalAddrV4, err)
		}
		localAddrs = append(localAddrs, a)
	}
	if cfg.LocalAddrV6 != "" && family != "ipv4" {
		a, err := parseAddrStripMask(cfg.LocalAddrV6)
		if err != nil {
			return nil, fmt.Errorf("parse local v6 %q: %w", cfg.LocalAddrV6, err)
		}
		localAddrs = append(localAddrs, a)
	}
	if len(localAddrs) == 0 {
		return nil, fmt.Errorf("tunnel %s: no local address", cfg.Tag)
	}
	return localAddrs, nil
}

func normalizeIPFamily(family string) string {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "", "ipv4", "v4", "4":
		return "ipv4"
	case "ipv6", "v6", "6":
		return "ipv6"
	case "dual", "both", "all":
		return "dual"
	default:
		return "ipv4"
	}
}

func parseAddrStripMask(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return netip.ParseAddr(s)
}

func base64ToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d bytes", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

func clientIDReserved(clientID string) ([3]byte, error) {
	var reserved [3]byte
	clientID = strings.TrimSpace(clientID)
	raw, err := base64.StdEncoding.DecodeString(clientID)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(clientID)
	}
	if err != nil {
		return reserved, fmt.Errorf("client_id base64 decode: %w", err)
	}
	if len(raw) != 3 {
		return reserved, fmt.Errorf("client_id expected 3 bytes, got %d", len(raw))
	}
	copy(reserved[:], raw)
	return reserved, nil
}
