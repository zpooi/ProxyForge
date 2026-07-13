package handlers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/proxy"
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

// proxyEndpoint 汇总代理对外连接所需的信息（对外地址、端口、TLS、共享密码），
// 供导出、订阅和统一轮换凭据接口共用。
type proxyEndpoint struct {
	Host     string
	Port     int
	TLS      bool
	Password string
}

func (h *Handlers) proxyEndpointInfo(r *http.Request, settings map[string]string) proxyEndpoint {
	port := 7843
	if v, ok := settings[SettingProxyPort]; ok {
		fmt.Sscanf(v, "%d", &port)
	}
	// 优先用设置里的「代理对外地址」（服务器真实 IP 或灰云域名）。面板域名常经
	// Cloudflare / nginx 只反代面板端口，套上代理端口会连不通，所以不能直接用它。
	host := strings.TrimSpace(settings[SettingProxyPublicHost])
	if host == "" {
		host = requestHost(r)
	}
	return proxyEndpoint{
		Host:     host,
		Port:     port,
		TLS:      settings[SettingProxyTLS] != "off",
		Password: settings[SettingProxyPassword],
	}
}

// collectActiveExports 汇总当前可用（隧道在跑、有出口 IP）的固定代理槽位，
// 供导出接口和免登录订阅接口共用。
func (h *Handlers) collectActiveExports(r *http.Request) ([]*proxyExport, error) {
	slots, err := h.DB.ListProxySlots()
	if err != nil {
		return nil, err
	}
	settings, _ := h.DB.AllSettings()
	ep := h.proxyEndpointInfo(r, settings)
	proxyPort := ep.Port

	host := ep.Host
	// Clash Verge / OpenClash 会用自己的 DNS 配置覆盖订阅顶层的 dns 段。若节点的
	// server 仍是域名，覆盖后可能在连接代理之前就报 dns resolve failed。订阅由
	// ProxyForge 主机先解析一次公网域名并写入直连 IP，从根上移除客户端侧 DNS 依赖；
	// 原域名仍保留给 TLS SNI 和普通文本导出。解析失败时才回退到域名 + DNS 段。
	clashHost := resolveProxyDialHost(host)
	// TLS 默认开启且服务端拒绝明文。使用内存自签证书时导出 skip-cert-verify；
	// 让客户端把 CONNECT 主机名藏进加密流，避开审查中间盒基于主机名的连接重置。
	proxyTLS := ep.TLS
	var active []*proxyExport
	running := tagSet(h.Scheduler.RunningTags())
	for _, s := range slots {
		if s.Status != "active" || s.AccountTag == "" || s.AccountStatus != "active" || s.PublicIP == "" || !running[s.AccountTag] {
			continue
		}
		active = append(active, &proxyExport{
			Name:          s.Username,
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
			ProxyDialHost: clashHost,
			ProxyPort:     proxyPort,
			TLS:           proxyTLS,
		})
	}

	// 追加在线的远程 agent 节点，作为独立地区节点。它们共用同一个代理端口，
	// 靠 node-<id> 用户名在 resolve 里被解析成对应 agent 出口；代理密码是全局
	// 共享密码（与 stable/random 一致）。地区节点没有跨地区兜底——离线即从订阅
	// 消失，由客户端的自动选择/故障转移组切到别的地区。
	active = append(active, h.collectAgentExports(host, clashHost, proxyPort, proxyTLS, agentProxyPassword(settings[SettingProxyPassword]))...)
	return active, nil
}

const proxyHostResolveTimeout = 3 * time.Second

// resolveProxyDialHost 为 Clash/Mihomo 节点准备不依赖客户端 DNS 的连接地址。
// IP 原样返回；域名优先解析 IPv4（当前订阅和默认监听均以 IPv4 为主）。公网 DNS
// 暂时失败时保留原域名，writeClashDNS 仍会输出兼容性兜底。
func resolveProxyDialHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	ctx, cancel := context.WithTimeout(context.Background(), proxyHostResolveTimeout)
	defer cancel()
	return resolveProxyDialHostWith(ctx, host, net.DefaultResolver.LookupIP)
}

type proxyHostLookup func(context.Context, string, string) ([]net.IP, error)

func resolveProxyDialHostWith(ctx context.Context, host string, lookup proxyHostLookup) string {
	addrs, err := lookup(ctx, "ip4", host)
	if err != nil {
		return host
	}
	for _, addr := range addrs {
		if v4 := addr.To4(); v4 != nil && !v4.IsUnspecified() {
			return v4.String()
		}
	}
	return host
}

