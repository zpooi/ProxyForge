package handlers

import (
	"encoding/json"
	"net/http"
)

func (h *Handlers) LogsJSON(w http.ResponseWriter, r *http.Request) {
	runs, err := h.DB.ListRuns(200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type runView struct {
		ID               int64  `json:"id"`
		StartedAt        string `json:"started_at"`
		FinishedAt       string `json:"finished_at"`
		Kind             string `json:"kind"`
		Status           string `json:"status"`
		Detail           string `json:"detail"`
		AccountsKept     *int   `json:"accounts_kept"`
		AccountsDisabled *int   `json:"accounts_disabled"`
	}
	var views []runView
	for _, r := range runs {
		v := runView{
			ID: r.ID, Kind: r.Kind, Status: r.Status, Detail: r.Detail,
			StartedAt:        r.StartedAt.Format("2006-01-02 15:04:05"),
			AccountsKept:     r.AccountsKept,
			AccountsDisabled: r.AccountsDisabled,
		}
		if r.FinishedAt != nil {
			v.FinishedAt = r.FinishedAt.Format("2006-01-02 15:04:05")
		}
		views = append(views, v)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"runs": views})
}
