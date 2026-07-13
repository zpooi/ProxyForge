// Command pfagent 是 ProxyForge 的极轻量远程出口 agent。
//
// 它部署在任意一台 VPS 上，主动拨号连回主控（wss，走主控现有端口、穿 NAT、
// 本机零入站端口），在长连接上跑 yamux。agent 在 VPS 本地维护三条 WARP 隧道，
// 主控需要经该地区出口时就开一条流，由对应 WARP 隧道拨号并双向转发。
//
// agent 不带数据库和管理端，只持久化 WARP 凭据与稳定 NodeID；默认三条 WARP
// 出口分别建立轻量控制连接，仍无需开放任何 VPS 入站端口。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/zpooi/ProxyForge/backend/internal/agentproto"
	"github.com/zpooi/ProxyForge/backend/internal/proxy"
	"github.com/zpooi/ProxyForge/backend/internal/warp"
)

const (
	// 拨号目标时的本地建立超时。
	localDialTimeout = 10 * time.Second
	// 重连退避区间：断线后从 minBackoff 起指数增长到 maxBackoff。
	minBackoff       = 2 * time.Second
	maxBackoff       = 60 * time.Second
	defaultWarpCount = 3
	maxWarpCount     = 8
	warpInitAttempts = 4
	// cloudflare trace 用于探测 WARP 公网出口 IP / 国家 / colo。
	traceURL     = "https://www.cloudflare.com/cdn-cgi/trace"
	traceTimeout = 6 * time.Second
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[pfagent] ")

	var (
		server    = flag.String("server", envOr("PF_SERVER", ""), "主控地址，如 https://panel.example.com（必填）")
		token     = flag.String("token", envOr("PF_TOKEN", ""), "准入 token（必填）")
		name      = flag.String("name", envOr("PF_NODE_NAME", ""), "节点展示名，默认取地区")
		state     = flag.String("state", envOr("PF_STATE", defaultStatePath()), "NodeID 持久化文件路径")
		warpCount = flag.Int("warp-count", envInt("PF_WARP_COUNT", defaultWarpCount), "本机维护的 WARP 出口数量")
	)
	flag.Parse()

	if strings.TrimSpace(*server) == "" || strings.TrimSpace(*token) == "" {
		log.Fatal("必须提供 -server 和 -token（或环境变量 PF_SERVER / PF_TOKEN）")
	}

	linkURL, err := buildLinkURL(*server)
	if err != nil {
		log.Fatalf("解析主控地址失败: %v", err)
	}

	nodeID, err := ensureNodeID(*state)
	if err != nil {
		log.Fatalf("初始化 NodeID 失败: %v", err)
	}
	log.Printf("节点 ID: %s", nodeID)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	directDialer := &net.Dialer{Timeout: traceTimeout}
	hostMeta := probeMeta(ctx, directDialer.DialContext)
	log.Printf("Agent 主机: %s / %s / %s", hostMeta.ip, hostMeta.country, hostMeta.colo)
	if *warpCount < 1 {
		*warpCount = defaultWarpCount
	}
	if *warpCount > maxWarpCount {
		*warpCount = maxWarpCount
	}

	egresses, err := ensureWarpEgresses(ctx, *state, nodeID, *name, *warpCount)
	if err != nil {
		log.Fatalf("初始化 WARP 出口失败: %v", err)
	}
	defer func() {
		for _, egress := range egresses {
			egress.tunnel.Close()
		}
	}()
	log.Printf("WARP 出口就绪: %d/%d", len(egresses), *warpCount)

	var wg sync.WaitGroup
	for _, egress := range egresses {
		egress := egress
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLoop(ctx, linkURL, *token, egress, hostMeta)
		}()
	}
	wg.Wait()
	log.Println("已退出")
}

