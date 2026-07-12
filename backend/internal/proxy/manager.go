package proxy

import (
	"crypto/tls"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/models"
)

type Manager struct {
	db *db.DB

	mu        sync.Mutex
	tunnels   map[string]*Tunnel
	meta      map[string]selectionMeta
	slots     map[string]slotBinding
	bindAddr      string
	password      string
	proxyPort     int
	transport     string
	ipFamily      string
	dnsMode       string
	proxyTLS      bool
	tlsServerName string
	server        *mixedServer
	serverTLS     bool

	lastPicked   map[string]time.Time
	lastPickedIP map[string]time.Time
	pickCount    map[string]int64
	ipPickCount  map[string]int64

	// 统一轮换凭据（auto 用户名）的状态。rotateCursor 是全局 round-robin 游标，
	// 让新分配依次落到不同逻辑节点；rotateSticky 按客户端 IP 记住当前窗口内选中的
	// 节点键，使单个客户端在一个时间窗内固定出口（不乱飘），窗口到期再轮到下一个。
	rotateCursor uint64
	rotateSticky map[string]rotateAssignment

	// agentResolver 把 node-<id> 用户名解析成远程 agent 出口。由 agenthub 注入，
	// 未接入时为 nil。放在 Manager 上是为了让代理监听器的 resolve 能统一分发
	// 本机 WARP 出口和远程 agent 出口。
	agentResolver AgentResolver
}

// rotateAssignment 记录一个客户端在当前时间窗内被粘滞分配到的逻辑节点。
type rotateAssignment struct {
	key      string    // 逻辑节点键："warp" 或 node-<id>
	assigned time.Time // 分配时刻，用于判断粘滞窗口是否过期
}

// AgentResolver 把固定的 agent 节点用户名解析成一个可拨号出口，并能枚举当前在线
// 的所有 agent 出口（供统一轮换凭据在节点间轮转）。由 agenthub.Hub 实现并通过
// SetAgentResolver 注入，避免 proxy 包反向依赖 agenthub。
type AgentResolver interface {
	ResolveEgress(username string) Egress
	// OnlineEgresses 返回当前在线 agent 的出口，按 NodeID 升序稳定排序，
	// 让轮换游标的推进顺序可预测（节点集合不变时顺序不变）。
	OnlineEgresses() []Egress
}

type slotBinding struct {
	Password   string
	AccountTag string
}

type selectionMeta struct {
	PublicIP     string
	Colo         string
	IsKeeper     bool
	LatencyMs    int
	SpeedBps     int
	PacketLoss   float64
	Score        float64
	TrafficBytes int64
	LastTestedAt *time.Time
}

func NewManager(database *db.DB) *Manager {
	return &Manager{
		db:           database,
		tunnels:      make(map[string]*Tunnel),
		meta:         make(map[string]selectionMeta),
		slots:        make(map[string]slotBinding),
		lastPicked:   make(map[string]time.Time),
		lastPickedIP: make(map[string]time.Time),
		pickCount:    make(map[string]int64),
		ipPickCount:  make(map[string]int64),
		rotateSticky: make(map[string]rotateAssignment),
	}
}

func (m *Manager) SetPassword(password string) {
	m.mu.Lock()
	m.password = password
	m.mu.Unlock()
}

func (m *Manager) SetBindAddr(addr string) {
	m.mu.Lock()
	m.bindAddr = addr
	m.mu.Unlock()
}

func (m *Manager) SetProxyPort(port int) {
	m.mu.Lock()
	m.proxyPort = port
	m.mu.Unlock()
}

func (m *Manager) SetTransport(mode string) {
	m.mu.Lock()
	m.transport = mode
	m.mu.Unlock()
}

func (m *Manager) SetIPFamily(family string) {
	m.mu.Lock()
	m.ipFamily = family
	m.mu.Unlock()
}

func (m *Manager) SetDNSMode(mode string) {
	m.mu.Lock()
	m.dnsMode = mode
	m.mu.Unlock()
}

func (m *Manager) SetProxyTLS(enabled bool, serverName string) {
	m.mu.Lock()
	m.proxyTLS = enabled
	m.tlsServerName = serverName
	m.mu.Unlock()
}

