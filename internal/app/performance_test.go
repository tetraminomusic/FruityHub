package app

import (
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitCacheTracksExternalChanges(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init")
	run("remote", "add", "origin", "https://example.com/old.git")
	g := &Git{Dir: dir}
	if g.Remote() != "https://example.com/old.git" {
		t.Fatal("remote missing")
	}
	run("remote", "set-url", "origin", "https://example.com/new.git")
	if g.Remote() != "https://example.com/new.git" {
		t.Fatal("cached stale remote after external change")
	}
	g.Branch()
	run("symbolic-ref", "HEAD", "refs/heads/other")
	if g.Branch() != "other" {
		t.Fatal("cached stale branch after external change")
	}
}

func TestQuickStateKeepsIdentityAndProjectList(t *testing.T) {
	s := NewServer(nil)
	s.cfg = &Config{UserName: "test-user", UserID: "test-id", DeviceToken: "private-device-key", Token: "private-github-key", Server: "https://example.com", Projects: []LocalProject{{Path: filepath.Join(t.TempDir(), "song"), Name: "Song"}}}
	r := httptest.NewRequest("GET", "http://127.0.0.1:0/api/state?quick=1", nil)
	r.Header.Set("X-FLSync", s.token)
	w := httptest.NewRecorder()
	s.Handler(0).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var state struct {
		Config     Config           `json:"config"`
		Projects   []ProjectSummary `json:"projects"`
		Registered bool             `json:"registered"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Registered || state.Config.UserName != "test-user" || len(state.Projects) != 1 || state.Projects[0].Name != "Song" {
		t.Fatalf("incomplete quick state: %+v", state)
	}
	if state.Config.Token != "" || state.Config.DeviceToken != "" {
		t.Fatal("quick state exposed credentials")
	}
}
