package handlers

import (
	"encoding/json"
	"net/http"
)

// TrafficJSON 返回每个 active 账号的累计流量。数据来自进程内隧道计数器
// 差分后写入的 warp_accounts.traffic_up/down，不再依赖 clash_api。
func (h *Handlers) TrafficJSON(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.DB.ListAccounts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type portStat struct {
		Tag      string `json:"tag"`
		PublicIP string `json:"public_ip"`
		Status   string `json:"status"`
		Upload   int64  `json:"upload"`
		Download int64  `json:"download"`
	}
	var stats []portStat
	var uploadTotal, downloadTotal int64
	for _, a := range accounts {
		if a.Status != "active" {
			continue
		}
		stats = append(stats, portStat{
			Tag:      a.Tag,
			PublicIP: a.LastPublicIP,
			Status:   a.Status,
			Upload:   a.TrafficUp,
			Download: a.TrafficDown,
		})
		uploadTotal += a.TrafficUp
		downloadTotal += a.TrafficDown
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"connections":   stats,
		"downloadTotal": downloadTotal,
		"uploadTotal":   uploadTotal,
		"total":         len(stats),
	})
}