// SetAgentResolver 注入远程 agent 节点的解析器。agenthub 启动后调用一次。
func (m *Manager) SetAgentResolver(r AgentResolver) {
	m.mu.Lock()
	m.agentResolver = r
	m.mu.Unlock()
}

func (m *Manager) resolve(username, password, clientIP string) []Egress {
	m.mu.Lock()
	defer m.mu.Unlock()

	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)

	// node-<id> 是远程 agent 节点，出口固定在那台 VPS 上（拿它所在地区的 IP），
	// 所以不接入 WARP 池排序，也不做跨地区兜底——离线就返回空，交给客户端的
	// 自动选择/故障转移组切到别的地区节点。鉴权仍走统一的代理密码。
	if strings.HasPrefix(username, NodeUsernamePrefix) {
		if m.password != "" && password != m.password {
			return nil
		}
		if m.agentResolver == nil {
			return nil
		}
		if eg := m.agentResolver.ResolveEgress(username); eg != nil {
			return []Egress{eg}
		}
		return nil
	}

	// auto 是统一轮换凭据：服务端在「所有逻辑节点」（WARP 池算一个 + 每个在线
	// agent 各算一个）之间轮转。用一个凭据即可自动摊到不同地区/出口，客户端无需
	// 逐个复制节点。选中节点排在候选链首位，其余作为故障转移兜底。
	if strings.EqualFold(username, RotateUsername) {
		if m.password != "" && password != m.password {
			return nil
		}
		return m.rotateCandidatesLocked(clientIP)
	}

	if username == "" || strings.EqualFold(username, "random") || strings.EqualFold(username, "stable") {
		if m.password != "" && password != m.password {
			return nil
		}
		return tunnelsAsEgress(m.stableCandidatesLocked())
	}
	if slot, ok := m.slots[username]; ok {
		if password != slot.Password {
			return nil
		}
		return tunnelsAsEgress(m.slotCandidatesLocked(slot.AccountTag))
	}
	if t, ok := m.tunnels[username]; ok {
		if m.password != "" && password != m.password {
			return nil
		}
		m.notePickLocked(username)
		return []Egress{t}
	}
	return nil
}

// rotateStickyWindow 是统一轮换凭据的粘滞窗口：同一客户端 IP 在窗口内固定用同一
// 逻辑节点（不乱飘），窗口到期后下次分配轮到下一个节点。取 3 分钟与 WARP 池的
// 粘滞衰减量级一致，既能撑住一次浏览会话，又不至于长期钉在一个节点上。
const rotateStickyWindow = 3 * time.Minute

// rotateCandidatesLocked 实现 auto 统一轮换凭据的选路：把 WARP 池当作一个逻辑节点、
// 每个在线 agent 各当一个逻辑节点，按客户端 IP 粘滞地 round-robin 选一个作为主出口，
// 其余节点作为故障转移兜底跟在后面。这样一条链接会自动跑遍不同节点（错开、不挤在
// 一个上），但单个客户端在粘滞窗口内出口稳定（不乱飘），选中节点故障时又能自动转移。
func (m *Manager) rotateCandidatesLocked(clientIP string) []Egress {
	// 收集逻辑节点：warp 排在最前（若有可用隧道），随后是按 NodeID 稳定排序的在线 agent。
	type node struct {
		key   string
		chain []Egress
	}
	var nodes []node
	if warp := tunnelsAsEgress(m.rankedStableCandidatesLocked()); len(warp) > 0 {
		nodes = append(nodes, node{key: "warp", chain: warp})
	}
	if m.agentResolver != nil {
		for _, eg := range m.agentResolver.OnlineEgresses() {
			if eg != nil {
				nodes = append(nodes, node{key: eg.Tag(), chain: []Egress{eg}})
			}
		}
	}
	if len(nodes) == 0 {
		return nil
	}

	idx := map[string]int{}
	for i, n := range nodes {
		idx[n.key] = i
	}

	// 粘滞：窗口内且节点仍在线，就复用上次选中的节点。
	now := time.Now()
	pick := -1
	if clientIP != "" {
		if a, ok := m.rotateSticky[clientIP]; ok && now.Sub(a.assigned) < rotateStickyWindow {
			if i, ok := idx[a.key]; ok {
				pick = i
			}
		}
	}
	// 否则用全局游标 round-robin 取下一个，并刷新该客户端的粘滞分配。
	if pick < 0 {
		pick = int(m.rotateCursor % uint64(len(nodes)))
		m.rotateCursor++
		if clientIP != "" {
			m.rotateSticky[clientIP] = rotateAssignment{key: nodes[pick].key, assigned: now}
		}
		m.pruneRotateStickyLocked(now)
	}

	// 选中节点的候选链在前（本身就带故障转移），其余节点各取首个出口作为跨节点兜底。
	selected := nodes[pick]
	out := make([]Egress, 0, len(selected.chain)+len(nodes)-1)
	out = append(out, selected.chain...)
	if selected.key == "warp" {
		m.notePickLocked(selected.chain[0].Tag())
	}
	for i, n := range nodes {
		if i == pick || len(n.chain) == 0 {
			continue
		}
		out = append(out, n.chain[0])
	}
	return out
}

