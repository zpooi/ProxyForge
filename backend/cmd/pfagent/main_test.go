package main

import (
	"path/filepath"
	"testing"

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
