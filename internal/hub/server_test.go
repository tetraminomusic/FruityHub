package hub_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0o700)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	b, _ := json.Marshal(map[string]any{"appId": 1, "org": gh.Org, "pem": string(pemKey)})
	os.WriteFile(filepath.Join(data, "github-app.json"), b, 0o600)
	s, err := hub.New(hub.Options{DataDir: data, PublicURL: "http://x", GitHubAPI: gh.Srv.URL, MaxFile: 10})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	anon := &client{t: t, base: srv.URL}
	reg := func(nick string) *client {
		code, r := anon.do("POST", "/api/register", map[string]string{"nick": nick, "os": "macOS"})
		if code != 200 {
			t.Fatalf("register %s: %d %v", nick, code, r)
		}
		return &client{t: t, base: srv.URL, tok: r["token"].(string)}
	}
	if code, _ := anon.do("POST", "/api/register", map[string]string{"nick": "a/b"}); code != 400 {
		t.Fatal("bad nick accepted")
	}
	if code, _ := anon.do("GET", "/api/me", nil); code != 401 {
		t.Fatal("anonymous access")
	}
	a, bb, c := reg("alpha"), reg("beta"), reg("gamma")
	if code, _ := bb.do("PATCH", "/api/me", map[string]string{"nick": "ALPHA"}); code != 400 {
		t.Fatal("rename to a taken nick")
	}
	code, p := a.do("POST", "/api/projects", map[string]string{"name": "Трек"})
	if code != 200 || !strings.HasPrefix(p["repo"].(string), gh.Org+"/trek-alpha") {
		t.Fatalf("create: %d %v", code, p)
	}
	pid := p["id"].(string)
	if code, _ := bb.do("GET", "/api/projects/"+pid+"/token", nil); code != 403 {
		t.Fatal("non-member got a token")
	}
	if code, r := a.do("GET", "/api/projects/"+pid+"/token", nil); code != 200 || !strings.HasPrefix(r["token"].(string), "ghs_") {
		t.Fatalf("owner token: %d %v", code, r)
	}
	_, inv := a.do("POST", "/api/projects/"+pid+"/invite", map[string]string{"nick": "beta"})
	if code, _ := c.do("POST", "/api/invites/"+inv["id"].(string)+"/accept", nil); code != 404 {
		t.Fatal("someone else accepted the invite")
	}
	bb.do("POST", "/api/invites/"+inv["id"].(string)+"/accept", nil)
	_, me := c.do("GET", "/api/me", nil)
	_, inv2 := a.do("POST", "/api/projects/"+pid+"/invite", map[string]string{"nick": "gamma"})
	c.do("POST", "/api/invites/"+inv2["id"].(string)+"/accept", nil)
	_, beta := bb.do("GET", "/api/me", nil)
	if code, _ := c.do("DELETE", "/api/projects/"+pid+"/members/"+beta["id"].(string), nil); code != 403 {
		t.Fatal("a member removed another member")
	}
	if code, _ := c.do("DELETE", "/api/projects/"+pid+"/members/"+me["id"].(string), nil); code != 200 {
		t.Fatal("leaving a project failed")
	}
	// plugin files: size limit, only the requester downloads
	_, list := bb.do("POST", "/api/plugin-requests", map[string]any{"to": "alpha", "items": []map[string]string{{"plugin": "Serum", "format": "VST3"}}})
	_ = list
	code2, all := bb.do("GET", "/api/inbox", nil)
	_ = all
	if code2 != 200 {
		t.Fatal("inbox")
	}
	_, ainbox := a.do("GET", "/api/inbox", nil)
	q := ainbox["incoming"].([]any)[0].(map[string]any)
	if code, _ := a.do("PUT", "/api/plugin-requests/"+q["id"].(string)+"/file?kind=plugin", []byte("this is more than ten bytes")); code != 413 {
		t.Fatalf("big file accepted: %d", code)
	}
	if code, _ := a.do("PUT", "/api/plugin-requests/"+q["id"].(string)+"/file?kind=plugin&name=s.zip", []byte("zipdata")); code != 200 {
		t.Fatal("upload failed")
	}
	if code, _ := c.do("GET", "/api/plugin-requests/"+q["id"].(string)+"/file", nil); code != 404 && code != 403 {
		t.Fatal("stranger downloaded the plugin")
	}
	if code, r := bb.do("GET", "/api/plugin-requests/"+q["id"].(string)+"/file", nil); code != 200 || r["raw"] != "zipdata" {
		t.Fatalf("download: %d %v", code, r)
	}
	// setup page needs the admin key; the install callback asks GitHub itself
	if code, r := anon.do("GET", "/setup", nil); code != 200 || !strings.Contains(r["raw"].(string), "ключ администратора") {
		t.Fatal("setup without key")
	}
	if code, r := anon.do("GET", "/setup/installed?installation_id=999", nil); code != 200 || !strings.Contains(r["raw"].(string), "Готово") {
		t.Fatalf("installed: %v", r)
	}
	if s.GitHubApp().InstallationID != 7 {
		t.Fatalf("installation id taken from the URL: %d", s.GitHubApp().InstallationID)
	}
}
