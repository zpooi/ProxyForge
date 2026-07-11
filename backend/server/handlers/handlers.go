package handlers

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/zpooi/ProxyForge/backend/internal/agenthub"
	"github.com/zpooi/ProxyForge/backend/internal/auth"
	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/scheduler"
)

type Handlers struct {
	DB        *db.DB
	Auth      *auth.Service
	Scheduler *scheduler.Scheduler
	Hub       *agenthub.Hub

	webFS fs.FS
}

func (h *Handlers) Init(webFS fs.FS) error {
	h.webFS = webFS
	return nil
}

func (h *Handlers) WebFileServer() http.Handler {
	fileServer := http.FileServer(http.FS(h.webFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (h *Handlers) AppPage(w http.ResponseWriter, r *http.Request) {
	content, err := fs.ReadFile(h.webFS, "index.html")
	if err != nil {
		http.Error(w, "app not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}
