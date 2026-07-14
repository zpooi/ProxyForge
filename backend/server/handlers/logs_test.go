package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zpooi/ProxyForge/backend/internal/applog"
)

func TestLiveLogsJSONAndDownload(t *testing.T) {
	store, err := applog.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Write([]byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	h := &Handlers{LogStore: store}

	rec := httptest.NewRecorder()
	h.LiveLogsJSON(rec, httptest.NewRequest(http.MethodGet, "/api/logs/live?offset=4", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("live status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Date    string `json:"date"`
		Content string `json:"content"`
		Next    int64  `json:"next"`
		More    bool   `json:"more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Date == "" || body.Content != "two\n" || body.Next != 8 || body.More {
		t.Fatalf("unexpected live response: %#v", body)
	}

	download := httptest.NewRecorder()
	h.DownloadLogs(download, httptest.NewRequest(http.MethodGet, "/api/logs/download", nil))
	if download.Code != http.StatusOK || download.Body.String() != "one\ntwo\n" {
		t.Fatalf("download status/body = %d %q", download.Code, download.Body.String())
	}
	if disposition := download.Header().Get("Content-Disposition"); !strings.Contains(disposition, "proxyforge-") || !strings.HasSuffix(disposition, `.log"`) {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
}
