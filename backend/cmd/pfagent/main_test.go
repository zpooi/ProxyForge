package main

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/agentproto"
	"github.com/zpooi/ProxyForge/backend/internal/warp"
)

func TestWarpNodeIdentity(t *testing.T) {
	if got := warpNodeID("abc123", 0); got != "abc123" {
		t.Fatalf("first node id = %q", got)
	}
	if got := warpNodeID("abc123", 2); got != "abc123-3" {
		t.Fatalf("third node id = %q", got)
	}
	if got := warpNodeName("Malaysia", "MY", 1); got != "Malaysia #2" {
		t.Fatalf("custom node name = %q", got)
	}
	if got := warpNodeName("", "MY", 1); got != "MY WARP #2" {
		t.Fatalf("regional node name = %q", got)
	}
}

func TestRelayAgentUDPPreservesDatagrams(t *testing.T) {
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buffer := make([]byte, 1024)
		n, peer, err := echo.ReadFromUDP(buffer)
		if err == nil {
			_, _ = echo.WriteToUDP(append([]byte("reply:"), buffer[:n]...), peer)
		}
	}()

	remote, err := net.DialUDP("udp4", nil, echo.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	controller, agent := net.Pipe()
	defer controller.Close()
	_ = controller.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan struct{})
	go func() {
		relayAgentUDP(agent, remote)
		close(done)
	}()

	framed := agentproto.NewPacketConn(controller, echo.LocalAddr())
	if _, err := framed.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	n, err := framed.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:n]) != "reply:ping" {
		t.Fatalf("agent UDP response = %q", buffer[:n])
	}
	_ = controller.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent UDP relay did not stop")
	}
}

func TestBuildQueryIncludesAgentHostAndWarpEgress(t *testing.T) {
	egress := &warpEgress{
		index:     1,
		agentID:   "agent-123",
		agentName: "Malaysia VPS",
		nodeID:    "agent-123-2",
		name:      "MY WARP #2",
	}
	query := buildQuery(
		egress,
		egressMeta{ip: "104.28.1.2", country: "MY", colo: "KUL"},
		egressMeta{ip: "203.0.113.8", country: "MY", colo: "KUL"},
	)
	if query.Get("token") != "" {
		t.Fatal("agent token must not be placed in URL query")
	}

	for key, want := range map[string]string{
		"v":            "2",
		"agent_id":     "agent-123",
		"agent_name":   "Malaysia VPS",
		"node_id":      "agent-123-2",
		"egress_index": "2",
		"ip":           "104.28.1.2",
		"host_ip":      "203.0.113.8",
		"host_country": "MY",
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("query[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestWarpProfilePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warp-profiles.json")
	profiles := []warp.Account{{DeviceID: "device-1", AccessToken: "token-1"}}
	if err := saveWarpProfiles(path, profiles); err != nil {
		t.Fatal(err)
	}
	profiles[0].DeviceID = "device-2"
	if err := saveWarpProfiles(path, profiles); err != nil {
		t.Fatal(err)
	}
	got, err := loadWarpProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != "device-2" || got[0].AccessToken != "token-1" {
		t.Fatalf("profiles = %+v", got)
	}
}

func TestWarpTunnelConfig(t *testing.T) {
	account := warp.Account{
		PrivateKey:           "private",
		ClientID:             "client",
		PeerPublicKey:        "peer",
		EndpointHost:         "engage.cloudflareclient.com:2408",
		AddressV4:            "172.16.0.2",
		MasquePrivateKey:     "masque-private",
		MasqueEndpointPubKey: "masque-peer",
		MasqueEndpointV4:     "162.159.198.1",
	}
	cfg := warpTunnelConfig(account, "agent-test-1")
	if cfg.Tag != "agent-test-1" || cfg.EndpointHost != "engage.cloudflareclient.com" || cfg.EndpointPort != 2408 {
		t.Fatalf("config endpoint = %+v", cfg)
	}
	if cfg.TransportMode != "auto" || cfg.IPFamily != "ipv4" || cfg.MasqueEndpointV4 != "162.159.198.1" {
		t.Fatalf("config transport = %+v", cfg)
	}
}
