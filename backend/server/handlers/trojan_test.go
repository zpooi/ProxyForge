package handlers

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/proxy"
)

func TestTrojanWebSocketRequiresSubscriptionToken(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := database.SetSetting(SettingSubscriptionToken, "token123"); err != nil {
		t.Fatal(err)
	}

	h := &Handlers{DB: database, Manager: proxy.NewManager(nil)}
	router := chi.NewRouter()
	router.Get(TrojanWebSocketRoute, h.TrojanWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()
	wsBase := "ws" + strings.TrimPrefix(server.URL, "http")

	ctx := context.Background()
	valid, _, err := websocket.Dial(ctx, wsBase+trojanWebSocketPath("token123"), nil)
	if err != nil {
		t.Fatalf("valid Trojan WebSocket token was rejected: %v", err)
	}
	_ = valid.Close(websocket.StatusNormalClosure, "done")

	invalid, resp, err := websocket.Dial(ctx, wsBase+trojanWebSocketPath("wrong"), nil)
	if invalid != nil {
		_ = invalid.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("invalid token response = %#v, err=%v; want hidden 404", resp, err)
	}
	if strings.Contains(trojanWebSocketPath("token123"), "token123") {
		t.Fatal("Trojan WebSocket path leaked the subscription token")
	}
}
