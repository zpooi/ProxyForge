package proxy

import (
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
	bindAddr  string
	password  string
	proxyPort int
	transport string
	ipFamily  string
	dnsMode   string
	server    *mixedServer

	lastPicked   map[string]time.Time
	lastPickedIP map[string]time.Time
	pickCount    map[string]int64
	ipPickCount  map[string]int64
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

func (m *Manager) resolve(username, password string) []*Tunnel {
	m.mu.Lock()
	defer m.mu.Unlock()

	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || strings.EqualFold(username, "random") || strings.EqualFold(username, "stable") {
		if m.password != "" && password != m.password {
			return nil
		}
		return m.stableCandidatesLocked()
	}
	if slot, ok := m.slots[username]; ok {
		if password != slot.Password {
			return nil
		}
		t := m.tunnels[slot.AccountTag]
		if t != nil {
			m.notePickLocked(slot.AccountTag)
			return []*Tunnel{t}
		}
		return nil
	}
	if t, ok := m.tunnels[username]; ok {
		if m.password != "" && password != m.password {
			return nil
		}
		m.notePickLocked(username)
		return []*Tunnel{t}
	}
	return nil
}

func (m *Manager) stableCandidatesLocked() []*Tunnel {
	pool := m.rankedStableCandidatesLocked()
	if len(pool) > 0 {
		m.notePickLocked(pool[0].cfg.Tag)
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
		if m.proxyPort <= 0 || m.server.port() != m.proxyPort {
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
		srv, err := startProxy(bindAddr, m.proxyPort, m.resolve, m.recordUsage)
		if err != nil {
			log.Printf("[proxy] start proxy on :%d failed: %v", m.proxyPort, err)
			return
		}
		m.server = srv
		log.Printf("[proxy] proxy listening on :%d (webshare: tag=precise egress, random=stable pool)", m.proxyPort)
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
