package hub_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flsync/internal/fakegh"
	"flsync/internal/hub"
)

type client struct {
	t    *testing.T
	base string
	tok  string
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		out = map[string]any{"raw": string(raw)}
	}
	return resp.StatusCode, out
}

func TestHubRules(t *testing.T) {
	root := t.TempDir()
	gh := fakegh.New(root)
	defer gh.Close()
	gh.AddUser("tA", "alogin")
	gh.AddUser("tB", "blogin")
	gh.AddUser("tC", "clogin")
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0o700)
	s, err := hub.New(hub.Options{DataDir: data, GitHubAPI: gh.Srv.URL, Code: "secret", StreamWait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	anon := &client{t: t, base: srv.URL}
	reg := func(nick, tok string) *client {
		code, r := anon.do("POST", "/api/register", map[string]string{"nick": nick, "os": "macOS", "code": "secret", "githubToken": tok})
		if code != 200 {
			t.Fatalf("register %s: %d %v", nick, code, r)
		}
		return &client{t: t, base: srv.URL, tok: r["token"].(string)}
	}
	if code, _ := anon.do("POST", "/api/register", map[string]string{"nick": "x1", "code": "wrong", "githubToken": "tA"}); code != 403 {
		t.Fatal("wrong code accepted")
	}
	if code, _ := anon.do("POST", "/api/register", map[string]string{"nick": "x1", "code": "secret", "githubToken": "nope"}); code != 400 {
		t.Fatal("bad GitHub token accepted")
	}
	if code, _ := anon.do("POST", "/api/register", map[string]string{"nick": "a/b", "code": "secret", "githubToken": "tA"}); code != 400 {
		t.Fatal("bad nick accepted")
	}
	if code, _ := anon.do("GET", "/api/me", nil); code != 401 {
		t.Fatal("anonymous access")
	}
	a, b, c := reg("alpha", "tA"), reg("beta", "tB"), reg("gamma", "tC")
	if _, me := b.do("GET", "/api/me", nil); me["github"] != "blogin" {
		t.Fatalf("github login not verified: %v", me)
	}
	if code, _ := b.do("PATCH", "/api/me", map[string]string{"nick": "ALPHA"}); code != 400 {
		t.Fatal("rename to a taken nick")
	}
	code, p := a.do("POST", "/api/projects", map[string]string{"name": "Трек", "repo": "alogin/trek", "cloneUrl": "x"})
	if code != 200 {
		t.Fatalf("add project: %d %v", code, p)
	}
	pid := p["id"].(string)
	if code, _ := b.do("POST", "/api/projects", map[string]string{"name": "чужой", "repo": "ALOGIN/trek"}); code != 409 {
		t.Fatal("someone else registered the same repo")
	}
	// collaborators that are already on GitHub become members
	if code, r := a.do("POST", "/api/projects/"+pid+"/sync-members", map[string]any{"logins": []string{"clogin", "stranger"}}); code != 200 || len(r["members"].([]any)) != 2 {
		t.Fatalf("sync: %d %v", code, r)
	}
	_, inv := a.do("POST", "/api/projects/"+pid+"/invite", map[string]string{"nick": "beta"})
	if inv["to"].(map[string]any)["github"] != "blogin" {
		t.Fatalf("invite must tell the GitHub login: %v", inv)
	}
	if code, _ := c.do("POST", "/api/invites/"+inv["id"].(string)+"/accept", nil); code != 404 {
		t.Fatal("someone else accepted the invite")
	}
	b.do("POST", "/api/invites/"+inv["id"].(string)+"/accept", nil)
	_, me := c.do("GET", "/api/me", nil)
	_, beta := b.do("GET", "/api/me", nil)
	if code, _ := c.do("DELETE", "/api/projects/"+pid+"/members/"+beta["id"].(string), nil); code != 403 {
		t.Fatal("a member removed another member")
	}
	if code, _ := c.do("DELETE", "/api/projects/"+pid+"/members/"+me["id"].(string), nil); code != 200 {
		t.Fatal("leaving a project failed")
	}

	// plugin stream: bytes go through, nothing is stored
	b.do("POST", "/api/plugin-requests", map[string]any{"to": "alpha", "items": []map[string]string{{"plugin": "Serum", "format": "VST3"}}})
	_, ainbox := a.do("GET", "/api/inbox", nil)
	q := ainbox["incoming"].([]any)[0].(map[string]any)
	id := q["id"].(string)
	if code, _ := b.do("GET", "/api/plugin-requests/"+id+"/stream", nil); code != 400 {
		t.Fatal("download before approval")
	}
	payload := bytes.Repeat([]byte("plugin-bytes-"), 50000)
	if code, _ := a.do("POST", "/api/plugin-requests/"+id+"/approve", map[string]any{"kind": "plugin", "name": "Serum.vst3.zip", "size": len(payload)}); code != 200 {
		t.Fatal("approve")
	}
	// the receiver is offline: the sender gives up after the wait
	if code, _ := a.do("PUT", "/api/plugin-requests/"+id+"/stream", payload); code != 408 {
		t.Fatalf("send without a receiver: %d", code)
	}
	if code, _ := c.do("GET", "/api/plugin-requests/"+id+"/stream", nil); code != 403 {
		t.Fatal("a stranger got the stream")
	}
	var wg sync.WaitGroup
	var got []byte
	var gotCode int
	wg.Add(1)
	go func() { // receiver first, sender a bit later
		defer wg.Done()
		req, _ := http.NewRequest("GET", srv.URL+"/api/plugin-requests/"+id+"/stream", nil)
		req.Header.Set("Authorization", "Bearer "+b.tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		gotCode = resp.StatusCode
		got, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}()
	time.Sleep(300 * time.Millisecond)
	if code, r := a.do("PUT", "/api/plugin-requests/"+id+"/stream", payload); code != 200 {
		t.Fatalf("send: %d %v", code, r)
	}
	wg.Wait()
	if gotCode != 200 || !bytes.Equal(got, payload) {
		t.Fatalf("received %d bytes, code %d", len(got), gotCode)
	}
	b.do("POST", "/api/plugin-requests/"+id+"/done", map[string]any{"ok": true})
	if _, r := b.do("GET", "/api/plugin-requests/"+id, nil); r["status"] != "installed" {
		t.Fatalf("status: %v", r)
	}
	ents, _ := os.ReadDir(data)
	for _, e := range ents {
		if e.Name() == "logs" { // журнал сервера — только строки журнала, плагинов там нет
			ls, _ := os.ReadDir(filepath.Join(data, "logs"))
			for _, l := range ls {
				if l.Name() != "server.log" {
					t.Fatalf("unexpected file in logs: %s", l.Name())
				}
			}
			b, _ := os.ReadFile(filepath.Join(data, "logs", "server.log"))
			if bytes.Contains(b, payload) {
				t.Fatal("в журнал попали данные плагина")
			}
			continue
		}
		if e.Name() != "db.json" && e.Name() != "server-code" && e.Name() != "admin-key" && !strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("unexpected file on the server: %s", e.Name())
		}
	}
}

// Сменил ник: запросы к прежнему нику (у друга в проекте он ещё записан) доходят до нынешнего;
// прежний ник свободен для других, и его нынешний владелец важнее.
func TestRenamedNick(t *testing.T) {
	root := t.TempDir()
	gh := fakegh.New(root)
	defer gh.Close()
	gh.AddUser("tA", "alogin")
	gh.AddUser("tB", "blogin")
	gh.AddUser("tC", "clogin")
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0o700)
	s, err := hub.New(hub.Options{DataDir: data, GitHubAPI: gh.Srv.URL, Code: "secret", StreamWait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	anon := &client{t: t, base: srv.URL}
	reg := func(nick, tok string) *client {
		code, r := anon.do("POST", "/api/register", map[string]string{"nick": nick, "os": "macOS", "code": "secret", "githubToken": tok})
		if code != 200 {
			t.Fatalf("register %s: %d %v", nick, code, r)
		}
		return &client{t: t, base: srv.URL, tok: r["token"].(string)}
	}
	a, b := reg("старый", "tA"), reg("друг", "tB")
	if code, _ := a.do("PATCH", "/api/me", map[string]string{"nick": "новый"}); code != 200 {
		t.Fatal("rename")
	}
	if code, r := b.do("POST", "/api/plugin-requests", map[string]any{"to": "старый", "items": []map[string]string{{"plugin": "Serum", "format": "VST3"}}}); code != 200 {
		t.Fatalf("запрос к прежнему нику: %d %v", code, r)
	}
	_, inbox := a.do("GET", "/api/inbox", nil)
	if len(inbox["incoming"].([]any)) != 1 {
		t.Fatal("запрос не дошёл до нового ника")
	}
	// прежний ник занял другой человек — теперь запросы идут ему
	c := reg("старый", "tC")
	b.do("POST", "/api/plugin-requests", map[string]any{"to": "старый", "items": []map[string]string{{"plugin": "Vital", "format": "VST3"}}})
	if _, ci := c.do("GET", "/api/inbox", nil); len(ci["incoming"].([]any)) != 1 {
		t.Fatal("нынешний владелец ника не получил запрос")
	}
	// приложение сообщает узнанный прежний ник; чужой нынешний ник так не присвоить
	a.do("PATCH", "/api/me", map[string]string{"addOldNick": "совсем старый"})
	a.do("PATCH", "/api/me", map[string]string{"addOldNick": "друг"})
	_, me := a.do("GET", "/api/me", nil)
	olds, _ := json.Marshal(me["oldNicks"])
	if !strings.Contains(string(olds), "совсем старый") || strings.Contains(string(olds), "друг") {
		t.Fatalf("прежние ники: %v", olds)
	}
}