// pruneRotateStickyLocked 清掉过期的粘滞分配，避免 map 随客户端 IP 无限增长。
func (m *Manager) pruneRotateStickyLocked(now time.Time) {
	for ip, a := range m.rotateSticky {
		if now.Sub(a.assigned) >= rotateStickyWindow {
			delete(m.rotateSticky, ip)
		}
	}
}

// tunnelsAsEgress 把 WARP 隧道候选链转成 []Egress。分开保留 *Tunnel 的内部
// 排序/健康逻辑，只在交给代理监听器的边界处做一次类型提升。
func tunnelsAsEgress(tunnels []*Tunnel) []Egress {
	if len(tunnels) == 0 {
		return nil
	}
	out := make([]Egress, 0, len(tunnels))
	for _, t := range tunnels {
		if t != nil {
			out = append(out, t)
		}
	}
	return out
}

func (m *Manager) stableCandidatesLocked() []*Tunnel {
	pool := m.rankedStableCandidatesLocked()
	if len(pool) > 0 {
		m.notePickLocked(pool[0].cfg.Tag)
	}
	return pool
}

// slotCandidatesLocked 为固定槽位返回一条带故障转移的拨号候选链：绑定的隧道
// 排在最前（保住用户期望的出口 IP），后面跟上其余健康隧道按分数排序作为兜底。
// dialVia 会依次尝试，绑定隧道断掉时自动切到最快的可用隧道，而不是直接失败。
func (m *Manager) slotCandidatesLocked(tag string) []*Tunnel {
	primary := m.tunnels[tag]
	pool := make([]*Tunnel, 0, len(m.tunnels))
	if primary != nil {
		m.notePickLocked(tag)
		pool = append(pool, primary)
	}
	for _, t := range m.rankedStableCandidatesLocked() {
		if t == nil || t.cfg.Tag == tag {
			continue
		}
		pool = append(pool, t)
	}
	return pool
}

func (m *Manager) rankedStableCandidatesLocked() []*Tunnel {
	if len(m.tunnels) == 0 {
		return nil
	}

	pool := m.candidatesLocked(true)
	if len(pool) == 0 {
		pool = m.candidatesLocked(false)
	}
	if len(pool) == 0 {
		return nil
	}

	now := time.Now()
	sort.Slice(pool, func(i, j int) bool {
		si := m.selectionScoreLocked(pool[i], now)
		sj := m.selectionScoreLocked(pool[j], now)
		if si == sj {
			return pool[i].cfg.Tag < pool[j].cfg.Tag
		}
		return si < sj
	})
	return pool
}