// runLoop 反复连接主控，断线后指数退避重连，直到收到退出信号。
func runLoop(ctx context.Context, linkURL, token string, egress *warpEgress, hostMeta egressMeta) {
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		if err := connectOnce(ctx, linkURL, token, egress, hostMeta); err != nil && ctx.Err() == nil {
			log.Printf("出口 %d 连接结束: %v", egress.index+1, err)
		}
		// 连接维持超过 1 分钟视为一次健康会话，重置退避。
		if time.Since(start) > time.Minute {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// connectOnce 建立一次 wss 连接并服务其上的所有 yamux 流，直到连接断开。
func connectOnce(ctx context.Context, linkURL, token string, egress *warpEgress, hostMeta egressMeta) error {
	// 每次重连都通过对应 WARP 隧道重新探测出口信息，主控据此刷新节点。
	meta := probeMeta(ctx, egress.tunnel.DialContext)
	full := linkURL + "?" + buildQuery(egress, meta, hostMeta).Encode()

	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dialCtx, full, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	if err != nil {
		return fmt.Errorf("wss 拨号失败: %w", err)
	}
	// websocket.NetConn 把连接适配成 net.Conn 供 yamux 使用；用二进制消息类型。
	netConn := websocket.NetConn(context.Background(), c, websocket.MessageBinary)

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.LogOutput = io.Discard
	session, err := yamux.Client(netConn, cfg)
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, "yamux client failed")
		return fmt.Errorf("yamux client: %w", err)
	}
	defer session.Close()

	log.Printf("WARP %d 已连接主控（出口 IP %s / %s / %s）", egress.index+1, meta.ip, meta.country, meta.colo)

	// 主控是 yamux 服务端，会 OpenStream；agent 侧循环 Accept 并服务。
	go func() {
		<-ctx.Done()
		_ = session.Close()
	}()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept stream: %w", err)
		}
		go serveStream(stream, egress.tunnel.DialContext)
	}
}

// serveStream 处理主控开出的一条流：读目标地址 → WARP 拨号 → 回状态 → 双向转发。
func serveStream(stream *yamux.Stream, dial func(context.Context, string, string) (net.Conn, error)) {
	defer stream.Close()

	_ = stream.SetDeadline(time.Now().Add(localDialTimeout))
	target, err := agentproto.ReadTarget(stream)
	if err != nil {
		return
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), localDialTimeout)
	remote, err := dial(dialCtx, "tcp", target)
	cancel()
	if err != nil {
		_ = agentproto.WriteStatus(stream, false)
		return
	}
	defer remote.Close()

	if err := agentproto.WriteStatus(stream, true); err != nil {
		return
	}
	// 握手完成，转发阶段不限时。
	_ = stream.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, stream)
		if tc, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stream, remote)
		_ = stream.Close()
		done <- struct{}{}
	}()
	<-done
	<-done
}

