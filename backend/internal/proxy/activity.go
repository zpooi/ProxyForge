package proxy

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// activityTracker tracks live proxy sessions so the log page can show who is
// using the pool right now, and emits Chinese, GRA-style lines for start/end.
type activityTracker struct {
	seq atomic.Uint64

	mu       sync.Mutex
	live     map[uint64]liveSession
	lastAuth map[string]time.Time
	lastDial map[string]time.Time
	stopCh   chan struct{}
	once     sync.Once
}

type liveSession struct {
	ClientIP string
	Username string
	Target   string
	Egress   string
	Protocol string
	Started  time.Time
}

func newActivityTracker() *activityTracker {
	a := &activityTracker{
		live:     make(map[uint64]liveSession),
		lastAuth: make(map[string]time.Time),
		lastDial: make(map[string]time.Time),
		stopCh:   make(chan struct{}),
	}
	go a.summaryLoop()
	return a
}

func (a *activityTracker) close() {
	if a == nil {
		return
	}
	a.once.Do(func() { close(a.stopCh) })
}

func (a *activityTracker) summaryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.logSummary()
		}
	}
}

func (a *activityTracker) logSummary() {
	if a == nil {
		return
	}
	a.mu.Lock()
	n := len(a.live)
	if n == 0 {
		a.mu.Unlock()
		return
	}
	type row struct {
		client, user, target, egress string
		age                          time.Duration
	}
	rows := make([]row, 0, 5)
	now := time.Now()
	for _, s := range a.live {
		rows = append(rows, row{
			client: s.ClientIP,
			user:   displayUser(s.Username),
			target: shortHost(s.Target),
			egress: displayEgress(s.Egress),
			age:    now.Sub(s.Started).Round(time.Second),
		})
		if len(rows) >= 5 {
			break
		}
	}
	a.mu.Unlock()

	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s/%s→%s(%s,%s)", r.client, r.user, r.target, r.egress, r.age))
	}
	extra := ""
	if n > len(rows) {
		extra = fmt.Sprintf(" · 另有 %d 条", n-len(rows))
	}
	logCN("当前活跃 %d 条 · %s%s", n, strings.Join(parts, " · "), extra)
}

func (a *activityTracker) begin(clientIP, username, target, egress, protocol string) uint64 {
	if a == nil {
		return 0
	}
	id := a.seq.Add(1)
	a.mu.Lock()
	a.live[id] = liveSession{
		ClientIP: clientIP,
		Username: username,
		Target:   target,
		Egress:   egress,
		Protocol: protocol,
		Started:  time.Now(),
	}
	a.mu.Unlock()
	logCN("连接开始 · 客户端 %s · 账号 %s · 协议 %s · 目标 %s · 出口 %s",
		emptyDash(clientIP), displayUser(username), emptyDash(protocol), emptyDash(target), displayEgress(egress))
	return id
}

func (a *activityTracker) end(id uint64, up, down int64) {
	if a == nil || id == 0 {
		return
	}
	a.mu.Lock()
	s, ok := a.live[id]
	if ok {
		delete(a.live, id)
	}
	a.mu.Unlock()
	if !ok {
		return
	}
	elapsed := time.Since(s.Started).Round(time.Second)
	if elapsed < time.Second {
		elapsed = time.Second
	}
	logCN("连接结束 · 客户端 %s · 账号 %s · 目标 %s · 出口 %s · 上行 %s · 下行 %s · 用时 %s",
		emptyDash(s.ClientIP), displayUser(s.Username), emptyDash(s.Target), displayEgress(s.Egress),
		formatDataSize(up), formatDataSize(down), elapsed)
}

func (a *activityTracker) authFail(clientIP, username, protocol, reason string) {
	if a == nil {
		return
	}
	key := clientIP + "|" + protocol
	now := time.Now()
	a.mu.Lock()
	if last, ok := a.lastAuth[key]; ok && now.Sub(last) < 5*time.Second {
		a.mu.Unlock()
		return
	}
	a.lastAuth[key] = now
	if len(a.lastAuth) > 256 {
		for k, t := range a.lastAuth {
			if now.Sub(t) > time.Minute {
				delete(a.lastAuth, k)
			}
		}
	}
	a.mu.Unlock()
	logCN("鉴权失败 · 客户端 %s · 账号 %s · 协议 %s · %s",
		emptyDash(clientIP), displayUser(username), emptyDash(protocol), emptyDash(reason))
}

func (a *activityTracker) dialFail(clientIP, username, target, egress, protocol string, err error) {
	if a == nil || err == nil {
		return
	}
	key := clientIP + "|" + target + "|" + egress
	now := time.Now()
	a.mu.Lock()
	if last, ok := a.lastDial[key]; ok && now.Sub(last) < 3*time.Second {
		a.mu.Unlock()
		return
	}
	a.lastDial[key] = now
	if len(a.lastDial) > 256 {
		for k, t := range a.lastDial {
			if now.Sub(t) > time.Minute {
				delete(a.lastDial, k)
			}
		}
	}
	a.mu.Unlock()
	logCN("拨号失败 · 客户端 %s · 账号 %s · 协议 %s · 目标 %s · 出口 %s · %v",
		emptyDash(clientIP), displayUser(username), emptyDash(protocol), emptyDash(target), displayEgress(egress), err)
}

func logCN(format string, args ...any) {
	log.Printf(format, args...)
}

func displayUser(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return "匿名"
	}
	if strings.EqualFold(username, RotateUsername) {
		return "统一轮换"
	}
	if strings.EqualFold(username, "random") || strings.EqualFold(username, "stable") {
		return "随机池"
	}
	if strings.HasPrefix(username, NodeUsernamePrefix) {
		id := strings.TrimPrefix(username, NodeUsernamePrefix)
		if len(id) > 8 {
			id = id[:8]
		}
		return "节点-" + id
	}
	return username
}

func displayEgress(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "未知出口"
	}
	if strings.HasPrefix(tag, NodeUsernamePrefix) {
		id := strings.TrimPrefix(tag, NodeUsernamePrefix)
		if len(id) > 8 {
			id = id[:8]
		}
		return "Agent-" + id
	}
	return tag
}

func shortHost(target string) string {
	host, _, err := splitHostPortSafe(target)
	if err != nil || host == "" {
		return target
	}
	return host
}

func splitHostPortSafe(hostport string) (string, string, error) {
	if strings.HasPrefix(hostport, "[") {
		if end := strings.Index(hostport, "]:"); end > 0 {
			return hostport[1:end], hostport[end+2:], nil
		}
	}
	if i := strings.LastIndex(hostport, ":"); i > 0 {
		return hostport[:i], hostport[i+1:], nil
	}
	return hostport, "", fmt.Errorf("no port")
}

func emptyDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

func formatDataSize(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}