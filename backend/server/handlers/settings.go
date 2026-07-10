package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (h *Handlers) SettingsPage(w http.ResponseWriter, r *http.Request) {
	h.AppPage(w, r)
}

func (h *Handlers) SettingsJSON(w http.ResponseWriter, r *http.Request) {
	settings, _ := h.DB.AllSettings()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
}

func (h *Handlers) SettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := h.saveSettings(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusFound)
}

func (h *Handlers) SettingsSaveJSON(w http.ResponseWriter, r *http.Request) {
	if err := h.saveSettings(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	settings, _ := h.DB.AllSettings()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "settings": settings})
}

func (h *Handlers) saveSettings(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("bad request")
	}
	keys := []string{
		SettingProxySlotCount,
		SettingTargetAccountCount,
		SettingAutoGeneration,
		SettingProxyPassword,
		SettingProxyListenAddr,
		SettingProxyPort,
		SettingProxyPublicHost,
		SettingWarpTransport,
		SettingTunnelIPFamily,
		SettingProxyDNSMode,
		SettingDedupIntervalSeconds,
	}
	// 这些是文本设置，允许保存空值（用于清空回退到默认行为）。
	allowEmpty := map[string]bool{
		SettingProxyPassword:   true,
		SettingProxyPublicHost: true,
	}
	for _, k := range keys {
		v := r.FormValue(k)
		if v == "" && !allowEmpty[k] {
			continue
		}
		_ = h.DB.SetSetting(k, v)
	}
	if v := r.FormValue(SettingProxySlotCount); v != "" {
		_ = h.DB.SetSetting(SettingTargetAccountCount, v)
	}
	_ = h.Scheduler.Reconcile()
	return nil
}