func ensureWarpEgresses(ctx context.Context, statePath, baseNodeID, baseName string, count int) ([]*warpEgress, error) {
	profilesPath := filepath.Join(filepath.Dir(statePath), "warp-profiles.json")
	profiles, err := loadWarpProfiles(profilesPath)
	if err != nil {
		return nil, err
	}
	client := warp.NewClient()
	if len(profiles) < count {
		profiles = append(profiles, make([]warp.Account, count-len(profiles))...)
	}

	egresses := make([]*warpEgress, 0, count)
	usedIPs := map[string]bool{}
	for i := 0; i < count; i++ {
		var tunnel *proxy.Tunnel
		var profile warp.Account
		var meta egressMeta
		var lastErr error
		for attempt := 0; attempt < warpInitAttempts && tunnel == nil; attempt++ {
			if attempt > 0 {
				wait := time.Duration(1<<attempt) * time.Second
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
			}
			profile = profiles[i]
			if attempt > 0 || !warpProfileReady(profile) {
				profile, lastErr = registerWarpProfile(ctx, client, baseNodeID, i)
				if lastErr != nil {
					continue
				}
				profiles[i] = profile
				if err := saveWarpProfiles(profilesPath, profiles); err != nil {
					return nil, err
				}
			}
			candidate, err := proxy.NewTunnel(warpTunnelConfig(profile, fmt.Sprintf("agent-%s-%d", baseNodeID, i+1)))
			if err != nil {
				lastErr = err
				continue
			}
			candidateMeta := probeMeta(ctx, candidate.DialContext)
			if candidateMeta.ip != "" && usedIPs[candidateMeta.ip] && attempt < warpInitAttempts-1 {
				lastErr = fmt.Errorf("WARP 出口 IP %s 重复", candidateMeta.ip)
				candidate.Close()
				continue
			}
			tunnel = candidate
			meta = candidateMeta
		}
		if tunnel == nil {
			log.Printf("WARP 出口 %d 初始化失败: %v", i+1, lastErr)
			continue
		}
		if meta.ip != "" {
			if usedIPs[meta.ip] {
				log.Printf("WARP 出口 %d 与现有出口共用 IP %s", i+1, meta.ip)
			}
			usedIPs[meta.ip] = true
		}
		name := warpNodeName(baseName, meta.country, i)
		egresses = append(egresses, &warpEgress{
			index:     i,
			agentID:   baseNodeID,
			agentName: baseName,
			nodeID:    warpNodeID(baseNodeID, i),
			name:      name,
			tunnel:    tunnel,
		})
		log.Printf("WARP 出口 %d 就绪: %s / %s / %s", i+1, meta.ip, meta.country, meta.colo)
	}
	if len(egresses) == 0 {
		return nil, fmt.Errorf("没有可用的 WARP 出口")
	}
	return egresses, nil
}

func registerWarpProfile(ctx context.Context, client *warp.Client, baseNodeID string, index int) (warp.Account, error) {
	account, err := client.Register(ctx)
	if err != nil {
		return warp.Account{}, err
	}
	masque, err := client.EnrollMasque(ctx, account.DeviceID, account.AccessToken, fmt.Sprintf("%s-warp-%d", baseNodeID, index+1))
	if err != nil {
		return warp.Account{}, err
	}
	account.MasquePrivateKey = masque.PrivateKey
	account.MasqueEndpointPubKey = masque.EndpointPubKey
	account.MasqueEndpointV4 = masque.EndpointV4
	account.MasqueEndpointV6 = masque.EndpointV6
	account.AddressV4 = masque.AddressV4
	account.AddressV6 = masque.AddressV6
	return *account, nil
}

func warpProfileReady(profile warp.Account) bool {
	return profile.DeviceID != "" && profile.AccessToken != "" &&
		profile.MasquePrivateKey != "" && profile.MasqueEndpointPubKey != "" &&
		(profile.MasqueEndpointV4 != "" || profile.MasqueEndpointV6 != "")
}

func warpTunnelConfig(account warp.Account, tag string) proxy.Config {
	host, port := splitEndpoint(account.EndpointHost)
	return proxy.Config{
		Tag:                  tag,
		PrivateKey:           account.PrivateKey,
		ClientID:             account.ClientID,
		PeerPublicKey:        account.PeerPublicKey,
		LocalAddrV4:          account.AddressV4,
		LocalAddrV6:          account.AddressV6,
		EndpointHost:         host,
		EndpointPort:         port,
		MTU:                  1280,
		TransportMode:        "auto",
		IPFamily:             "ipv4",
		DNSMode:              "system",
		MasquePrivateKey:     account.MasquePrivateKey,
		MasqueEndpointPubKey: account.MasqueEndpointPubKey,
		MasqueEndpointV4:     account.MasqueEndpointV4,
		MasqueEndpointV6:     account.MasqueEndpointV6,
	}
}

func splitEndpoint(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if host, port, err := net.SplitHostPort(raw); err == nil {
		if n, err := strconv.Atoi(port); err == nil && n > 0 {
			return host, n
		}
		return host, 2408
	}
	return strings.Trim(raw, "[]"), 2408
}

func warpNodeID(base string, index int) string {
	if index == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, index+1)
}

