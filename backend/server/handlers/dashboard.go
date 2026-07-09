package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/models"
)

func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	h.AppPage(w, r)
}

func (h *Handlers) DashboardJSON(w http.ResponseWriter, r *http.Request) {
	accounts, _ := h.DB.ListAccounts()
	clients, _ := h.DB.ListClientUsage(20)
	total := len(accounts)
	active := 0
	disabled := 0
	errCount := 0
	var totalUp, totalDown int64
	uniqueIPs := map[string]bool{}

	// 健康节点的平均延迟，供仪表盘顶部指标使用。
	var latencySum, latencyCount int
	running := tagSet(h.Scheduler.RunningTags())
	for _, a := range accounts {
		healthy := dashboardAccountHealthy(a, running)
		switch a.Status {
		case "active":
			if healthy {
				active++
			}
		case "disabled":
			disabled++
		case "error":
			errCount++
		}
		totalUp += a.TrafficUp
		totalDown += a.TrafficDown
		if healthy {
			uniqueIPs[a.LastPublicIP] = true
			latencySum += a.LastLatencyMs
			latencyCount++
		}
	}
	avgLatency := 0
	if latencyCount > 0 {
		avgLatency = latencySum / latencyCount
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

	type dashboardClient struct {
		ClientIP   string    `json:"client_ip"`
		Username   string    `json:"username"`
		AccountTag string    `json:"account_tag"`
		TotalUp    int64     `json:"total_up"`
		TotalDown  int64     `json:"total_down"`
		HitCount   int64     `json:"hit_count"`
		FirstSeen  time.Time `json:"first_seen_at"`
		LastSeen   time.Time `json:"last_seen_at"`
	}
	clientStats := make([]dashboardClient, 0, len(clients))
	for _, c := range clients {
		clientStats = append(clientStats, dashboardClient{
			ClientIP:   c.ClientIP,
			Username:   c.Username,
			AccountTag: c.AccountTag,
			TotalUp:    c.TotalUp,
			TotalDown:  c.TotalDown,
			HitCount:   c.HitCount,
			FirstSeen:  c.FirstSeen,
			LastSeen:   c.LastSeen,
		})
	}

	// 出口 IP 统计（仪表盘核心图表）：以 ip_pool 为主表，用健康账号富化
	// 国家/机房/延迟/绑定账号，并统计每个出口 IP 固定绑定的代理槽位数。
	// 数据源全部来自已加载的 accounts + slots + ip_pool，不新增查询开销。
	slots, _ := h.DB.ListProxySlots()

	// IP -> 提供该出口的健康账号（优先 keeper，其次任一命中该 IP 的健康账号）。
	type ipAccount struct {
		tag       string
		country   string
		colo      string
		latencyMs int
		speedBps  int
		healthy   bool
	}
	ipToAccount := map[string]ipAccount{}
	for _, a := range accounts {
		if a.LastPublicIP == "" {
			continue
		}
		healthy := dashboardAccountHealthy(a, running)
		cur, exists := ipToAccount[a.LastPublicIP]
		// 选取规则：keeper 优先，其次健康节点，最后才是任意账号。
		better := !exists ||
			(a.IsIPKeeper && !cur.healthy) ||
			(healthy && !cur.healthy)
		if better {
			ipToAccount[a.LastPublicIP] = ipAccount{
				tag:       a.Tag,
				country:   a.LastCountry,
				colo:      a.LastColo,
				latencyMs: a.LastLatencyMs,
				speedBps:  a.LastSpeedBps,
				healthy:   healthy || a.IsIPKeeper,
			}
		}
	}

	// 每个出口 IP 固定绑定的活跃代理槽位数。
	slotsByIP := map[string]int{}
	for _, s := range slots {
		if s.PinnedPublicIP != "" {
			slotsByIP[s.PinnedPublicIP]++
		}
	}

	type egressStat struct {
		IP         string     `json:"ip"`
		Country    string     `json:"country"`
		Colo       string     `json:"colo"`
		AccountTag string     `json:"account_tag"`
		LatencyMs  int        `json:"latency_ms"`
		SpeedBps   int        `json:"speed_bps"`
		SlotCount  int        `json:"slot_count"`
		CurrentUp  int64      `json:"current_up_bps"`
		CurrentDn  int64      `json:"current_down_bps"`
		TotalUp    int64      `json:"total_up"`
		TotalDown  int64      `json:"total_down"`
		LastSeen   *time.Time `json:"last_seen_at"`
	}
	ipEntries, _ := h.DB.ListIPPool()
	egress := make([]egressStat, 0, len(ipEntries))
	for _, e := range ipEntries {
		if e.PublicIP == "" {
			continue
		}
		acc := ipToAccount[e.PublicIP]
		egress = append(egress, egressStat{
			IP:         e.PublicIP,
			Country:    acc.country,
			Colo:       acc.colo,
			AccountTag: acc.tag,
			LatencyMs:  acc.latencyMs,
			SpeedBps:   acc.speedBps,
			SlotCount:  slotsByIP[e.PublicIP],
			CurrentUp:  e.CurrentUpBps,
			CurrentDn:  e.CurrentDownBps,
			TotalUp:    e.TotalUp,
			TotalDown:  e.TotalDown,
			LastSeen:   e.LastSeenAt,
		})
	}
	sort.Slice(egress, func(i, j int) bool {
		ti := egress[i].TotalUp + egress[i].TotalDown
		tj := egress[j].TotalUp + egress[j].TotalDown
		if ti == tj {
			return egress[i].IP < egress[j].IP
		}
		return ti > tj
	})

	resp := map[string]any{
		"active":          active,
		"disabled":        disabled,
		"error":           errCount,
		"total":           total,
		"unique_ips":      len(uniqueIPs),
		"proxy_slots":     h.Scheduler.ProxySlotCount(),
		"target_pool":     h.Scheduler.TargetWarpPoolSize(),
		"running_tunnels": h.Scheduler.RunningTunnels(),
		"avg_latency_ms":  avgLatency,
		"total_up":        totalUp,
		"total_down":      totalDown,
		"proxy_port":      proxyPort,
		"running":         h.Scheduler.IsRunning(),
		"last_run_at":     h.Scheduler.LastRunAt(),
		"next_run_at":     nextRun,
		"now":             time.Now(),
		"clients":         clientStats,
		"egress_ips":      egress,
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
