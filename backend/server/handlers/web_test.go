package handlers_test

import (
	"io/fs"
	"testing"

	"github.com/zpooi/ProxyForge/backend"
	"github.com/zpooi/ProxyForge/backend/server/handlers"
)

func TestWebAssetsAvailable(t *testing.T) {
	webFS := backend.Web()
	for _, name := range []string{"index.html", "style.css", "assets/main.js", "assets/App.js"} {
		if _, err := fs.Stat(webFS, name); err != nil {
			t.Fatalf("expected built web asset %s: %v", name, err)
		}
	}

	var h handlers.Handlers
	if err := h.Init(webFS); err != nil {
		t.Fatal(err)
	}
}
