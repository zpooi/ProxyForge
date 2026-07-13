// Package agenthub 管理所有远程 agent 的反向连接。
//
// 每个 agent 从它所在的 VPS 主动拨号连回主控（wss，走现有端口、穿 NAT、
// VPS 零入站端口），在这条长连接上跑 yamux 多路复用。主控作为 yamux 服务端，
// 需要经某个 agent 出口时就 OpenStream，agent 收到后通过 VPS 本地 WARP 隧道
// 拨号目标，于是主控获得对应地区的 WARP 出口，供订阅当作独立地区节点。
//
// Hub 只维护内存态（在线会话 + RTT），持久化的「见过哪些 agent」交给 db。
package agenthub

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/zpooi/ProxyForge/backend/internal/agentproto"
	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/proxy"
)

const (
	// 心跳间隔与超时：主控定期 ping 每个会话测活并测 RTT，连续失败即判离线。
	pingInterval = 15 * time.Second
	pingTimeout  = 8 * time.Second

	// 单条 agent 拨号的建立超时。yamux 流本身很快，主要等 agent 侧本地拨号目标。
	dialTimeout = 10 * time.Second
)

// Meta 是 agent 连接时上报的自描述信息，来自握手 URL 的查询参数。
type Meta struct {
	NodeID       string
	Name         string
	PublicIP     string
	Country      string
	Colo         string
	Version      string
	AgentID      string
	AgentName    string
	HostPublicIP string
	HostCountry  string
	HostColo     string
	EgressIndex  int
}

// Hub 管理在线 agent 会话。它同时实现 proxy.AgentResolver，被注入到 proxy.Manager。
type Hub struct {
	db *db.DB

	mu     sync.Mutex
	agents map[string]*agentConn // key: NodeID
}

var _ proxy.AgentResolver = (*Hub)(nil)

func New(database *db.DB) *Hub {
	return &Hub{
		db:     database,
		agents: make(map[string]*agentConn),
	}
}

// agentConn 是一个在线 agent 的会话状态。
type agentConn struct {
	meta        Meta
	session     *yamux.Session
	connectedAt time.Time
	lastPingMs  atomic.Int64
	txBytes     atomic.Int64
	rxBytes     atomic.Int64
	// 实时速率：pingLoop 每 pingInterval 对累计 tx/rx 差分一次，供页面显示当前吞吐。
	// cur* 用原子读写供查询侧无锁读取；last*/lastSampleAt 只在 pingLoop 单 goroutine 内使用。
	curUpBps     atomic.Int64
	curDownBps   atomic.Int64
	lastTx       int64
	lastRx       int64
	lastSampleAt time.Time
	closeOnce    sync.Once
}

// Accept 接管一条刚建立的 agent 连接（已从 websocket 转成 net.Conn），
// 在其上建立 yamux 服务端会话并注册。它会阻塞到会话结束（连接断开），
// 因此调用方应在 HTTP handler 的 goroutine 里直接调用，返回即代表 agent 下线。
func (h *Hub) Accept(conn net.Conn, meta Meta) error {
	if strings.TrimSpace(meta.NodeID) == "" {
		return fmt.Errorf("agenthub: empty node id")
	}

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = pingInterval
	cfg.ConnectionWriteTimeout = pingTimeout
	cfg.LogOutput = logWriter{}
	session, err := yamux.Server(conn, cfg)
	if err != nil {
		return fmt.Errorf("agenthub: yamux server: %w", err)
	}

	ac := &agentConn{
		meta:         meta,
		session:      session,
		connectedAt:  time.Now(),
		lastSampleAt: time.Now(),
	}

	// 同一 NodeID 重连时踢掉旧会话，保证 map 里始终是最新那条。
	h.mu.Lock()
	if old := h.agents[meta.NodeID]; old != nil {
		old.close()
	}
	h.agents[meta.NodeID] = ac
	h.mu.Unlock()

	if err := h.db.UpsertAgentNode(meta.NodeID, meta.Name, meta.PublicIP, meta.Country, meta.Colo); err != nil {
		log.Printf("[agenthub] upsert node %s failed: %v", meta.NodeID, err)
	}
	log.Printf("[agenthub] agent online: node=%s ip=%s country=%s colo=%s", meta.NodeID, meta.PublicIP, meta.Country, meta.Colo)

	// 阻塞直到会话结束（对端断开或 ping 失败）。
	h.pingLoop(ac)

	h.mu.Lock()
	if h.agents[meta.NodeID] == ac {
		delete(h.agents, meta.NodeID)
	}
	h.mu.Unlock()
	ac.close()
	_ = h.db.TouchAgentNode(meta.NodeID)
	log.Printf("[agenthub] agent offline: node=%s", meta.NodeID)
	return nil
}

// pingLoop 定期 ping 会话测活并记录 RTT，会话关闭或连续 ping 失败时返回。
func (h *Hub) pingLoop(ac *agentConn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ac.session.CloseChan():
			return
		case <-ticker.C:
			rtt, err := ac.session.Ping()
			if err != nil {
				return
			}
			ms := rtt.Milliseconds()
			if ms <= 0 {
				ms = 1
			}
			ac.lastPingMs.Store(ms)
			ac.sampleThroughput(time.Now())
			_ = h.db.TouchAgentNode(ac.meta.NodeID)
		}
	}
}