func (m *Manager) candidatesLocked(keepersOnly bool) []*Tunnel {
	out := make([]*Tunnel, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		meta := m.meta[t.cfg.Tag]
		if keepersOnly && !meta.IsKeeper {
			continue
		}
		if meta.PacketLoss >= 0.80 {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (m *Manager) selectionScoreLocked(t *Tunnel, now time.Time) float64 {
	meta := m.meta[t.cfg.Tag]

	score := meta.Score
	if score <= 0 {
		score = 3000
	}
	if meta.LatencyMs > 0 {
		score += float64(meta.LatencyMs) * 0.45
	} else {
		score += 800
	}
	if meta.SpeedBps > 0 {
		score += 250000000.0 / float64(meta.SpeedBps)
	} else {
		score += 1200
	}
	score += meta.PacketLoss * 5000
	score += coloPenalty(meta.Colo)
	if meta.IsKeeper {
		score -= 350
	}
	if meta.LastTestedAt == nil {
		score += 900
	} else {
		age := now.Sub(*meta.LastTestedAt)
		if age > 24*time.Hour {
			score += 700
		}
		if age > 72*time.Hour {
			score += 1500
		}
	}

	if meta.TrafficBytes > 0 {
		mb := float64(meta.TrafficBytes) / 1024.0 / 1024.0
		score += math.Log10(mb+1) * 180
	}

	if last, ok := m.lastPicked[t.cfg.Tag]; ok {
		age := now.Sub(last)
		switch {
		case age < 2*time.Minute:
			score -= 900 * (1 - age.Seconds()/(2*time.Minute).Seconds())
		case age < 15*time.Minute:
			score -= 250 * (1 - age.Seconds()/(15*time.Minute).Seconds())
		}
	}
	if meta.PublicIP != "" {
		if last, ok := m.lastPickedIP[meta.PublicIP]; ok {
			age := now.Sub(last)
			switch {
			case age < 2*time.Minute:
				score -= 500 * (1 - age.Seconds()/(2*time.Minute).Seconds())
			case age < 15*time.Minute:
				score -= 150 * (1 - age.Seconds()/(15*time.Minute).Seconds())
			}
		}
		score += float64(m.ipPickCount[meta.PublicIP]) * 5
	}
	score += float64(m.pickCount[t.cfg.Tag]) * 3
	if live := t.lastDialLatencyMs.Load(); live > 0 {
		age := now.Sub(time.Unix(t.lastDialAtUnix.Load(), 0))
		if age < 10*time.Minute {
			score += float64(live) * 1.6
		}
	}
	score += float64(t.dialFailures.Load()) * 3000

	return score
}

func (m *Manager) notePickLocked(tag string) {
	now := time.Now()
	m.lastPicked[tag] = now
	m.pickCount[tag]++
	if meta := m.meta[tag]; meta.PublicIP != "" {
		m.lastPickedIP[meta.PublicIP] = now
		m.ipPickCount[meta.PublicIP]++
	}
}

func (m *Manager) Reconcile() error {
	accounts, err := m.db.ListActiveAccounts()
	if err != nil {
		return fmt.Errorf("list active accounts: %w", err)
	}
	slots, err := m.db.ListProxySlots()
	if err != nil {
		return fmt.Errorf("list proxy slots: %w", err)
	}
	m.mu.Lock()
	transport := m.transport
	ipFamily := m.ipFamily
	dnsMode := m.dnsMode
	m.mu.Unlock()

	desired := make(map[string]Config, len(accounts))
	nextMeta := make(map[string]selectionMeta, len(accounts))
	for _, a := range accounts {
		cfg := configFromAccount(a)
		cfg.TransportMode = transport
		cfg.IPFamily = ipFamily
		cfg.DNSMode = dnsMode
		desired[a.Tag] = cfg
		nextMeta[a.Tag] = metaFromAccount(a)
	}

	nextSlots := make(map[string]slotBinding, len(slots))
	for _, slot := range slots {
		if slot.Status != "active" || slot.AccountTag == "" || slot.AccountStatus != "active" {
			continue
		}
		nextSlots[slot.Username] = slotBinding{Password: slot.Password, AccountTag: slot.AccountTag}
	}

	m.mu.Lock()
	m.meta = nextMeta
	m.slots = nextSlots

	for tag, t := range m.tunnels {
		want, ok := desired[tag]
		if !ok || want != t.cfg {
			log.Printf("[proxy] stopping tunnel %s", tag)
			t.Close()
			delete(m.tunnels, tag)
			delete(m.lastPicked, tag)
			delete(m.pickCount, tag)
		}
	}

	toStart := make(map[string]Config)
	for tag, cfg := range desired {
		if _, ok := m.tunnels[tag]; !ok {
			toStart[tag] = cfg
		}
	}
	m.reconcileServerLocked()
	m.mu.Unlock()

	return m.startTunnels(toStart, desired)
}

func (m *Manager) startTunnels(toStart map[string]Config, desired map[string]Config) error {
	const startupConcurrency = 4
	if len(toStart) == 0 {
		return nil
	}

	type result struct {
		tag string
		cfg Config
		tun *Tunnel
		err error
	}
	sem := make(chan struct{}, startupConcurrency)
	results := make(chan result, len(toStart))
	var wg sync.WaitGroup
	for tag, cfg := range toStart {
		wg.Add(1)
		go func(tag string, cfg Config) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t, err := newTunnel(cfg)
			results <- result{tag: tag, cfg: cfg, tun: t, err: err}
		}(tag, cfg)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for r := range results {
		if r.err != nil {
			log.Printf("[proxy] start tunnel %s failed: %v", r.tag, r.err)
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		m.mu.Lock()
		if current, exists := m.tunnels[r.tag]; exists || desired[r.tag] != r.cfg {
			if current != nil {
				log.Printf("[proxy] tunnel %s already running, discarding duplicate", r.tag)
			}
			m.mu.Unlock()
			r.tun.Close()
			continue
		}
		m.tunnels[r.tag] = r.tun
		m.mu.Unlock()
		log.Printf("[proxy] tunnel %s ready (%s endpoint %s)", r.tag, r.tun.transport, r.tun.endpoint)
	}
	return firstErr
}

func (m *Manager) reconcileServerLocked() {
	if m.server != nil {
		// 端口变化或 TLS 开关变化都需要重启监听（TLS 配置在监听建立时固化）。
		if m.proxyPort <= 0 || m.server.port() != m.proxyPort || m.serverTLS != m.proxyTLS {
			log.Printf("[proxy] stopping proxy on :%d", m.server.port())
			m.server.Close()
			m.server = nil
		}
	}
	if m.proxyPort > 0 && m.server == nil {
		bindAddr := m.bindAddr
		if bindAddr == "" {
			bindAddr = "0.0.0.0"
		}
		var tlsConfig *tls.Config
		if m.proxyTLS {
			cfg, err := newSelfSignedTLSConfig(m.tlsServerName)
			if err != nil {
				log.Printf("[proxy] build TLS config failed, falling back to plaintext: %v", err)
			} else {
				tlsConfig = cfg
			}
		}
		srv, err := startProxy(bindAddr, m.proxyPort, m.resolve, m.recordUsage, tlsConfig)
		if err != nil {
			log.Printf("[proxy] start proxy on :%d failed: %v", m.proxyPort, err)
			return
		}
		m.server = srv
		m.serverTLS = tlsConfig != nil
		if m.serverTLS {
			log.Printf("[proxy] proxy listening on :%d with opportunistic TLS (webshare: tag=precise egress, random=stable pool)", m.proxyPort)
		} else {
			log.Printf("[proxy] proxy listening on :%d (webshare: tag=precise egress, random=stable pool)", m.proxyPort)
		}
	}
}

func (m *Manager) recordUsage(usage ProxyUsage) {
	if usage.ClientIP == "" || (usage.UpBytes <= 0 && usage.DownBytes <= 0) {
		return
	}
	go func() {
		if err := m.db.AddClientUsage(usage.ClientIP, usage.Username, usage.AccountTag, usage.UpBytes, usage.DownBytes); err != nil {
			log.Printf("[proxy] record client usage %s failed: %v", usage.ClientIP, err)
		}
	}()
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server != nil {
		m.server.Close()
		m.server = nil
	}
	for tag, t := range m.tunnels {
		t.Close()
		delete(m.tunnels, tag)
	}
}

func (m *Manager) StopTunnel(tag string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tunnels[tag]
	if t == nil {
		return false
	}
	t.Close()
	delete(m.tunnels, tag)
	delete(m.lastPicked, tag)
	delete(m.pickCount, tag)
	return true
}

// HealthCheck 原地重建持续拨号失败的隧道。相比慢速的换绑账号路径，
// 这里重跑 auto 回退（MASQUE→WireGuard）把同一个账号的隧道拉起来，
// 既能快速自愈，又不会改变用户看到的出口 IP。返回重建成功的隧道数。
func (m *Manager) HealthCheck() int {
	m.mu.Lock()
	type candidate struct {
		tag string
		cfg Config
	}
	var stale []candidate
	for tag, t := range m.tunnels {
		if t == nil {
			continue
		}
		if t.dialFailures.Load() >= tunnelRebuildAfterFailures {
			stale = append(stale, candidate{tag: tag, cfg: t.cfg})
		}
	}
	m.mu.Unlock()

	if len(stale) == 0 {
		return 0
	}

	rebuilt := 0
	for _, c := range stale {
		fresh, err := newTunnel(c.cfg)
		if err != nil {
			log.Printf("[proxy] health rebuild tunnel %s failed: %v", c.tag, err)
			continue
		}
		m.mu.Lock()
		old := m.tunnels[c.tag]
		// 期间账号可能已被 reconcile 换掉，确认还是同一份配置再替换。
		if old == nil || old.cfg != c.cfg {
			m.mu.Unlock()
			fresh.Close()
			continue
		}
		m.tunnels[c.tag] = fresh
		m.mu.Unlock()
		old.Close()
		rebuilt++
		log.Printf("[proxy] health rebuilt tunnel %s (%s endpoint %s)", c.tag, fresh.transport, fresh.endpoint)
	}
	return rebuilt
}

func (m *Manager) ProxyPort() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server == nil {
		return 0
	}
	return m.server.port()
}

func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tunnels)
}

type TrafficDelta struct {
	Tag string
	Tx  int64
	Rx  int64
}

func (m *Manager) Snapshot() []TrafficDelta {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TrafficDelta, 0, len(m.tunnels))
	for tag, t := range m.tunnels {
		out = append(out, TrafficDelta{
			Tag: tag,
			Tx:  t.txBytes.Load(),
			Rx:  t.rxBytes.Load(),
		})
	}
	return out
}

