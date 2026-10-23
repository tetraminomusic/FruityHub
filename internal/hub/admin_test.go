package hub_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flsync/internal/fakegh"
	"flsync/internal/hub"
)

func adminDo(t *testing.T, base, key, method, path string, body []byte) (int, []byte) {
	req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("X-Admin-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// Админка: ключ, обзор, удаление, бэкап → восстановление на «новом» сервере, переезд, раздача обновлений.
func TestAdmin(t *testing.T) {
	root := t.TempDir()
	gh := fakegh.New(root)
	defer gh.Close()
	gh.AddUser("tA", "alogin")
	gh.AddUser("tB", "blogin")
	data := filepath.Join(root, "data")
	s, err := hub.New(hub.Options{DataDir: data, GitHubAPI: gh.Srv.URL, Code: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(data, "admin-key")); len(bytes.TrimSpace(b)) < 16 || string(bytes.TrimSpace(b)) != s.AdminKey() {
		t.Fatal("ключ администратора должен создаться в папке данных")
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	key := s.AdminKey()

	anon := &client{t: t, base: srv.URL}
	reg := func(nick, tok string) *client {
		_, r := anon.do("POST", "/api/register", map[string]string{"nick": nick, "os": "Windows", "code": "secret", "githubToken": tok})
		return &client{t: t, base: srv.URL, tok: r["token"].(string)}
	}
	a, b := reg("anna", "tA"), reg("boris", "tB")
	if code, _ := a.do("POST", "/api/projects", map[string]string{"name": "Трек", "repo": "alogin/trek", "cloneUrl": "x"}); code != 200 {
		t.Fatal("project")
	}

	if code, _ := adminDo(t, srv.URL, "wrong", "GET", "/api/admin/overview", nil); code != 403 {
		t.Fatal("чужой ключ должен отклоняться")
	}
	code, raw := adminDo(t, srv.URL, key, "GET", "/api/admin/overview", nil)
	var ov struct {
		Users []struct {
			ID, Nick string
		}
		Projects []struct{ ID, Name string }
	}
	json.Unmarshal(raw, &ov)
	if code != 200 || len(ov.Users) != 2 || len(ov.Projects) != 1 {
		t.Fatalf("обзор: %d %s", code, raw)
	}

	// бэкап → восстановление в новую папку → там узнают устройство
	code, bk := adminDo(t, srv.URL, key, "GET", "/api/admin/backup", nil)
	if code != 200 {
		t.Fatal("backup")
	}
	bf := filepath.Join(root, "backup.json")
	os.WriteFile(bf, bk, 0o600)
	data2 := filepath.Join(root, "data2")
	if err := hub.RestoreBackup(data2, bf); err != nil {
		t.Fatal(err)
	}
	s2, err := hub.New(hub.Options{DataDir: data2, GitHubAPI: gh.Srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Code() != "secret" || s2.AdminKey() != key {
		t.Fatal("код и ключ должны переехать")
	}
	srv2 := httptest.NewServer(s2.Handler())
	defer srv2.Close()
	if code, r := (&client{t: t, base: srv2.URL, tok: a.tok}).do("GET", "/api/projects", nil); code != 200 {
		t.Fatalf("на новом сервере устройство не узнали: %d %v", code, r)
	}

	// переезд: старый сервер говорит приложениям новый адрес
	adminDo(t, srv.URL, key, "POST", "/api/admin/move", []byte(`{"url":"`+srv2.URL+`"}`))
	if _, r := a.do("GET", "/api/inbox", nil); r["moveTo"] != srv2.URL {
		t.Fatalf("inbox moveTo: %v", r["moveTo"])
	}

	// обновления
	payload := []byte("new-exe-bytes")
	if code, r := adminDo(t, srv.URL, key, "PUT", "/api/admin/updates/windows-amd64?version=0.7.0&name=FruityHub.exe", payload); code != 200 {
		t.Fatalf("publish: %d %s", code, r)
	}
	if _, r := a.do("GET", "/api/inbox", nil); r["update"] != "0.7.0" {
		t.Fatalf("inbox update: %v", r["update"])
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/updates/windows-amd64/download", nil)
	req.Header.Set("Authorization", "Bearer "+b.tok)
	resp, _ := http.DefaultClient.Do(req)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, payload) || resp.Header.Get("X-SHA256") == "" {
		t.Fatalf("скачивание обновления: %q", got)
	}
	if code, _ := adminDo(t, srv.URL, key, "PUT", "/api/admin/updates/windows-amd64?version=0.7.1&name=FruityHub.exe", []byte("v2")); code != 200 {
		t.Fatal("publish 2")
	}
	ents, _ := os.ReadDir(filepath.Join(data, "updates"))
	if len(ents) != 2 { // manifest + один файл: старые версии удаляются
		t.Fatalf("старые сборки должны удаляться: %d файлов", len(ents))
	}

	// удаление пользователя: его проект без других участников уходит с сервера
	if code, _ := adminDo(t, srv.URL, key, "DELETE", "/api/admin/users/"+ov.Users[0].ID, nil); code != 200 {
		t.Fatal("delete user")
	}
	_, raw = adminDo(t, srv.URL, key, "GET", "/api/admin/overview", nil)
	json.Unmarshal(raw, &ov)
	if len(ov.Users) != 1 {
		t.Fatalf("после удаления: %s", raw)
	}
}

// Отмена передачи любой стороной посреди потока и повтор.
func TestCancelRetryTransfer(t *testing.T) {
	root := t.TempDir()
	gh := fakegh.New(root)
	defer gh.Close()
	gh.AddUser("tA", "alogin")
	gh.AddUser("tB", "blogin")
	s, err := hub.New(hub.Options{DataDir: filepath.Join(root, "data"), GitHubAPI: gh.Srv.URL, Code: "secret", StreamWait: 5e9})
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
	a, b := reg("anna", "tA"), reg("boris", "tB")
	b.do("PATCH", "/api/me", map[string]string{"sharing": "all"})
	code, raw := a.do("POST", "/api/plugin-requests", map[string]any{"to": "boris", "items": []map[string]string{{"plugin": "DX7 V", "format": "VST3"}}})
	if code != 200 {
		t.Fatalf("request: %d %v", code, raw)
	}
	_, inbox := b.do("GET", "/api/inbox", nil)
	id := inbox["incoming"].([]any)[0].(map[string]any)["id"].(string)
	b.do("POST", "/api/plugin-requests/"+id+"/approve", map[string]any{"kind": "plugin", "name": "DX7 V.vst3"})
	// отправитель начинает поток и ждёт получателя; получатель (тот, кто просил) отменяет
	done := make(chan int, 1)
	go func() {
		c, _ := b.do("PUT", "/api/plugin-requests/"+id+"/stream", []byte("payload"))
		done <- c
	}()
	time.Sleep(200 * time.Millisecond)
	if c, r := a.do("POST", "/api/plugin-requests/"+id+"/cancel", nil); c != 200 || r["status"] != "cancelled" {
		t.Fatalf("cancel: %d %v", c, r)
	}
	select {
	case c := <-done:
		if c != 410 {
			t.Fatalf("отправитель должен узнать об отмене (410), а получил %d", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("поток не оборвался после отмены")
	}
	// сообщение «ошибка» от оборванной стороны не перезаписывает «отменён»
	b.do("POST", "/api/plugin-requests/"+id+"/done", map[string]any{"ok": false, "note": "оборвалось"})
	if _, r := a.do("GET", "/api/plugin-requests/"+id, nil); r["status"] != "cancelled" {
		t.Fatalf("статус после отмены: %v", r["status"])
	}
	// повтор — снова «разрешён», передача проходит
	if c, r := b.do("POST", "/api/plugin-requests/"+id+"/retry", nil); c != 200 || r["status"] != "approved" {
		t.Fatalf("retry: %d %v", c, r)
	}
	go func() { c, _ := b.do("PUT", "/api/plugin-requests/"+id+"/stream", []byte("payload")); done <- c }()
	req, _ := http.NewRequest("GET", srv.URL+"/api/plugin-requests/"+id+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+a.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "payload" || <-done != 200 {
		t.Fatalf("повторная передача: %q", got)
	}
}
