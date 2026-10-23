package hub_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flsync/internal/fakegh"
	"flsync/internal/hub"
)

func TestLocksAndNego(t *testing.T) {
	root := t.TempDir()
	gh := fakegh.New(root)
	defer gh.Close()
	gh.AddUser("tA", "alogin")
	gh.AddUser("tC", "clogin")
	gh.AddUser("tD", "dlogin")
	s, err := hub.New(hub.Options{DataDir: filepath.Join(root, "data"), GitHubAPI: gh.Srv.URL, Code: "secret", StreamWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	anon := &client{t: t, base: srv.URL}
	reg := func(nick, tok string) *client {
		_, r := anon.do("POST", "/api/register", map[string]string{"nick": nick, "os": "Windows", "code": "secret", "githubToken": tok})
		return &client{t: t, base: srv.URL, tok: r["token"].(string)}
	}
	a, c, d := reg("alpha", "tA"), reg("gamma", "tC"), reg("delta", "tD")
	_, p := a.do("POST", "/api/projects", map[string]string{"name": "Трек", "repo": "alogin/trek", "cloneUrl": "x"})
	pid := p["id"].(string)
	a.do("POST", "/api/projects/"+pid+"/sync-members", map[string]any{"logins": []string{"clogin"}})

	// --- замки ---
	if code, l := a.do("POST", "/api/projects/"+pid+"/lock", map[string]any{"note": "свожу"}); code != 200 || l["user"] != "alpha" {
		t.Fatalf("take: %d %v", code, l)
	}
	if code, r := c.do("POST", "/api/projects/"+pid+"/lock", nil); code != 409 {
		t.Fatalf("second lock must be refused: %d %v", code, r)
	}
	if code, _ := d.do("POST", "/api/projects/"+pid+"/lock", nil); code != 403 {
		t.Fatal("not a member took the lock")
	}
	_, in := c.do("GET", "/api/inbox", nil)
	if l := in["locks"].(map[string]any)[pid].(map[string]any); l["user"] != "alpha" || l["note"] != "свожу" {
		t.Fatalf("inbox locks: %v", in["locks"])
	}
	_, ps := c.do("GET", "/api/projects", nil)
	_ = ps
	if code, _ := c.do("DELETE", "/api/projects/"+pid+"/lock", nil); code != 409 {
		t.Fatal("someone else released the lock")
	}
	if code, l := c.do("POST", "/api/projects/"+pid+"/lock", map[string]any{"force": true}); code != 200 || l["user"] != "gamma" {
		t.Fatalf("force: %d %v", code, l)
	}
	if code, _ := c.do("DELETE", "/api/projects/"+pid+"/lock", nil); code != 200 {
		t.Fatal("release")
	}
	_, in = a.do("GET", "/api/inbox", nil)
	if in["locks"].(map[string]any)[pid] != nil {
		t.Fatalf("lock must be gone: %v", in["locks"])
	}

	// --- сверка зависимостей ---
	c.do("PATCH", "/api/me", map[string]string{"sharing": "all"})
	_, rs := a.do("POST", "/api/plugin-requests", map[string]any{"to": "gamma", "items": []map[string]string{{"plugin": "D-50", "format": "VST3"}}})
	_ = rs
	_, inC := c.do("GET", "/api/inbox", nil)
	id := inC["incoming"].([]any)[0].(map[string]any)["id"].(string)
	c.do("POST", "/api/plugin-requests/"+id+"/approve", map[string]any{"kind": "plugin", "name": "D-50.vst3"})
	if code, _ := a.do("PUT", "/api/plugin-requests/"+id+"/offer", map[string]any{"items": []any{}}); code != 403 {
		t.Fatal("the requester must not publish the offer")
	}
	code, r := c.do("PUT", "/api/plugin-requests/"+id+"/offer", map[string]any{"plugin": "D-50.vst3", "items": []any{map[string]any{"root": "PROGRAMFILES", "rel": "Roland Cloud", "files": []string{"a", "b"}}}})
	if code != 200 || r["seq"].(float64) != 1 {
		t.Fatalf("offer: %d %v", code, r)
	}
	_, inA := a.do("GET", "/api/inbox", nil)
	if s := inA["ready"].([]any)[0].(map[string]any)["offerSeq"]; s != float64(1) {
		t.Fatalf("receiver must see the offer: %v", inA["ready"])
	}
	_, o := a.do("GET", "/api/plugin-requests/"+id+"/offer", nil)
	if o["offer"].(map[string]any)["plugin"] != "D-50.vst3" {
		t.Fatalf("get offer: %v", o)
	}
	done := make(chan map[string]any)
	go func() { _, h := c.do("GET", "/api/plugin-requests/"+id+"/have", nil); done <- h }()
	time.Sleep(300 * time.Millisecond)
	if code, _ := a.do("PUT", "/api/plugin-requests/"+id+"/have", map[string]any{"seq": 7, "have": map[string]any{}}); code != 409 {
		t.Fatal("stale answer accepted")
	}
	a.do("PUT", "/api/plugin-requests/"+id+"/have", map[string]any{"seq": 1, "have": map[string]any{"0": []int{1}}})
	select {
	case h := <-done:
		if h["have"].(map[string]any)["0"].([]any)[0] != float64(1) {
			t.Fatalf("have: %v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not get the answer")
	}
	_, inA = a.do("GET", "/api/inbox", nil)
	if s := inA["ready"].([]any)[0].(map[string]any)["offerSeq"]; s != nil {
		t.Fatalf("answered offer must not be shown again: %v", s)
	}
}

func TestServerLogs(t *testing.T) {
	root := t.TempDir()
	gh := fakegh.New(root)
	defer gh.Close()
	gh.AddUser("tA", "alogin")
	s, err := hub.New(hub.Options{DataDir: filepath.Join(root, "data"), GitHubAPI: gh.Srv.URL, Code: "secret", AdminKey: "adm"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	anon := &client{t: t, base: srv.URL}
	_, r := anon.do("POST", "/api/register", map[string]string{"nick": "alpha", "os": "Windows", "code": "secret", "githubToken": "tA"})
	a := &client{t: t, base: srv.URL, tok: r["token"].(string)}
	a.do("GET", "/api/plugin-requests/nope", nil) // ошибка 404 — попадёт в журнал
	if code, _ := a.do("POST", "/api/log", map[string]any{"version": "0.7.6", "os": "Windows", "lines": []map[string]any{{"t": 1, "text": "[job] ■ Получаю D-50 — ошибка: Access is denied"}}}); code != 200 {
		t.Fatal("client log", code)
	}
	if code, _ := a.do("GET", "/api/admin/logs", nil); code != 403 {
		t.Fatal("logs without admin key")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/admin/logs?since=0", nil)
	req.Header.Set("X-Admin-Key", "adm")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Entries []hub.LogEntry `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	var srvErr, cli bool
	for _, e := range out.Entries {
		if e.Level == "error" && e.Src == "alpha" && strings.Contains(e.Text, "/api/plugin-requests/nope → 404") {
			srvErr = true
		}
		if e.Level == "client" && strings.HasPrefix(e.Src, "alpha (0.7.6") && strings.Contains(e.Text, "Access is denied") {
			cli = true
		}
	}
	if !srvErr || !cli {
		t.Fatalf("журнал: %+v", out.Entries)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/admin/logs/file", nil)
	req.Header.Set("X-Admin-Key", "adm")
	resp, _ = http.DefaultClient.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Access is denied") {
		t.Fatalf("файл журнала: %s", b)
	}
}

func TestReinstallFlag(t *testing.T) {
	root := t.TempDir()
	gh := fakegh.New(root)
	defer gh.Close()
	gh.AddUser("tA", "alogin")
	gh.AddUser("tB", "blogin")
	s, _ := hub.New(hub.Options{DataDir: filepath.Join(root, "data"), GitHubAPI: gh.Srv.URL, Code: "secret"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	anon := &client{t: t, base: srv.URL}
	reg := func(nick, tok string) *client {
		_, r := anon.do("POST", "/api/register", map[string]string{"nick": nick, "os": "Windows", "code": "secret", "githubToken": tok})
		return &client{t: t, base: srv.URL, tok: r["token"].(string)}
	}
	a, b := reg("Кирюха на MSI", "tA"), reg("tetramino", "tB")
	_, inv := b.do("GET", "/api/me", nil)
	_ = inv
	code, raw := a.do("POST", "/api/plugin-requests", map[string]any{"to": "tetramino", "items": []map[string]any{{"plugin": "TR-909", "format": "VST3", "reinstall": true}}})
	if code != 200 {
		t.Fatalf("%d %v", code, raw)
	}
	_, in := b.do("GET", "/api/inbox", nil)
	q := in["incoming"].([]any)[0].(map[string]any)
	if q["reinstall"] != true {
		t.Fatalf("флаг переустановки потерялся: %v", q)
	}
	id := q["id"].(string)
	b.do("POST", "/api/plugin-requests/"+id+"/approve", map[string]any{"kind": "plugin", "name": "TR-909.vst3"})
	a.do("POST", "/api/plugin-requests/"+id+"/done", map[string]any{"ok": true, "partial": true, "note": "данные не все"})
	if _, r := b.do("GET", "/api/plugin-requests/"+id, nil); r["status"] != "installed" || r["partial"] != true {
		t.Fatalf("частичная установка: %v", r)
	}
	a.do("POST", "/api/plugin-requests/"+id+"/retry", map[string]any{"reinstall": false})
	if _, r := a.do("GET", "/api/plugin-requests/"+id, nil); r["status"] != "approved" || r["reinstall"] == true || r["partial"] == true {
		t.Fatalf("повтор без переустановки: %v", r)
	}
}