// defaultAgentProxyPassword 是全局代理密码为空时，agent 出口对外使用的占位密码。
// resolve 对 node-<id> 在全局密码为空时接受任意密码，所以占位值一定能通过鉴权。
const defaultAgentProxyPassword = "proxyforge"

// agentProxyPassword 返回 agent 出口对外（Clash 订阅 / 单条复制链接）使用的密码。
// agent 出口鉴权走全局代理密码；但全局密码为空时，导出会写成空的 password 字段，
// 部分客户端（含 Clash / Mihomo）会因此拒绝或连不上。此时回退到非空占位密码，
// 保证客户端总能带上非空凭据；因全局密码为空，resolve 对 node-<id> 放行任意密码。
func agentProxyPassword(globalPassword string) string {
	if strings.TrimSpace(globalPassword) != "" {
		return globalPassword
	}
	return defaultAgentProxyPassword
}

// collectAgentExports 把当前在线且启用的远程 agent 转成导出节点。名字按地区取，
// 同地区多个节点追加短后缀去重，保证 Clash proxy-group 成员引用不冲突。
func (h *Handlers) collectAgentExports(host, clashHost string, proxyPort int, proxyTLS bool, proxyPassword string) []*proxyExport {
	if h.Hub == nil {
		return nil
	}
	online := map[string]bool{}
	for _, o := range h.Hub.Snapshot() {
		online[o.NodeID] = true
	}
	nodes, err := h.DB.ListAgentNodes()
	if err != nil {
		return nil
	}

	usedNames := map[string]int{}
	var out []*proxyExport
	for _, n := range nodes {
		if !n.Enabled || !online[n.NodeID] {
			continue
		}
		label := agentDisplayName(n.Name, n.Country, n.NodeID)
		// 同名去重：第二个及以后追加 nodeID 短前缀，避免 Clash 节点名冲突。
		if usedNames[label] > 0 {
			short := n.NodeID
			if len(short) > 4 {
				short = short[:4]
			}
			label = fmt.Sprintf("%s-%s", label, short)
		}
		usedNames[label]++

		out = append(out, &proxyExport{
			Name:          label,
			Username:      proxy.AgentUsername(n.NodeID),
			Password:      proxyPassword,
			PublicIP:      n.PublicIP,
			Country:       n.Country,
			ProxyHost:     host,
			ProxyDialHost: clashHost,
			ProxyPort:     proxyPort,
			TLS:           proxyTLS,
			IsAgent:       true,
		})
	}
	return out
}

type proxyExport struct {
	Name          string // Clash 节点显示名：固定槽位用用户名，agent 用地区标签
	Username      string // 代理鉴权用户名：固定槽位用槽位名，agent 用 node-<id>
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
	ProxyDialHost string // Clash 实际拨号地址；优先为服务端预解析的 IP
	ProxyPort     int
	TLS           bool
	IsAgent       bool
}

// NodeName 返回 Clash 里的节点显示名。优先用 Name，兜底回退到 Username，
// 避免空名字生成非法 YAML。
func (p *proxyExport) NodeName() string {
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return p.Username
}

func (p *proxyExport) ClashServer() string {
	if strings.TrimSpace(p.ProxyDialHost) != "" {
		return p.ProxyDialHost
	}
	return p.ProxyHost
}

// clashScalar 把节点名安全地序列化成 YAML 标量。agent 节点名含空格 / CJK / emoji，
// 直接裸写在部分 Clash 实现里会解析出错，统一用双引号包裹并转义内部引号与反斜杠。
func clashScalar(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
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
		fmt.Fprintf(w, "%s:%s@%s  (%s / %s / %s / score %s)\n",
			p.Username, p.Password, net.JoinHostPort(p.ProxyHost, strconv.Itoa(p.ProxyPort)), tag, ip, country, score)
	}
}

func writeClash(w http.ResponseWriter, list []*proxyExport) {
	fmt.Fprintf(w, "# ProxyForge - Clash 固定代理账号\n")
	writeClashDNS(w, list)
	fmt.Fprintf(w, "proxies:\n")
	for _, p := range list {
		writeClashProxy(w, p)
	}

	writeClashGroups(w, list)
	writeClashRules(w)
}

