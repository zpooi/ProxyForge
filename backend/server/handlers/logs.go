package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
)

const liveLogChunkSize = 256 << 10

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

func (h *Handlers) LiveLogsJSON(w http.ResponseWriter, r *http.Request) {
	if h.LogStore == nil {
		http.Error(w, "live log is unavailable", http.StatusServiceUnavailable)
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	chunk, err := h.LogStore.Read(offset, liveLogChunkSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A browser left open across midnight carries yesterday's offset. Reset it
	// against the newly rotated file even when the new file is already larger.
	if expected := r.URL.Query().Get("date"); expected != "" && expected != chunk.Date && offset != 0 {
		chunk, err = h.LogStore.Read(0, liveLogChunkSize)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"date":    chunk.Date,
		"content": chunk.Content,
		"next":    chunk.Next,
		"more":    chunk.More,
	})
}

func (h *Handlers) DownloadLogs(w http.ResponseWriter, _ *http.Request) {
	if h.LogStore == nil {
		http.Error(w, "live log is unavailable", http.StatusServiceUnavailable)
		return
	}
	date, content, err := h.LogStore.Snapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="proxyforge-`+date+`.log"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	_, _ = w.Write(content)
}
