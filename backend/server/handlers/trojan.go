package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

const (
	TrojanWebSocketRoute  = "/api/v1/connect/{key}"
	trojanWebSocketPrefix = "/api/v1/connect/"
)

func trojanWebSocketPath(token string) string {
	return trojanWebSocketPrefix + url.PathEscape(trojanWebSocketKey(token))
}

// trojanWebSocketKey keeps the actual subscription token out of nginx access
// logs. Possession of this derived path still does not authenticate Trojan;
// every node has a separate protocol credential checked inside the stream.
func trojanWebSocketKey(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("proxyforge/trojan-ws/v1\x00" + token))
	return hex.EncodeToString(digest[:])
}

// TrojanWebSocket terminates the WebSocket camouflage layer used by Clash.
// TLS is terminated by the existing BaoTa/nginx HTTPS virtual host, so the
// carrier-facing connection is an ordinary HTTPS WebSocket on port 443.
func (h *Handlers) TrojanWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.Manager == nil || !h.trojanWebSocketKeyValid(chi.URLParam(r, "key")) {
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

func (h *Handlers) trojanWebSocketKeyValid(got string) bool {
	token, _, err := h.DB.GetSetting(SettingSubscriptionToken)
	want := trojanWebSocketKey(token)
	got = strings.TrimSpace(got)
	return err == nil && want != "" && got != "" &&
		subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func (h *Handlers) subscriptionTokenValid(got string) bool {
	want, _, err := h.DB.GetSetting(SettingSubscriptionToken)
	got = strings.TrimSpace(got)
	return err == nil && want != "" && got != "" &&
		subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
