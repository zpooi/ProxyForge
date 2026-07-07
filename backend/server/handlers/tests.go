package handlers

import (
	"encoding/json"
	"net/http"
)

// AccountTestsJSON 返回指定账号最近的测试历史（延迟/速度/评分/出口 IP）。
func (h *Handlers) AccountTestsJSON(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	if id == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	tests, err := h.DB.ListAccountTests(id, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type testView struct {
		TestedAt  string  `json:"tested_at"`
		PublicIP  string  `json:"public_ip"`
		Colo      string  `json:"colo"`
		Country   string  `json:"country"`
		LatencyMs int     `json:"latency_ms"`
		SpeedBps  int     `json:"speed_bps"`
		Score     float64 `json:"score"`
		Error     string  `json:"error"`
	}
	views := []testView{}
	for _, t := range tests {
		views = append(views, testView{
			TestedAt:  t.TestedAt.Format("2006-01-02 15:04:05"),
			PublicIP:  t.PublicIP,
			Colo:      t.Colo,
			Country:   t.Country,
			LatencyMs: t.LatencyMs,
			SpeedBps:  t.SpeedBps,
			Score:     t.Score,
			Error:     t.Error,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tests": views})
}
