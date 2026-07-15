package proxy

import (
	"context"
	"net"
	"time"
)

// NodeUsernamePrefix 是远程 agent 节点在代理协议里的用户名前缀。Clash 订阅里
// 每个 agent 节点用 node-<nodeID> 作为用户名，resolve 见到该前缀就走 agentResolver
// 而不是 WARP 池。导出以便 export/agenthub 复用同一前缀。
const NodeUsernamePrefix = "node-"

// AgentUsername 由节点的稳定 NodeID 拼出代理用户名，导出给 export/agenthub 复用，
// 保证前缀在一处定义。NodeID 由 agent 生成、hub 以它为键索引在线会话。
func AgentUsername(nodeID string) string {
	return NodeUsernamePrefix + nodeID
}

// RotateUsername 是「统一轮换」凭据的用户名。客户端用它（配合共享代理密码）时，
// 服务端在所有逻辑节点之间轮换出口：WARP 池算一个节点，每个在线 agent 各算一个。
// 按客户端 IP 粘滞一个时间窗避免乱飘，窗口到期轮到下一个；全局 round-robin 把新
// 分配均匀铺开，既不挤在同一节点、也不让节点乱飘。选中节点故障时最多尝试三条
// 跨节点/本机备用出口，避免坏目标让一条连接遍历整个池。
const RotateUsername = "auto"

// Egress 是一条可拨号的出口通道。WARP 隧道（*Tunnel）和远程 agent 节点
// （agentEgress）都实现它，代理监听器 dialVia/relay 只依赖这个接口，从而
// 对「本机 WARP 出口」和「远程 Agent WARP 出口」一视同仁地转发、计流量、记失败。
type Egress interface {
	// SupportsUDP reports whether DialContext preserves UDP datagram
	// boundaries. Stream-only remote agents must fail closed here.
	SupportsUDP() bool
	// DialContext 经该出口拨号到 target（host:port）。
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	// Tag 是出口的稳定标识：WARP 用账号 tag，agent 用 node-<id>。用于日志与流量归属。
	Tag() string
	// Kind 是出口类型标签，仅用于日志（wireguard/masque/agent）。
	Kind() string
	// NoteDial 记录拨号耗时，并把非目标类错误标记为待独立健康确认。
	NoteDial(elapsed time.Duration, err error)
	// AddTx/AddRx 累加经该出口上行/下行的字节数。
	AddTx(n int64)
	AddRx(n int64)
}

// 编译期断言：*Tunnel 必须满足 Egress。
var _ Egress = (*Tunnel)(nil)

func (t *Tunnel) Tag() string       { return t.cfg.Tag }
func (t *Tunnel) Kind() string      { return t.transport }
func (t *Tunnel) SupportsUDP() bool { return t != nil && t.tnet != nil }
func (t *Tunnel) AddTx(n int64) {
	if t != nil {
		t.txBytes.Add(n)
	}
}
func (t *Tunnel) AddRx(n int64) {
	if t != nil {
		t.rxBytes.Add(n)
	}
}

// NoteDial 导出 noteDial，满足 Egress 接口。noteDial 内部已判空。
func (t *Tunnel) NoteDial(elapsed time.Duration, err error) { t.noteDial(elapsed, err) }
