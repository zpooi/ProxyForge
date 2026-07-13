package proxy

import (
	"path/filepath"
	"testing"

	"github.com/zpooi/ProxyForge/backend/internal/db"
)

func TestManagerStopFlushesQueuedUsage(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}

	m := NewManager(database)
	for range 100 {
		m.recordUsage(ProxyUsage{
			ClientIP:   "192.0.2.10",
			Username:   "pf-001",
			AccountTag: "warp-001",
			UpBytes:    10,
			DownBytes:  20,
		})
	}
	m.Stop()
	m.Stop() // Shutdown is intentionally idempotent.
	m.recordUsage(ProxyUsage{ClientIP: "192.0.2.10", UpBytes: 999})

	clients, err := database.ListClientUsage(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1", len(clients))
	}
	got := clients[0]
	if got.TotalUp != 1000 || got.TotalDown != 2000 || got.HitCount != 100 {
		t.Fatalf("unexpected accumulated usage: %#v", got)
	}
}
