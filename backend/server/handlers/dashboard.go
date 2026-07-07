package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/models"
)

func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	h.AppPage(w, r)
}

func (h *Handlers) DashboardJSON(w http.ResponseWriter, r *http.Request) {
	accounts, _ := h.DB.ListAccounts()
	total := len(accounts)
	active := 0
	disabled := 0
	errCount := 0
	var totalUp, totalDown int64
	uniqueIPs := map[string]bool{}
	keeperCount := 0
	running := tagSet(h.Scheduler.RunningTags())
	for _, a := range accounts {
		switch a.Status {
		case "active":
			if dashboardAccountHealthy(a, running) {
				active++
			}
		case "disabled":
			disabled++
		case "error":
			errCount++
		}
		totalUp += a.TrafficUp
		totalDown += a.TrafficDown
		if dashboardAccountHealthy(a, running) {
			uniqueIPs[a.LastPublicIP] = true
		}
		if a.IsIPKeeper {
			keeperCount++
		}
	}

	settings, _ := h.DB.AllSettings()
	dedupInterval := 600
	if v, ok := settings[SettingDedupIntervalSeconds]; ok {
		var n int
		_, _ = parseInt(v, &n)
		if n > 0 {
			dedupInterval = n
		}
	}
	proxyPort := 7843
	if v, ok := settings[SettingProxyPort]; ok {
		var n int
		_, _ = parseInt(v, &n)
		if n > 0 {
			proxyPort = n
		}
	}

	nextRun := h.Scheduler.LastRunAt().Add(time.Duration(dedupInterval) * time.Second)
	if h.Scheduler.LastRunAt().IsZero() {
		nextRun = time.Now().Add(30 * time.Second)
	}

	resp := map[string]any{
		"total":           total,
		"active":          active,
		"disabled":        disabled,
		"error":           errCount,
		"unique_ips":      len(uniqueIPs),
		"keepers":         keeperCount,
		"proxy_slots":     h.Scheduler.ProxySlotCount(),
		"target_pool":     h.Scheduler.TargetWarpPoolSize(),
		"running_tunnels": h.Scheduler.RunningTunnels(),
		"total_up":        totalUp,
		"total_down":      totalDown,
		"proxy_port":      proxyPort,
		"running":         h.Scheduler.IsRunning(),
		"last_run_at":     h.Scheduler.LastRunAt(),
		"next_run_at":     nextRun,
		"now":             time.Now(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func dashboardAccountHealthy(a *models.Account, running map[string]bool) bool {
	return a != nil &&
		running[a.Tag] &&
		a.Status == "active" &&
		a.LastTestedAt != nil &&
		a.LastPublicIP != "" &&
		a.LastLatencyMs > 0 &&
		a.LastLatencyMs <= 700 &&
		a.LastSpeedBps >= 120*1024 &&
		a.LastPacketLoss < 0.50
}
