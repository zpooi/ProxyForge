package handlers

import (
	"encoding/json"
	"net/http"
)

func (h *Handlers) IPPoolJSON(w http.ResponseWriter, r *http.Request) {
	pool, err := h.DB.ListIPPool()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	accounts, _ := h.DB.ListAccounts()
	tagByID := map[int64]string{}
	for _, a := range accounts {
		tagByID[a.ID] = a.Tag
	}
	type ipView struct {
		PublicIP       string `json:"public_ip"`
		KeeperTag      string `json:"keeper_tag"`
		TotalUp        int64  `json:"total_up"`
		TotalDown      int64  `json:"total_down"`
		CurrentUpBps   int64  `json:"current_up_bps"`
		CurrentDownBps int64  `json:"current_down_bps"`
	}
	var views []ipView
	for _, e := range pool {
		keeper := ""
		if e.KeeperAccountID != nil {
			keeper = tagByID[*e.KeeperAccountID]
		}
		views = append(views, ipView{
			PublicIP: e.PublicIP, KeeperTag: keeper,
			TotalUp: e.TotalUp, TotalDown: e.TotalDown,
			CurrentUpBps: e.CurrentUpBps, CurrentDownBps: e.CurrentDownBps,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ip_pool": views})
}
