package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"strconv"

	"github.com/zpooi/ProxyForge/backend/internal/auth"
)

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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accounts":   views,
		"slots":      slotViews,
		"proxy_host": proxyHost,
		"proxy_port": proxyPort,
	})
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
	_ = auth.FromRequest(r)
	go func() {
		_, err := h.Scheduler.GenerateAccounts(r.Context(), n)
		_ = err
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
