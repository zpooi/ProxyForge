package server

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zpooi/ProxyForge/backend"
	"github.com/zpooi/ProxyForge/backend/internal/auth"
	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/scheduler"
	"github.com/zpooi/ProxyForge/backend/server/handlers"
)

type Server struct {
	DB        *db.DB
	Auth      *auth.Service
	Scheduler *scheduler.Scheduler
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	h := &handlers.Handlers{
		DB:        s.DB,
		Auth:      s.Auth,
		Scheduler: s.Scheduler,
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

	// 所有受保护路由走 auth middleware
	r.Group(func(r chi.Router) {
		r.Use(s.Auth.Middleware)

		r.Get("/", h.Dashboard)
		r.Get("/accounts", h.AccountsPage)
		r.Get("/settings", h.SettingsPage)
		r.Post("/settings", h.SettingsSave)
		r.Get("/settings/password", h.PasswordPage)
		r.Post("/settings/password", h.PasswordSave)
		r.Get("/ippool", redirectHome)
		r.Get("/traffic", redirectHome)
		r.Get("/logs", redirectHome)

		// JSON API
		r.Get("/api/accounts/json", h.AccountsJSON)
		r.Get("/api/accounts/{id}/tests", h.AccountTestsJSON)
		r.Get("/api/ippool/json", h.IPPoolJSON)
		r.Get("/api/traffic/json", h.TrafficJSON)
		r.Get("/api/dashboard/json", h.DashboardJSON)
		r.Get("/api/logs/json", h.LogsJSON)
		r.Get("/api/settings/json", h.SettingsJSON)
		r.Post("/api/settings", h.SettingsSaveJSON)
		r.Get("/api/export", h.ExportProxies)
		r.Get("/api/subscription", h.SubscriptionToken)

		// Actions
		r.Post("/api/accounts/generate", h.AccountsGenerate)
		r.Post("/api/accounts/{id}/enable", h.AccountEnable)
		r.Post("/api/accounts/{id}/disable", h.AccountDisable)
		r.Post("/api/accounts/{id}/retest", h.AccountRetest)
		r.Post("/api/accounts/{id}/delete", h.AccountDelete)
		r.Post("/api/run", h.RunNow)
	})

	// 兼容旧路径
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})

	return r
}

func redirectHome(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusFound)
}
