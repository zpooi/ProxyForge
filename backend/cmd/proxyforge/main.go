package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/agenthub"
	"github.com/zpooi/ProxyForge/backend/internal/auth"
	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/proxy"
	"github.com/zpooi/ProxyForge/backend/internal/scheduler"
	"github.com/zpooi/ProxyForge/backend/internal/warp"
	"github.com/zpooi/ProxyForge/backend/server"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[ProxyForge] ")

	dbPath := envOr("DB_PATH", "data.db")
	projectRoot := envOr("PROJECT_ROOT", ".")
	listenAddr := envOr("LISTEN_ADDR", ":7800")

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		log.Fatalf("migrate db: %v", err)
	}
	if err := database.SeedDefaultsIfEmpty(projectRoot); err != nil {
		log.Fatalf("seed defaults: %v", err)
	}

	authService := auth.New(database)
	manager := proxy.NewManager(database)
	warpClient := warp.NewClient()

	sched := scheduler.New(database, manager, warpClient)

	// agentHub 管理远程出口 agent 的反向连接。注入到 manager 后，node-<id>
	// 用户名会被解析成对应 agent 出口，与本机 WARP 出口在同一个代理监听器上分发。
	agentHub := agenthub.New(database)
	manager.SetAgentResolver(agentHub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动时在后台对齐一次，避免 WARP 探测阻塞 Web 管理端口监听。
	go func() {
		if err := sched.Reconcile(); err != nil {
			log.Printf("initial reconcile: %v", err)
		}
	}()

	go sched.Run(ctx)

	srv := &server.Server{
		DB:        database,
		Auth:      authService,
		Scheduler: sched,
		Hub:       agentHub,
	}

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on %s (project root: %s, db: %s)", listenAddr, projectRoot, dbPath)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	// 优雅关闭
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")

	cancel()
	manager.Stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
