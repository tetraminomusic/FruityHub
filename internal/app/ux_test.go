package app

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinsAndTrayPersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := NewServer(nil)
	dir := filepath.Join(t.TempDir(), "project")
	s.cfg = &Config{Projects: []LocalProject{{Path: dir}}}
	if !s.TrayEnabled() {
		t.Fatal("tray must be enabled by default")
	}
	h := s.Handler(0)
	post := func(path, body string) {
		t.Helper()
		r := httptest.NewRequest("POST", "http://127.0.0.1:0"+path, strings.NewReader(body))
		r.Header.Set("X-FLSync", s.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	// Используем JSON-маршалинг, чтобы проверить и Windows-пути.
	b, _ := json.Marshal(map[string]any{"path": dir, "pinned": true})
	post("/api/projects/pin", string(b))
	if !LoadConfig().Projects[0].Pinned {
		t.Fatal("pin was not persisted")
	}
	post("/api/settings", `{"tray":false}`)
	if s.TrayEnabled() || !LoadConfig().NoTray {
		t.Fatal("tray disable was not applied and persisted")
	}
	reopened := NewServer(nil)
	if reopened.TrayEnabled() {
		t.Fatal("tray reset after reopening")
	}
	post("/api/settings", `{"tray":true}`)
	if !s.TrayEnabled() || LoadConfig().NoTray {
		t.Fatal("tray enable was not applied")
	}
}
