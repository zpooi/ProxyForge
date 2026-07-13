package handlers

import (
	"strings"
	"testing"

	"github.com/zpooi/ProxyForge/backend/internal/agenthub"
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

func TestAgentInstallScriptUsesThreeWarpEgressesAndRestarts(t *testing.T) {
	script := agentInstallScript("https://panel.example.com", "secret")
	for _, want := range []string{
		"-warp-count 3",
		"systemctl restart pfagent.service",
		"install -m 0755",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("install script missing %q", want)
		}
	}
}

func TestAgentUninstallCommandRemovesServiceAndState(t *testing.T) {
	command := agentUninstallCommand()
	for _, want := range []string{
		"systemctl disable --now pfagent.service",
		"/etc/systemd/system/pfagent.service",
		"/usr/local/bin/pfagent",
		"/var/lib/pfagent",
		"systemctl daemon-reload",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("uninstall command missing %q", want)
		}
	}
}

func TestOnlineAgentViewsGroupsWarpEgressesByVPS(t *testing.T) {
	snapshot := []agenthub.OnlineNode{
		{
			NodeID: "abc123-2",
			Meta: agenthub.Meta{
				AgentID: "abc123", HostPublicIP: "203.0.113.8", HostCountry: "MY", HostColo: "KUL",
				PublicIP: "104.28.1.2", Country: "MY", Colo: "KUL", EgressIndex: 2,
			},
			LatencyMs: 20, TxBytes: 30, RxBytes: 40,
		},
		{
			NodeID: "abc123",
			Meta: agenthub.Meta{
				AgentID: "abc123", HostPublicIP: "203.0.113.8", HostCountry: "MY", HostColo: "KUL",
				PublicIP: "104.28.1.1", Country: "MY", Colo: "KUL", EgressIndex: 1,
			},
			LatencyMs: 10, TxBytes: 10, RxBytes: 20,
		},
	}

	views := onlineAgentViews(snapshot)
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	view := views[0]
	if view.PublicIP != "203.0.113.8" || view.Country != "MY" || view.EgressCount != 2 {
		t.Fatalf("unexpected agent summary: %+v", view)
	}
	if view.LatencyMs != 15 || view.TxBytes != 40 || view.RxBytes != 60 {
		t.Fatalf("unexpected aggregate metrics: %+v", view)
	}
	if view.Egresses[0].PublicIP != "104.28.1.1" || view.Egresses[1].PublicIP != "104.28.1.2" {
		t.Fatalf("unexpected egress order: %+v", view.Egresses)
	}
}

func TestAgentBaseIDSupportsLegacyWarpSuffix(t *testing.T) {
	if got := agentBaseID("", "0123456789abcdef-3"); got != "0123456789abcdef" {
		t.Fatalf("legacy agent base id = %q", got)
	}
	if got := agentBaseID("reported", "0123456789abcdef-3"); got != "reported" {
		t.Fatalf("reported agent base id = %q", got)
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
