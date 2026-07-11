package handlers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/zpooi/ProxyForge/backend/internal/agentdist"
	"github.com/zpooi/ProxyForge/backend/internal/agenthub"
)

// AgentLink 是 agent 反向连接的 WebSocket 端点。agent 从任意 VPS 主动连回，
// 靠 URL 里的准入 token 鉴权；升级成功后交给 agenthub 在其上跑 yamux 会话。
// 这条连接走主控现有端口，所以 VPS 无需开任何入站端口、NAT 后也能用。
func (h *Handlers) AgentLink(w http.ResponseWriter, r *http.Request) {
	if h.Hub == nil {
		http.Error(w, "agent hub not ready", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	if !h.agentTokenValid(q.Get("token")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	nodeID := strings.TrimSpace(q.Get("node_id"))
	if nodeID == "" {
		http.Error(w, "missing node_id", http.StatusBadRequest)
		return
	}

	// 接受 WebSocket 升级。InsecureSkipVerify 关掉 Origin 校验：客户端是我们自己的
	// agent（非浏览器），不存在 CSRF 风险，鉴权靠 token。
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	// 反向隧道是长连接，不能有读超时；用 background context 适配成 net.Conn。
	netConn := websocket.NetConn(context.Background(), c, websocket.MessageBinary)

	meta := agenthub.Meta{
		NodeID:   nodeID,
		Name:     strings.TrimSpace(q.Get("name")),
		PublicIP: strings.TrimSpace(q.Get("ip")),
		Country:  strings.TrimSpace(q.Get("country")),
		Colo:     strings.TrimSpace(q.Get("colo")),
		Version:  strings.TrimSpace(q.Get("v")),
	}

	// Accept 阻塞到会话结束（agent 下线）。
	_ = h.Hub.Accept(netConn, meta)
	_ = c.Close(websocket.StatusNormalClosure, "session ended")
}

// NodesJSON 返回节点列表：本机 WARP 出口 + 所有远程 agent（在线态叠加内存快照）。
func (h *Handlers) NodesJSON(w http.ResponseWriter, r *http.Request) {
	type nodeView struct {
		NodeID    string `json:"node_id"`
		Name      string `json:"name"`
		Kind      string `json:"kind"` // local / agent
		PublicIP  string `json:"public_ip"`
		Country   string `json:"country"`
		Colo      string `json:"colo"`
		Online    bool   `json:"online"`
		Enabled   bool   `json:"enabled"`
		LatencyMs int    `json:"latency_ms"`
		TxBytes   int64  `json:"tx_bytes"`
		RxBytes   int64  `json:"rx_bytes"`
		LastSeen  string `json:"last_seen"`
	}

	var views []nodeView

	// 本机节点：把新加坡 WARP 出口作为一个恒在线的「本机」节点展示，
	// 让用户在一个列表里同时看到本机和各远程 agent。
	views = append(views, nodeView{
		NodeID:   "local",
		Name:     "本机 (WARP)",
		Kind:     "local",
		Online:   h.Scheduler.RunningTunnels() > 0,
		Enabled:  true,
		LastSeen: time.Now().UTC().Format(time.RFC3339),
	})

	// 远程 agent：以 DB 里「见过的节点」为准，叠加 Hub 的实时在线态。
	online := map[string]agenthub.OnlineNode{}
	if h.Hub != nil {
		for _, o := range h.Hub.Snapshot() {
			online[o.NodeID] = o
		}
	}
	nodes, _ := h.DB.ListAgentNodes()
	for _, n := range nodes {
		v := nodeView{
			NodeID:   n.NodeID,
			Name:     agentDisplayName(n.Name, n.Country, n.NodeID),
			Kind:     "agent",
			PublicIP: n.PublicIP,
			Country:  n.Country,
			Colo:     n.Colo,
			Enabled:  n.Enabled,
		}
		if n.LastSeenAt != nil {
			v.LastSeen = n.LastSeenAt.UTC().Format(time.RFC3339)
		}
		if o, ok := online[n.NodeID]; ok {
			v.Online = true
			v.LatencyMs = o.LatencyMs
			v.TxBytes = o.TxBytes
			v.RxBytes = o.RxBytes
			// 在线上报的 IP/地区更新鲜，覆盖库里的旧值。
			if o.Meta.PublicIP != "" {
				v.PublicIP = o.Meta.PublicIP
			}
			if o.Meta.Country != "" {
				v.Country = o.Meta.Country
				v.Name = agentDisplayName(n.Name, o.Meta.Country, n.NodeID)
			}
			if o.Meta.Colo != "" {
				v.Colo = o.Meta.Colo
			}
		}
		views = append(views, v)
	}

	sort.Slice(views, func(i, j int) bool {
		// 本机置顶，其余在线优先、再按名字。
		if views[i].Kind != views[j].Kind {
			return views[i].Kind == "local"
		}
		if views[i].Online != views[j].Online {
			return views[i].Online
		}
		return views[i].Name < views[j].Name
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"nodes": views})
}

// NodeEnroll 返回一行安装命令 + 准入信息，供前端展示复制。首次调用时生成 token。
func (h *Handlers) NodeEnroll(w http.ResponseWriter, r *http.Request) {
	token, err := h.ensureAgentToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	base := h.panelBaseURL(r)
	// 一行安装命令：从主控自身下载安装脚本并执行，脚本再拉对应架构的 agent 二进制。
	// 脚本内联 token 与主控地址，用户粘贴即用，无需手填参数。
	install := fmt.Sprintf("curl -fsSL '%s/agent/install.sh?token=%s' | sudo bash", base, token)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"install_command": install,
		"token":           token,
		"server":          base,
		"has_binary":      agentdist.HasAny(),
	})
}

// NodeDelete 删除一个远程 agent 节点记录。在线会话会在下次重连时重新登记；
// 要彻底移除应先在 VPS 上停掉 agent 服务，再删除记录。
func (h *Handlers) NodeDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.NodeID) == "" {
		http.Error(w, "missing node_id", http.StatusBadRequest)
		return
	}
	if err := h.DB.DeleteAgentNode(body.NodeID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// NodeTokenRotate 轮换准入 token。已注册的在线 agent 不受影响（靠已建立的会话），
// 但用旧 token 的新安装会被拒；用于撤销泄露的安装命令。
func (h *Handlers) NodeTokenRotate(w http.ResponseWriter, r *http.Request) {
	token, err := h.generateAgentToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"token": token})
}

