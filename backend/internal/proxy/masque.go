package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"time"

	usqueapi "github.com/inipew/usque/api"
	"github.com/inipew/usque/masque"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	masqueProbeTimeout = 20 * time.Second
	masqueConnectSNI   = "consumer-masque.cloudflareclient.com"
	masquePort         = 443
)

type masqueEndpoint struct {
	addr     net.Addr
	label    string
	useHTTP2 bool
	insecure bool
}

func newMasqueTunnel(cfg Config) (*Tunnel, error) {
	if !cfg.hasMasqueConfig() {
		return nil, fmt.Errorf("tunnel %s: MASQUE credentials are missing", cfg.Tag)
	}
	endpoints := masqueEndpointCandidates(cfg)
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("tunnel %s: no MASQUE endpoints", cfg.Tag)
	}

	failures := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		t, err := newMasqueTunnelAtEndpoint(cfg, endpoint)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint.label, err))
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), masqueProbeTimeout)
		err = t.probe(ctx)
		cancel()
		if err == nil {
			return t, nil
		}

		status := t.status()
		t.Close()
		failures = append(failures, fmt.Sprintf("%s: %v (%s)", endpoint.label, err, status))
	}

	return nil, fmt.Errorf("tunnel %s: no reachable MASQUE endpoint after %d attempt(s): %s",
		cfg.Tag, len(endpoints), summarizeFailures(failures))
}

func newMasqueTunnelAtEndpoint(cfg Config, endpoint masqueEndpoint) (*Tunnel, error) {
	mtu := defaultMTU(cfg.MTU)
	localAddrs, err := localAddrsFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	privKey, err := parseMasquePrivateKey(cfg.MasquePrivateKey)
	if err != nil {
		return nil, err
	}
	peerPubKey, err := parseMasqueEndpointPublicKey(cfg.MasqueEndpointPubKey)
	if err != nil {
		return nil, err
	}
	cert, err := generateMasqueCert(privKey)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := masque.PrepareTlsConfig(privKey, peerPubKey, cert, masqueConnectSNI, endpoint.insecure)
	if err != nil {
		return nil, err
	}

	tunDev, tnet, err := netstack.CreateNetTUN(localAddrs, tunnelDNSAddrs(), mtu)
	if err != nil {
		return nil, fmt.Errorf("create MASQUE netstack tun: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go usqueapi.MaintainTunnel(ctx, usqueapi.MaintainTunnelConfig{
		TLSConfig:       tlsConfig,
		KeepalivePeriod: 30 * time.Second,
		Endpoint:        endpoint.addr,
		Device:          usqueapi.NewNetstackAdapter(tunDev),
		MTU:             mtu,
		ReconnectDelay:  1 * time.Second,
		AlwaysReconnect: true,
		UseHTTP2:        endpoint.useHTTP2,
		HookEnv: map[string]string{
			"USQUE_MODE": "proxyforge",
			"USQUE_IPV4": cfg.LocalAddrV4,
			"USQUE_IPV6": cfg.LocalAddrV6,
		},
	})

	return &Tunnel{
		cfg:       cfg,
		transport: "masque",
		endpoint:  endpoint.label,
		tnet:      tnet,
		tunCloser: tunDev,
		cancel:    cancel,
	}, nil
}

func masqueEndpointCandidates(cfg Config) []masqueEndpoint {
	var out []masqueEndpoint
	if ep, ok := parseMasqueUDPEndpoint(cfg.MasqueEndpointV4); ok {
		out = append(out, ep)
	}
	if ep, ok := parseMasqueUDPEndpoint(cfg.MasqueEndpointV6); ok && hasUsableIPv6() {
		out = append(out, ep)
	}
	return out
}

func parseMasqueUDPEndpoint(raw string) (masqueEndpoint, bool) {
	ip := parseEndpointIP(raw)
	if ip == nil {
		return masqueEndpoint{}, false
	}
	addr := &net.UDPAddr{IP: ip, Port: masquePort}
	return masqueEndpoint{
		addr:  addr,
		label: "h3/" + addr.String(),
	}, true
}

func parseEndpointIP(raw string) net.IP {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	if addr, err := netip.ParseAddr(raw); err == nil {
		return net.IP(addr.AsSlice())
	}
	return net.ParseIP(raw)
}

func parseMasquePrivateKey(value string) (*ecdsa.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("MASQUE private key base64 decode: %w", err)
	}
	key, err := x509.ParseECPrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse MASQUE private key: %w", err)
	}
	return key, nil
}

func parseMasqueEndpointPublicKey(value string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil {
		return nil, fmt.Errorf("decode MASQUE endpoint public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse MASQUE endpoint public key: %w", err)
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("MASQUE endpoint public key is %T, expected ECDSA", pub)
	}
	return ecPub, nil
}

func generateMasqueCert(privKey *ecdsa.PrivateKey) ([][]byte, error) {
	cert, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}, &x509.Certificate{}, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("generate MASQUE cert: %w", err)
	}
	return [][]byte{cert}, nil
}