func (m *Manager) RunningTags() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	tags := make([]string, 0, len(m.tunnels))
	for tag := range m.tunnels {
		tags = append(tags, tag)
	}
	return tags
}

func (m *Manager) IsRunning(tag string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.tunnels[tag]
	return ok
}

func configFromAccount(a *models.Account) Config {
	return Config{
		Tag:                  a.Tag,
		PrivateKey:           a.PrivateKey,
		ClientID:             a.ClientID,
		PeerPublicKey:        a.PeerPublicKey,
		LocalAddrV4:          a.LocalAddressV4,
		LocalAddrV6:          a.LocalAddressV6,
		EndpointHost:         a.EndpointHost,
		EndpointPort:         a.EndpointPort,
		MTU:                  a.MTU,
		ListenPort:           a.ListenPort,
		IsKeeper:             a.IsIPKeeper,
		MasquePrivateKey:     a.MasquePrivateKey,
		MasqueEndpointPubKey: a.MasqueEndpointPubKey,
		MasqueEndpointV4:     a.MasqueEndpointV4,
		MasqueEndpointV6:     a.MasqueEndpointV6,
	}
}

func metaFromAccount(a *models.Account) selectionMeta {
	return selectionMeta{
		PublicIP:     a.LastPublicIP,
		Colo:         a.LastColo,
		IsKeeper:     a.IsIPKeeper,
		LatencyMs:    a.LastLatencyMs,
		SpeedBps:     a.LastSpeedBps,
		PacketLoss:   a.LastPacketLoss,
		Score:        a.LastScore,
		TrafficBytes: a.TrafficUp + a.TrafficDown,
		LastTestedAt: a.LastTestedAt,
	}
}

func coloPenalty(colo string) float64 {
	switch strings.ToUpper(strings.TrimSpace(colo)) {
	case "HKG", "TPE", "NRT", "KIX", "ICN":
		return 0
	case "SIN":
		return 120
	case "SJC", "SEA":
		return 450
	case "LAX":
		return 900
	case "DFW", "ORD", "IAD", "EWR", "ATL", "MIA", "DEN", "PHX", "PDX":
		return 1200
	case "":
		return 300
	default:
		return 600
	}
}
