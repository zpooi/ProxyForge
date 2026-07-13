package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/proxy"
)

const manualAccountGenerationTimeout = 2 * time.Hour

type accountView struct {
	ID             int64   `json:"id"`
	Tag            string  `json:"tag"`
	Status         string  `json:"status"`
	PublicIP       string  `json:"public_ip"`
	Colo           string  `json:"colo"`
	Country        string  `json:"country"`
	LatencyMs      int     `json:"latency_ms"`
	SpeedBps       int     `json:"speed_bps"`
	PacketLoss     float64 `json:"packet_loss"`
	Score          float64 `json:"score"`
	IsKeeper       bool    `json:"is_keeper"`
	TrafficUp      int64   `json:"traffic_up"`
	TrafficDown    int64   `json:"traffic_down"`
	DisabledReason string  `json:"disabled_reason"`
}

type slotView struct {
	ID              int64   `json:"id"`
	Username        string  `json:"username"`
	Password        string  `json:"password"`
	Status          string  `json:"status"`
	AccountTag      string  `json:"account_tag"`
	AccountStatus   string  `json:"account_status"`
	PublicIP        string  `json:"public_ip"`
	PinnedPublicIP  string  `json:"pinned_public_ip"`
	Country         string  `json:"country"`
	LatencyMs       int     `json:"latency_ms"`
	SpeedBps        int     `json:"speed_bps"`
	PacketLoss      float64 `json:"packet_loss"`
	Score           float64 `json:"score"`
	LastError       string  `json:"last_error"`
	ProbeFailures   int     `json:"probe_failures"`
	IPDriftFailures int     `json:"ip_drift_failures"`
}