// proxyServerDomains 收集导出节点里所有「域名形式」的代理服务器地址（去重、稳定排序）。
// IP 形式的 server 客户端直接 TCP 连、无需 DNS，故跳过。返回为空表示所有节点都用 IP，
// 不需要为解析代理域名做任何 DNS 特殊处理。
func proxyServerDomains(list []*proxyExport) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range list {
		host := strings.TrimSpace(p.ClashServer())
		if host == "" || net.ParseIP(host) != nil {
			continue // 空或 IP 无需解析
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

// writeClashDNS 在代理服务器用域名时输出一个 DNS 段，专治 OpenClash / mihomo 的 fake-ip
// 死锁：默认 fake-ip 模式会把代理服务器域名也解析成不可路由的 198.18.x 假 IP，或让解析
// 依赖尚未连上的代理，导致「dns resolve failed」。这里把代理域名加入 fake-ip-filter（返回
// 真实 IP），并用 nameserver-policy 强制它们走明文公共 DNS，绕开任何进代理的解析回环。
// 所有节点都用 IP 时不输出 DNS 段，保持配置最小、不干扰用户既有 DNS 设置。
func writeClashDNS(w http.ResponseWriter, list []*proxyExport) {
	domains := proxyServerDomains(list)
	if len(domains) == 0 {
		return
	}
	// 明文公共 DNS：解析代理域名时直连这些服务器，不经过代理，避免先有鸡还是先有蛋。
	resolvers := []string{"223.5.5.5", "119.29.29.29", "1.1.1.1", "8.8.8.8"}

	fmt.Fprintf(w, "dns:\n")
	fmt.Fprintf(w, "  enable: true\n")
	fmt.Fprintf(w, "  ipv6: false\n")
	fmt.Fprintf(w, "  enhanced-mode: fake-ip\n")
	fmt.Fprintf(w, "  fake-ip-range: 198.18.0.1/16\n")
	fmt.Fprintf(w, "  fake-ip-filter:\n")
	for _, d := range domains {
		fmt.Fprintf(w, "    - %s\n", clashScalar(d))
	}
	fmt.Fprintf(w, "  default-nameserver:\n")
	for _, r := range resolvers {
		fmt.Fprintf(w, "    - %s\n", r)
	}
	fmt.Fprintf(w, "  nameserver:\n")
	for _, r := range resolvers {
		fmt.Fprintf(w, "    - %s\n", r)
	}
	fmt.Fprintf(w, "  proxy-server-nameserver:\n")
	for _, r := range resolvers {
		fmt.Fprintf(w, "    - %s\n", r)
	}
	// nameserver-policy 把代理域名钉死在明文公共 DNS 上，即便用户改了上游也不会把
	// 代理域名的解析导进代理，从根上断掉解析回环。
	fmt.Fprintf(w, "  nameserver-policy:\n")
	for _, d := range domains {
		fmt.Fprintf(w, "    %s:\n", clashScalar(d))
		for _, r := range resolvers {
			fmt.Fprintf(w, "      - %s\n", r)
		}
	}
	fmt.Fprintf(w, "\n")
}

// writeClashGroups 输出代理组。除了让用户手动挑节点的 PROXYFORGE 外，还提供三个自动组：
//   - 「🔒 会话稳定」(consistent-hashing)：同一目标域名稳定落到同一节点，节点故障才重映射；
//   - 「♻️ 自动选择」(url-test)：定时对所有节点测速，自动选中延迟最低的一个；
//   - 「🔀 故障转移」(fallback)：按顺序使用节点，当前节点健康检查失败时自动切到下一个。
//
// PROXYFORGE 的第一个成员是「会话稳定」，Clash 里 select 组默认选中首项。这样
// Codex/Claude 等长连接客户端不必硬编码某个 pf 编号，也不会因 url-test 重选最快节点
// 而在会话中途更换出口 IP；普通用户仍可切到自动选择、故障转移或任一固定节点。
func writeClashGroups(w http.ResponseWriter, list []*proxyExport) {
	fmt.Fprintf(w, "\nproxy-groups:\n")

	// 没有可用节点时写一个指向 DIRECT 的最小组：Clash 不接受成员为空的 proxy-group，
	// 空组会导致整份订阅加载失败，这里退化成直连保证 YAML 合法。
	if len(list) == 0 {
		fmt.Fprintf(w, "  - name: PROXYFORGE\n")
		fmt.Fprintf(w, "    type: select\n")
		fmt.Fprintf(w, "    proxies:\n")
		fmt.Fprintf(w, "      - DIRECT\n")
		return
	}

	// gstatic 的 generate_204 是各家 Clash 通用的连通性探测地址，返回 204 且体积极小。
	// 使用 HTTPS，避免 HTTP HEAD 被出口或中间网络劫持/复用异常；mihomo 也明确建议
	// provider 和 group 的健康检查使用 HTTPS。
	const healthURL = "https://www.gstatic.com/generate_204"

	fmt.Fprintf(w, "  - name: 🔒 会话稳定\n")
	fmt.Fprintf(w, "    type: load-balance\n")
	fmt.Fprintf(w, "    strategy: consistent-hashing\n")
	fmt.Fprintf(w, "    url: %s\n", healthURL)
	fmt.Fprintf(w, "    interval: 300\n")
	fmt.Fprintf(w, "    proxies:\n")
	for _, p := range list {
		fmt.Fprintf(w, "      - %s\n", clashScalar(p.NodeName()))
	}

	fmt.Fprintf(w, "  - name: ♻️ 自动选择\n")
	fmt.Fprintf(w, "    type: url-test\n")
	fmt.Fprintf(w, "    url: %s\n", healthURL)
	fmt.Fprintf(w, "    interval: 300\n")
	fmt.Fprintf(w, "    tolerance: 50\n")
	fmt.Fprintf(w, "    proxies:\n")
	for _, p := range list {
		fmt.Fprintf(w, "      - %s\n", clashScalar(p.NodeName()))
	}

	fmt.Fprintf(w, "  - name: 🔀 故障转移\n")
	fmt.Fprintf(w, "    type: fallback\n")
	fmt.Fprintf(w, "    url: %s\n", healthURL)
	fmt.Fprintf(w, "    interval: 300\n")
	fmt.Fprintf(w, "    proxies:\n")
	for _, p := range list {
		fmt.Fprintf(w, "      - %s\n", clashScalar(p.NodeName()))
	}

	fmt.Fprintf(w, "  - name: PROXYFORGE\n")
	fmt.Fprintf(w, "    type: select\n")
	fmt.Fprintf(w, "    proxies:\n")
	fmt.Fprintf(w, "      - 🔒 会话稳定\n")
	fmt.Fprintf(w, "      - ♻️ 自动选择\n")
	fmt.Fprintf(w, "      - 🔀 故障转移\n")
	for _, p := range list {
		fmt.Fprintf(w, "      - %s\n", clashScalar(p.NodeName()))
	}
}

// writeClashRules 输出规则段。没有 rules 时 Clash 规则模式下所有连接都无处匹配、
// fallthrough 成直连，代理形同虚设（全局模式绕过规则所以照常工作）。这里让
// 内网/回环直连，其余全部经 PROXYFORGE，并以 MATCH 兜底保证规则模式可用。
func writeClashRules(w http.ResponseWriter) {
	rules := []string{
		// ProxyForge 默认只建立 IPv4 WARP 隧道。Windows NCSI 会密集请求这两个纯 IPv6
		// 域名来判断 IPv6 可用性；让请求进入代理只会得到 502，并诱发 url-test 对所有
		// 节点做无意义的健康检查。明确拒绝后 Windows 会正确判定“无 IPv6”，不影响 IPv4。
		"DOMAIN,ipv6.msftconnecttest.com,REJECT",
		"DOMAIN,ipv6.msftncsi.com,REJECT",
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
	fmt.Fprintf(w, "  - name: %s\n", clashScalar(p.NodeName()))
	fmt.Fprintf(w, "    type: http\n")
	fmt.Fprintf(w, "    server: %s\n", clashScalar(p.ClashServer()))
	fmt.Fprintf(w, "    port: %d\n", p.ProxyPort)
	fmt.Fprintf(w, "    username: %s\n", clashScalar(p.Username))
	fmt.Fprintf(w, "    password: %s\n", clashScalar(p.Password))
	// TLS 开启时，客户端对「客户端↔代理」这一跳套 TLS。配置可信证书文件后客户端
	// 正常校验证书；仅自签兼容模式需要 skip-cert-verify。
	if p.TLS {
		fmt.Fprintf(w, "    tls: true\n")
		if !trustedProxyTLSConfigured() {
			fmt.Fprintf(w, "    skip-cert-verify: true\n")
		}
		if p.ProxyHost != "" {
			fmt.Fprintf(w, "    sni: %s\n", clashScalar(p.ProxyHost))
		}
	}
}

func trustedProxyTLSConfigured() bool {
	return strings.TrimSpace(os.Getenv("PROXY_TLS_CERT_FILE")) != "" &&
		strings.TrimSpace(os.Getenv("PROXY_TLS_KEY_FILE")) != ""
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