// ResolveEgress 把 node-<id> 用户名解析成一个可拨号出口。离线或未知返回 nil，
// 由代理监听器交回空候选、触发客户端层的地区节点切换。
func (h *Hub) ResolveEgress(username string) proxy.Egress {
	nodeID := strings.TrimPrefix(username, proxy.NodeUsernamePrefix)
	if nodeID == username {
		return nil // 不是 node- 前缀
	}
	h.mu.Lock()
	ac := h.agents[nodeID]
	h.mu.Unlock()
	if ac == nil {
		return nil
	}
	return &agentEgress{conn: ac}
}

// OnlineEgresses 返回当前所有在线 agent 的出口，按 NodeID 升序稳定排序，
// 供统一轮换凭据（auto）在节点间可预测地轮转。
func (h *Hub) OnlineEgresses() []proxy.Egress {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.agents))
	for id := range h.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]proxy.Egress, 0, len(ids))
	for _, id := range ids {
		out = append(out, &agentEgress{conn: h.agents[id]})
	}
	return out
}

// OnlineNode 是查询用的在线 agent 快照。
type OnlineNode struct {
	NodeID    string
	Meta      Meta
	LatencyMs int
	TxBytes   int64
	RxBytes   int64
	UpBps     int64
	DownBps   int64
	Since     time.Time
}

// Snapshot 返回当前在线 agent 列表。
func (h *Hub) Snapshot() []OnlineNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]OnlineNode, 0, len(h.agents))
	for id, ac := range h.agents {
		out = append(out, OnlineNode{
			NodeID:    id,
			Meta:      ac.meta,
			LatencyMs: int(ac.lastPingMs.Load()),
			TxBytes:   ac.txBytes.Load(),
			RxBytes:   ac.rxBytes.Load(),
			UpBps:     ac.curUpBps.Load(),
			DownBps:   ac.curDownBps.Load(),
			Since:     ac.connectedAt,
		})
	}
	return out
}

// IsOnline 报告某个 NodeID 是否有在线会话。
func (h *Hub) IsOnline(nodeID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.agents[nodeID]
	return ok
}

// sampleThroughput 按距上次采样的时间差，把累计 tx/rx 差分成当前上下行 bps。
// 只在 pingLoop 单 goroutine 内调用，读 last*/lastSampleAt 无需加锁；结果写进
// 原子字段供查询侧无锁读取。首次采样（lastSampleAt 为零值间隔）只记基线不出速率。
func (ac *agentConn) sampleThroughput(now time.Time) {
	tx := ac.txBytes.Load()
	rx := ac.rxBytes.Load()
	elapsed := now.Sub(ac.lastSampleAt).Seconds()
	if elapsed > 0 {
		up := tx - ac.lastTx
		down := rx - ac.lastRx
		if up < 0 {
			up = 0
		}
		if down < 0 {
			down = 0
		}
		ac.curUpBps.Store(int64(float64(up) / elapsed))
		ac.curDownBps.Store(int64(float64(down) / elapsed))
	}
	ac.lastTx = tx
	ac.lastRx = rx
	ac.lastSampleAt = now
}

func (ac *agentConn) close() {
	ac.closeOnce.Do(func() {
		_ = ac.session.Close()
	})
}

// logWriter 把 yamux 的内部日志降级到我们的前缀日志，避免裸 stderr 噪声。
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	log.Printf("[agenthub/yamux] %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// ---------- egress ----------

// agentEgress 让远程 agent 会话满足 proxy.Egress：每次拨号在 yamux 上开一条流，
// 写入目标地址、读回拨号状态，随后这条流本身就是到目标的双向管道。
type agentEgress struct {
	conn *agentConn
}

func (e *agentEgress) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if e == nil || e.conn == nil {
		return nil, fmt.Errorf("agent egress not ready")
	}
	stream, err := e.conn.session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("agent open stream: %w", err)
	}

	deadline := time.Now().Add(dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = stream.SetDeadline(deadline)

	if err := agentproto.WriteTarget(stream, address); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("agent write target: %w", err)
	}
	ok, err := agentproto.ReadStatus(stream)
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("agent read status: %w", err)
	}
	if !ok {
		_ = stream.Close()
		return nil, fmt.Errorf("agent %s failed to dial %s", e.conn.meta.NodeID, address)
	}
	// 握手完成，清掉拨号 deadline，转发阶段不限时。
	_ = stream.SetDeadline(time.Time{})
	return stream, nil
}

func (e *agentEgress) Tag() string  { return proxy.NodeUsernamePrefix + e.conn.meta.NodeID }
func (e *agentEgress) Kind() string { return "agent-warp" }

func (e *agentEgress) NoteDial(elapsed time.Duration, err error) {
	if err == nil && elapsed > 0 {
		e.conn.lastPingMs.Store(elapsed.Milliseconds())
	}
}

func (e *agentEgress) AddTx(n int64) { e.conn.txBytes.Add(n) }
func (e *agentEgress) AddRx(n int64) { e.conn.rxBytes.Add(n) }
