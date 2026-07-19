package server

import (
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zpooi/ProxyForge/backend"
	"github.com/zpooi/ProxyForge/backend/internal/agenthub"
	"github.com/zpooi/ProxyForge/backend/internal/applog"
	"github.com/zpooi/ProxyForge/backend/internal/auth"
	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/proxy"
	"github.com/zpooi/ProxyForge/backend/internal/scheduler"
	"github.com/zpooi/ProxyForge/backend/server/handlers"
)

type Server struct {
	DB        *db.DB
	Auth      *auth.Service
	Scheduler *scheduler.Scheduler
	Hub       *agenthub.Hub
	Manager   *proxy.Manager
	LogStore  *applog.Store
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(responseHeaders)
	r.Use(limitRequestBody(1 << 20))
	r.Use(newRequestGuard().Middleware)
	r.Use(newAuthRequestGuard().Middleware)

	h := &handlers.Handlers{
		DB:        s.DB,
		Auth:      s.Auth,
		Scheduler: s.Scheduler,
		Hub:       s.Hub,
		Manager:   s.Manager,
		LogStore:  s.LogStore,
	}
	if err := h.Init(backend.Web()); err != nil {
		log.Fatalf("init handlers: %v", err)
	}

	webFiles := h.WebFileServer()
	r.Handle("/assets/*", webFiles)
	r.Handle("/style.css", webFiles)
	r.Get("/setup", h.SetupPage)
	r.Post("/setup", h.SetupSubmit)
	r.Get("/login", h.LoginPage)
	r.Post("/login", h.LoginSubmit)
	r.Get("/logout", h.Logout)

	// 免登录订阅端点，靠 URL token 鉴权，供 Clash 客户端定时同步。
	r.Get("/sub/clash", h.ClashSubscription)
	r.Get("/sub/share", h.ShareSubscription)
	// Clash/Mihomo uses Trojan over this authenticated WebSocket endpoint.
	// nginx terminates the public TLS connection on port 443.
	r.Get(handlers.TrojanWebSocketRoute, h.TrojanWebSocket)

	// 免登录 agent 端点，靠 URL token 鉴权：反向连接、安装脚本、二进制下载。
	// agent 从 VPS 主动连回，无浏览器会话，故不走登录中间件。
	r.Get("/agent/link", h.AgentLink)
	r.Get("/agent/install.sh", h.AgentInstallScript)
	r.Get("/agent/download", h.AgentDownload)

	// 所有受保护路由走 auth middleware
	r.Group(func(r chi.Router) {
		r.Use(s.Auth.Middleware)

		r.Get("/", h.Dashboard)
		r.Get("/accounts", h.AccountsPage)
		r.Get("/settings", h.SettingsPage)
		r.Post("/settings", h.SettingsSave)
		r.Get("/settings/password", h.PasswordPage)
		r.Post("/settings/password", h.PasswordSave)
		r.Get("/nodes", h.NodesPage)
		r.Get("/ippool", redirectHome)
		r.Get("/traffic", redirectHome)
		r.Get("/logs", h.AppPage)

		// JSON API
		r.Get("/api/accounts/json", h.AccountsJSON)
		r.Get("/api/accounts/{id}/tests", h.AccountTestsJSON)
		r.Get("/api/ippool/json", h.IPPoolJSON)
		r.Get("/api/traffic/json", h.TrafficJSON)
		r.Get("/api/dashboard/json", h.DashboardJSON)
		r.Get("/api/logs/json", h.LogsJSON)
		r.Get("/api/logs/live", h.LiveLogsJSON)
		r.Get("/api/logs/download", h.DownloadLogs)
		r.Get("/api/settings/json", h.SettingsJSON)
		r.Post("/api/settings", h.SettingsSaveJSON)
		r.Get("/api/export", h.ExportProxies)
		r.Get("/api/subscription", h.SubscriptionToken)

		// 节点（本机 + 远程 agent）
		r.Get("/api/nodes/json", h.NodesJSON)
		r.Get("/api/nodes/rotate", h.NodeRotateInfo)
		r.Post("/api/nodes/enroll", h.NodeEnroll)
		r.Post("/api/nodes/delete", h.NodeDelete)
		r.Post("/api/nodes/token/rotate", h.NodeTokenRotate)

		// Actions
		r.Post("/api/accounts/generate", h.AccountsGenerate)
		r.Post("/api/accounts/{id}/enable", h.AccountEnable)
		r.Post("/api/accounts/{id}/disable", h.AccountDisable)
		r.Post("/api/accounts/{id}/retest", h.AccountRetest)
		r.Post("/api/accounts/{id}/delete", h.AccountDelete)
		r.Post("/api/run", h.RunNow)
	})

	// Unknown paths return a real 404 instead of masking mistakes with a
	// dashboard redirect.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	return r
}

// responseHeaders 防止包含代理密码、订阅 token 的管理 API/订阅响应落入浏览器或
// 中间代理缓存，同时为所有页面补上基础浏览器安全边界。静态资源文件名不带内容哈希，
// 使用 no-cache 让客户端可复用但每次重新验证，避免升级后加载旧 JS。
func responseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/style.css" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "no-store, max-age=0")
			w.Header().Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func redirectHome(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusFound)
}