// NodesPage 渲染节点页面（本机 + 远程 agent）。和其他页面一样返回 SPA 外壳，
// 具体内容由前端 Nodes 组件按路由渲染。
func (h *Handlers) NodesPage(w http.ResponseWriter, r *http.Request) {
	h.AppPage(w, r)
}

// AgentInstallScript 是免登录的安装脚本端点，靠 URL token 鉴权。输出一段 bash：
// 探测架构 → 从主控下载对应 agent 二进制 → 装成 systemd 服务常驻自启。
func (h *Handlers) AgentInstallScript(w http.ResponseWriter, r *http.Request) {
	if !h.agentTokenValid(r.URL.Query().Get("token")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token, _, _ := h.DB.GetSetting(SettingAgentToken)
	base := h.panelBaseURL(r)

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	fmt.Fprint(w, agentInstallScript(base, token))
}

// AgentDownload 免登录下发对应架构的 agent 二进制，靠 URL token 鉴权。
func (h *Handlers) AgentDownload(w http.ResponseWriter, r *http.Request) {
	if !h.agentTokenValid(r.URL.Query().Get("token")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	goos := r.URL.Query().Get("os")
	if goos == "" {
		goos = "linux"
	}
	goarch := r.URL.Query().Get("arch")
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	data, name, err := agentdist.Read(goos, goarch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	_, _ = w.Write(data)
}

// ---------- helpers ----------

func (h *Handlers) agentTokenValid(got string) bool {
	want, _, _ := h.DB.GetSetting(SettingAgentToken)
	got = strings.TrimSpace(got)
	return want != "" && got != "" && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func (h *Handlers) ensureAgentToken() (string, error) {
	token, ok, err := h.DB.GetSetting(SettingAgentToken)
	if err != nil {
		return "", err
	}
	if ok && token != "" {
		return token, nil
	}
	return h.generateAgentToken()
}

func (h *Handlers) generateAgentToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := h.DB.SetSetting(SettingAgentToken, token); err != nil {
		return "", err
	}
	return token, nil
}

// panelBaseURL 返回面板对外访问的 base URL（scheme://host），供安装命令拼接。
// 优先用请求里的 Host（用户当前访问的地址通常就是可达的面板地址）。
func (h *Handlers) panelBaseURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	if fp := r.Header.Get("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return scheme + "://" + host
}

func agentDisplayName(name, country, nodeID string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	if strings.TrimSpace(country) != "" {
		return country + " 节点"
	}
	short := nodeID
	if len(short) > 8 {
		short = short[:8]
	}
	return "节点 " + short
}

// agentInstallScript 生成 systemd 安装脚本。极简：下载二进制到 /usr/local/bin，
// 写一个 systemd unit（内联 server/token），enable + start。
func agentInstallScript(base, token string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

# ProxyForge 远程出口 agent 安装脚本。
# 用途：把当前 VPS 变成 ProxyForge 的一个出口节点（用本机原生 IP 出口）。

SERVER='%s'
TOKEN='%s'
BIN=/usr/local/bin/pfagent
# StateDirectory=pfagent 下 systemd 会创建 /var/lib/pfagent 并授权给 DynamicUser，
# NodeID 持久化在这里，重启复用同一节点身份。
STATE=/var/lib/pfagent/node_id

if [ "$(id -u)" -ne 0 ]; then
  echo "请用 root 运行（或 sudo）"; exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构: $(uname -m)"; exit 1 ;;
esac

echo "[pfagent] 下载 agent ($ARCH)..."
curl -fsSL "$SERVER/agent/download?token=$TOKEN&os=linux&arch=$ARCH" -o "$BIN"
chmod +x "$BIN"

echo "[pfagent] 安装 systemd 服务..."
cat >/etc/systemd/system/pfagent.service <<EOF
[Unit]
Description=ProxyForge Egress Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN -server '$SERVER' -token '$TOKEN' -state '$STATE'
Restart=always
RestartSec=5
DynamicUser=yes
StateDirectory=pfagent
AmbientCapabilities=
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now pfagent.service
echo "[pfagent] 完成。查看状态: systemctl status pfagent"
`, base, token)
}
