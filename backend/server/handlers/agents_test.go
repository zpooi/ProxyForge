package handlers

import (
	"testing"

	"github.com/zpooi/ProxyForge/backend/internal/models"
)

func TestSummarizeLocalNodeMultipleEgresses(t *testing.T) {
	accounts := []*models.Account{
		{Tag: "warp-1", Status: "active", LastPublicIP: "198.51.100.1", LastCountry: "SG", LastColo: "SIN", LastLatencyMs: 100, TrafficUp: 10, TrafficDown: 20},
		{Tag: "warp-2", Status: "active", LastPublicIP: "198.51.100.2", LastCountry: "JP", LastColo: "NRT", LastLatencyMs: 200, TrafficUp: 30, TrafficDown: 40},
		{Tag: "warp-3", Status: "active", LastPublicIP: "198.51.100.3", LastCountry: "US", LastColo: "LAX", LastLatencyMs: 300, TrafficUp: 50, TrafficDown: 60},
	}

	got := summarizeLocalNode(accounts, map[string]bool{"warp-1": true, "warp-2": true})
	if got.PublicIP != "2 个出口" || got.Country != "2 个地区" || got.Colo != "" {
		t.Fatalf("unexpected location summary: %+v", got)
	}
	if got.LatencyMs != 150 || got.TxBytes != 40 || got.RxBytes != 60 {
		t.Fatalf("unexpected metrics summary: %+v", got)
	}
}

func TestSummarizeLocalNodeSingleEgress(t *testing.T) {
	accounts := []*models.Account{
		{Tag: "warp-1", Status: "active", LastPublicIP: "198.51.100.1", LastCountry: "SG", LastColo: "SIN", LastLatencyMs: 88},
	}

	got := summarizeLocalNode(accounts, map[string]bool{"warp-1": true})
	if got.PublicIP != "198.51.100.1" || got.Country != "SG" || got.Colo != "SIN" || got.LatencyMs != 88 {
		t.Fatalf("unexpected single egress summary: %+v", got)
	}
}
