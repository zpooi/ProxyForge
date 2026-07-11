package handlers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

func (h *Handlers) ExportProxies(w http.ResponseWriter, r *http.Request) {
	active, err := h.collectActiveExports(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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

// collectActiveExports 汇总当前可用（隧道在跑、有出口 IP）的固定代理槽位，
// 供导出接口和免登录订阅接口共用。
func (h *Handlers) collectActiveExports(r *http.Request) ([]*proxyExport, error) {
	slots, err := h.DB.ListProxySlots()
	if err != nil {
		return nil, err
	}
	settings, _ := h.DB.AllSettings()
	proxyPort := 7843
	if v, ok := settings[SettingProxyPort]; ok {
		fmt.Sscanf(v, "%d", &proxyPort)
	}

	// 优先用设置里的「代理对外地址」（服务器真实 IP 或灰云域名）。面板域名常经
	// Cloudflare / nginx 只反代面板端口，套上代理端口会连不通，所以不能直接用它。
	host := strings.TrimSpace(settings[SettingProxyPublicHost])
	if host == "" {
		host = requestHost(r)
	}
	// TLS 默认开启（opportunistic）。开启时导出的 Clash 节点带 tls + skip-cert-verify，
	// 让客户端把 CONNECT 主机名藏进加密流，避开审查中间盒基于主机名的连接重置。
	proxyTLS := settings[SettingProxyTLS] != "off"
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
			TLS:           proxyTLS,
		})
	}
	return active, nil
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
	TLS           bool
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

	writeClashRules(w)
}

// writeClashRules 输出规则段。没有 rules 时 Clash 规则模式下所有连接都无处匹配、
// fallthrough 成直连，代理形同虚设（全局模式绕过规则所以照常工作）。这里让
// 内网/回环直连，其余全部经 PROXYFORGE，并以 MATCH 兜底保证规则模式可用。
func writeClashRules(w http.ResponseWriter) {
	rules := []string{
		"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
		"IP-CIDR6,::1/128,DIRECT,no-resolve",
		"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
		"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
		"MATCH,PROXYFORGE",
	}
	fmt.Fprintf(w, "\nrules:\n")
	for _, r := range rules {
		fmt.Fprintf(w, "  - %s\n", r)
	}
}

func writeClashProxy(w http.ResponseWriter, p *proxyExport) {
	fmt.Fprintf(w, "  - name: %s\n", p.Username)
	fmt.Fprintf(w, "    type: http\n")
	fmt.Fprintf(w, "    server: %s\n", p.ProxyHost)
	fmt.Fprintf(w, "    port: %d\n", p.ProxyPort)
	fmt.Fprintf(w, "    username: %s\n", p.Username)
	fmt.Fprintf(w, "    password: %s\n", p.Password)
	// TLS 开启时，客户端对「客户端↔代理」这一跳套 TLS，把明文 CONNECT 主机名藏进
	// 加密流。证书是内存自签，故 skip-cert-verify；威胁模型是审查中间盒被动读主机名，
	// 而非定向 MITM，账号密码仍然鉴权。
	if p.TLS {
		fmt.Fprintf(w, "    tls: true\n")
		fmt.Fprintf(w, "    skip-cert-verify: true\n")
		if p.ProxyHost != "" {
			fmt.Fprintf(w, "    sni: %s\n", p.ProxyHost)
		}
	}
}

// SubscriptionToken 返回（首次调用时生成）免登录订阅所用的 token。
// 需要登录，前端用它拼出完整的 Clash 订阅链接。
func (h *Handlers) SubscriptionToken(w http.ResponseWriter, r *http.Request) {
	token, err := h.ensureSubscriptionToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": token,
		"path":  "/sub/clash",
	})
}

// ClashSubscription 是免登录的 Clash 订阅端点，靠 URL 里的 token 鉴权，
// 让 Clash 客户端可以直接添加订阅并定时同步节点。
func (h *Handlers) ClashSubscription(w http.ResponseWriter, r *http.Request) {
	want, _, err := h.DB.GetSetting(SettingSubscriptionToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	got := r.URL.Query().Get("token")
	if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	active, err := h.collectActiveExports(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=proxyforge-clash.yaml")
	writeClash(w, active)
}

func (h *Handlers) ensureSubscriptionToken() (string, error) {
	token, ok, err := h.DB.GetSetting(SettingSubscriptionToken)
	if err != nil {
		return "", err
	}
	if ok && token != "" {
		return token, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token = hex.EncodeToString(b)
	if err := h.DB.SetSetting(SettingSubscriptionToken, token); err != nil {
		return "", err
	}
	return token, nil
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
