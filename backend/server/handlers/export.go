package handlers

import (
	"fmt"
	"net"
	"net/http"
)

func (h *Handlers) ExportProxies(w http.ResponseWriter, r *http.Request) {
	slots, err := h.DB.ListProxySlots()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings, _ := h.DB.AllSettings()
	proxyPort := 7843
	if v, ok := settings[SettingProxyPort]; ok {
		fmt.Sscanf(v, "%d", &proxyPort)
	}

	host := requestHost(r)
	var active []*proxyExport
	running := tagSet(h.Scheduler.RunningTags())
	for _, s := range slots {
		if s.Status != "active" || s.AccountTag == "" || s.AccountStatus != "active" || s.PublicIP == "" || !running[s.AccountTag] {
			continue
		}
		active = append(active, &proxyExport{
			Username:      s.Username,
			Password:      s.Password,
			AccountTag:    s.AccountTag,
			AccountStatus: s.AccountStatus,
			PublicIP:      s.PublicIP,
			Country:       s.Country,
			LatencyMs:     s.LatencyMs,
			SpeedBps:      s.SpeedBps,
			PacketLoss:    s.PacketLoss,
			Score:         s.Score,
			Keeper:        s.IsKeeper,
			LastSlotError: s.LastError,
			ProxyHost:     host,
			ProxyPort:     proxyPort,
		})
	}

	format := r.URL.Query().Get("format")
	if format == "clash" {
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=proxyforge-clash.yaml")
		writeClash(w, active)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writePlain(w, active)
}

type proxyExport struct {
	Username      string
	Password      string
	AccountTag    string
	AccountStatus string
	PublicIP      string
	Country       string
	LatencyMs     int
	SpeedBps      int
	PacketLoss    float64
	Score         float64
	Keeper        bool
	LastSlotError string
	ProxyHost     string
	ProxyPort     int
}

func writePlain(w http.ResponseWriter, list []*proxyExport) {
	fmt.Fprintf(w, "# ProxyForge 固定代理账号\n")
	fmt.Fprintf(w, "# 格式：用户名:密码@主机:端口\n")
	fmt.Fprintf(w, "# 账号密码保持稳定；底层 WARP 失效后会自动换绑。\n\n")
	for _, p := range list {
		ip := p.PublicIP
		if ip == "" {
			ip = "untested"
		}
		score := "-"
		if p.Score > 0 {
			score = fmt.Sprintf("%.0f", p.Score)
		}
		tag := p.AccountTag
		if tag == "" {
			tag = "未绑定"
		}
		country := p.Country
		if country == "" {
			country = "-"
		}
		fmt.Fprintf(w, "%s:%s@%s:%d  (%s / %s / %s / score %s)\n",
			p.Username, p.Password, p.ProxyHost, p.ProxyPort, tag, ip, country, score)
	}
}

func writeClash(w http.ResponseWriter, list []*proxyExport) {
	fmt.Fprintf(w, "# ProxyForge - Clash 固定代理账号\n")
	fmt.Fprintf(w, "proxies:\n")
	for _, p := range list {
		writeClashProxy(w, p)
	}
	fmt.Fprintf(w, "\nproxy-groups:\n")
	fmt.Fprintf(w, "  - name: PROXYFORGE\n")
	fmt.Fprintf(w, "    type: select\n")
	fmt.Fprintf(w, "    proxies:\n")
	for _, p := range list {
		fmt.Fprintf(w, "      - %s\n", p.Username)
	}
}

func writeClashProxy(w http.ResponseWriter, p *proxyExport) {
	fmt.Fprintf(w, "  - name: %s\n", p.Username)
	fmt.Fprintf(w, "    type: http\n")
	fmt.Fprintf(w, "    server: %s\n", p.ProxyHost)
	fmt.Fprintf(w, "    port: %d\n", p.ProxyPort)
	fmt.Fprintf(w, "    username: %s\n", p.Username)
	fmt.Fprintf(w, "    password: %s\n", p.Password)
}

func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return "127.0.0.1"
	}
	return host
}