func warpNodeName(base, country string, index int) string {
	suffix := fmt.Sprintf(" #%d", index+1)
	if strings.TrimSpace(base) != "" {
		return strings.TrimSpace(base) + suffix
	}
	if strings.TrimSpace(country) != "" {
		return strings.TrimSpace(country) + " WARP" + suffix
	}
	return "WARP" + suffix
}

func loadWarpProfiles(path string) ([]warp.Account, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 WARP 配置失败: %w", err)
	}
	var file warpProfileFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析 WARP 配置失败: %w", err)
	}
	return file.Profiles, nil
}

func saveWarpProfiles(path string, profiles []warp.Account) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建 WARP 配置目录失败: %w", err)
		}
	}
	data, err := json.MarshalIndent(warpProfileFile{Profiles: profiles}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 WARP 配置失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入 WARP 配置失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tmp)
			return fmt.Errorf("替换 WARP 配置失败: %w", err)
		}
		if retryErr := os.Rename(tmp, path); retryErr != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("保存 WARP 配置失败: %w", retryErr)
		}
	}
	return nil
}

// ---------- 元数据探测 ----------

type egressMeta struct {
	ip      string
	country string
	colo    string
}

type warpProfileFile struct {
	Profiles []warp.Account `json:"profiles"`
}

type warpEgress struct {
	index     int
	agentID   string
	agentName string
	nodeID    string
	name      string
	tunnel    *proxy.Tunnel
}

// probeMeta 通过 cloudflare trace 探测指定 WARP 隧道的公网出口信息。失败时返回空字段，
// 不阻断连接——主控仍能把它当作一个（地区未知的）出口节点。
func probeMeta(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) egressMeta {
	c, cancel := context.WithTimeout(ctx, traceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, traceURL, nil)
	if err != nil {
		return egressMeta{}
	}
	req.Header.Set("User-Agent", "ProxyForge-Agent/1.0")
	transport := &http.Transport{DialContext: dial, ForceAttemptHTTP2: false}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return egressMeta{}
	}
	defer resp.Body.Close()

	var m egressMeta
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 16*1024))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "ip":
			m.ip = val
		case "loc":
			m.country = val
		case "colo":
			m.colo = val
		}
	}
	return m
}

func buildQuery(egress *warpEgress, m, host egressMeta) url.Values {
	q := url.Values{}
	q.Set("node_id", egress.nodeID)
	q.Set("agent_id", egress.agentID)
	q.Set("egress_index", strconv.Itoa(egress.index+1))
	q.Set("v", fmt.Sprintf("%d", agentproto.ProtocolVersion))
	if egress.name != "" {
		q.Set("name", egress.name)
	}
	if egress.agentName != "" {
		q.Set("agent_name", egress.agentName)
	}
	if m.ip != "" {
		q.Set("ip", m.ip)
	}
	if m.country != "" {
		q.Set("country", m.country)
	}
	if m.colo != "" {
		q.Set("colo", m.colo)
	}
	if host.ip != "" {
		q.Set("host_ip", host.ip)
	}
	if host.country != "" {
		q.Set("host_country", host.country)
	}
	if host.colo != "" {
		q.Set("host_colo", host.colo)
	}
	return q
}

// buildLinkURL 把用户给的主控地址规整成 ws(s)://host/agent/link。
// http→ws、https→wss；未带 scheme 时默认按 https（wss）处理。
func buildLinkURL(server string) (string, error) {
	server = strings.TrimSpace(server)
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("不支持的 scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/agent/link"
	u.RawQuery = ""
	return u.String(), nil
}

// ---------- NodeID 持久化 ----------

// ensureNodeID 读取（或首次生成并持久化）稳定 NodeID。重连复用同一 ID，
// 主控据此把同一台 VPS 认成同一个节点，而不是每次上线都变新节点。
func ensureNodeID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		// 落盘失败不致命：本次仍用内存 ID，只是重启后会变。
		log.Printf("警告: NodeID 未能持久化到 %s: %v（重启后 ID 会变化）", path, err)
	}
	return id, nil
}

func defaultStatePath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "pfagent", "node_id")
	}
	return "pfagent-node-id"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