// agentProxyView 是一个在线远程 agent WARP 出口在代理列表里的一行。它和本机
// 槽位共用代理端口，但鉴权用户名是 node-<id>（在 proxy.resolve 里被解析成对应
// agent 出口），密码是全局共享代理密码。前端复制出来的链接可直接连、也和 Clash
// 订阅里的 agent 节点完全一致。
type agentProxyView struct {
	NodeID    string `json:"node_id"`
	AgentName string `json:"agent_name"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	PublicIP  string `json:"public_ip"`
	HostIP    string `json:"host_ip"`
	Country   string `json:"country"`
	Colo      string `json:"colo"`
	LatencyMs int    `json:"latency_ms"`
	SpeedBps  int64  `json:"speed_bps"`
	TrafficUp int64  `json:"traffic_up"`
	TrafficDn int64  `json:"traffic_down"`
}

func (h *Handlers) AccountsPage(w http.ResponseWriter, r *http.Request) {
	h.AppPage(w, r)
}

func (h *Handlers) AccountsJSON(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.DB.ListAccounts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slots, err := h.DB.ListProxySlots()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	running := tagSet(h.Scheduler.RunningTags())
	var views []accountView
	for _, a := range accounts {
		if a.Status != "active" || a.LastTestedAt == nil || a.LastPublicIP == "" || !running[a.Tag] {
			continue
		}
		views = append(views, accountView{
			ID: a.ID, Tag: a.Tag, Status: a.Status,
			PublicIP: a.LastPublicIP, Colo: a.LastColo, Country: a.LastCountry, LatencyMs: a.LastLatencyMs,
			SpeedBps: a.LastSpeedBps, PacketLoss: a.LastPacketLoss, Score: a.LastScore,
			IsKeeper: a.IsIPKeeper, TrafficUp: a.TrafficUp, TrafficDown: a.TrafficDown,
			DisabledReason: a.DisabledReason,
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		si := accountViewSortScore(views[i])
		sj := accountViewSortScore(views[j])
		if si == sj {
			return views[i].Tag < views[j].Tag
		}
		return si < sj
	})
	var slotViews []slotView
	for _, s := range slots {
		slotViews = append(slotViews, slotView{
			ID: s.ID, Username: s.Username, Password: s.Password, Status: s.Status,
			AccountTag: s.AccountTag, AccountStatus: s.AccountStatus, PublicIP: s.PublicIP,
			PinnedPublicIP: s.PinnedPublicIP, Country: s.Country,
			LatencyMs: s.LatencyMs, SpeedBps: s.SpeedBps, PacketLoss: s.PacketLoss,
			Score: s.Score, LastError: s.LastError, ProbeFailures: s.ProbeFailures,
			IPDriftFailures: s.IPDriftFailures,
		})
	}
	sort.SliceStable(slotViews, func(i, j int) bool {
		si := slotViewSortScore(slotViews[i])
		sj := slotViewSortScore(slotViews[j])
		if si == sj {
			return slotViews[i].Username < slotViews[j].Username
		}
		return si < sj
	})
	proxyPort := 7843
	if v, ok, _ := h.DB.GetSetting(SettingProxyPort); ok {
		fmt.Sscanf(v, "%d", &proxyPort)
	}
	// 复制单个代理链接的主机名，和导出订阅保持一致：优先用设置里的「代理对外地址」，
	// 没填才回退到访问域名。面板域名常经反代只暴露面板端口，套代理端口会连不通。
	proxyHost := requestHost(r)
	if v, ok, _ := h.DB.GetSetting(SettingProxyPublicHost); ok {
		if v = strings.TrimSpace(v); v != "" {
			proxyHost = v
		}
	}
	proxyPassword, _, _ := h.DB.GetSetting(SettingProxyPassword)
	proxyTLS, _, _ := h.DB.GetSetting(SettingProxyTLS)
	agents := h.collectAgentProxyViews(agentProxyPassword(proxyPassword))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accounts":   views,
		"slots":      slotViews,
		"agents":     agents,
		"proxy_host": proxyHost,
		"proxy_port": proxyPort,
		"proxy_tls":  proxyTLS != "off",
	})
}

// collectAgentProxyViews 把当前在线的远程 agent WARP 出口拍平成代理列表行。
// 每条在线会话（Hub 快照的一项）就是一个 WARP 出口，用户名 node-<id>、密码是
// 全局共享代理密码，和 Clash 订阅里的 agent 节点一一对应。离线出口不返回。
func (h *Handlers) collectAgentProxyViews(proxyPassword string) []agentProxyView {
	if h.Hub == nil {
		return nil
	}
	snapshot := h.Hub.Snapshot()
	views := make([]agentProxyView, 0, len(snapshot))
	for _, o := range snapshot {
		agentID := agentBaseID(o.Meta.AgentID, o.NodeID)
		host := agentHostDisplayName(o.Meta.AgentName, o.Meta.HostCountry, agentID)
		egressName := strings.TrimSpace(o.Meta.Name)
		if egressName == "" {
			egressName = host
			if o.Meta.EgressIndex > 0 {
				egressName = fmt.Sprintf("%s #%d", host, o.Meta.EgressIndex)
			}
		}
		views = append(views, agentProxyView{
			NodeID:    o.NodeID,
			AgentName: host,
			Name:      egressName,
			Username:  proxy.AgentUsername(o.NodeID),
			Password:  proxyPassword,
			PublicIP:  o.Meta.PublicIP,
			HostIP:    o.Meta.HostPublicIP,
			Country:   o.Meta.Country,
			Colo:      o.Meta.Colo,
			LatencyMs: o.LatencyMs,
			SpeedBps:  o.DownBps,
			TrafficUp: o.TxBytes,
			TrafficDn: o.RxBytes,
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].AgentName != views[j].AgentName {
			return views[i].AgentName < views[j].AgentName
		}
		return views[i].Name < views[j].Name
	})
	return views
}

func tagSet(tags []string) map[string]bool {
	out := make(map[string]bool, len(tags))
	for _, tag := range tags {
		out[tag] = true
	}
	return out
}

func accountViewSortScore(a accountView) float64 {
	score := 0.0
	if a.Status != "active" {
		score += 1_000_000_000
	}
	if a.PublicIP == "" {
		score += 100_000_000
	}
	if a.LatencyMs > 0 {
		score += float64(a.LatencyMs) * 2
	} else {
		score += 10_000_000
	}
	if a.SpeedBps > 0 {
		score += 200_000_000.0 / float64(a.SpeedBps)
	} else {
		score += 10_000_000
	}
	score += a.PacketLoss * 100_000
	if a.Score > 0 {
		score += a.Score * 0.05
	}
	if a.IsKeeper {
		score -= 250
	}
	return score
}

func slotViewSortScore(s slotView) float64 {
	score := 0.0
	if s.LastError != "" {
		score += 1_000_000_000
	}
	if s.Status != "active" || s.AccountStatus != "active" {
		score += 100_000_000
	}
	if s.PublicIP == "" {
		score += 10_000_000
	}
	if s.LatencyMs > 0 {
		score += float64(s.LatencyMs) * 2
	} else {
		score += 10_000_000
	}
	if s.SpeedBps > 0 {
		score += 200_000_000.0 / float64(s.SpeedBps)
	} else {
		score += 10_000_000
	}
	score += s.PacketLoss * 100_000
	if s.Score > 0 {
		score += s.Score * 0.05
	}
	return score
}

func (h *Handlers) AccountsGenerate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n, _ := strconv.Atoi(r.FormValue("n"))
	if n <= 0 {
		n = 1
	}
	if n > 50 {
		n = 50
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), manualAccountGenerationTimeout)
	go func() {
		defer cancel()
		if _, err := h.Scheduler.GenerateAccounts(ctx, n); err != nil {
			log.Printf("[accounts] background account generation failed: %v", err)
		}
	}()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "started", "n": n})
}

func (h *Handlers) AccountEnable(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	if id == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	_ = h.DB.UpdateAccountStatus(id, "active", "")
	_ = h.Scheduler.Reconcile()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (h *Handlers) AccountDisable(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	if id == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	_ = h.DB.UpdateAccountStatus(id, "disabled", "manually disabled")
	_ = h.Scheduler.Reconcile()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (h *Handlers) AccountRetest(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	if id == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	// 只重测这一个账号
	h.Scheduler.TestOne(id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "scheduled"})
}

func (h *Handlers) AccountDelete(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	if id == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	a, _ := h.DB.GetAccount(id)
	if a != nil {
		_ = h.DB.DeleteAccount(id)
		_ = h.Scheduler.Reconcile()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (h *Handlers) RunNow(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	kind := r.FormValue("kind")
	if kind == "" {
		kind = "dedup"
	}
	if err := h.Scheduler.Trigger(kind); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "scheduled", "kind": kind})
}
