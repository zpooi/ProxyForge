package handlers

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

const (
	TrojanWebSocketRoute  = "/api/v1/connect/{token}"
	trojanWebSocketPrefix = "/api/v1/connect/"
)

func trojanWebSocketPath(token string) string {
	return trojanWebSocketPrefix + url.PathEscape(strings.TrimSpace(token))
}

// TrojanWebSocket terminates the WebSocket camouflage layer used by Clash.
// TLS is terminated by the existing BaoTa/nginx HTTPS virtual host, so the
// carrier-facing connection is an ordinary HTTPS WebSocket on port 443.
func (h *Handlers) TrojanWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.Manager == nil || !h.subscriptionTokenValid(chi.URLParam(r, "token")) {
		// Keep active probes indistinguishable from an unknown website path.
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	netConn := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	h.Manager.ServeTrojan(netConn, requestClientIP(r))
	_ = netConn.Close()
	_ = c.Close(websocket.StatusNormalClosure, "session ended")
}

func (h *Handlers) subscriptionTokenValid(got string) bool {
	want, _, err := h.DB.GetSetting(SettingSubscriptionToken)
	got = strings.TrimSpace(got)
	return err == nil && want != "" && got != "" &&
		subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
