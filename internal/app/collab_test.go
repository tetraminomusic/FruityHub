package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub implements the handful of release endpoints we use.
type fakeGitHub struct {
	mu     sync.Mutex
	srv    *httptest.Server
	relID  int64
	assets map[int64]*Asset
	blobs  map[int64][]byte
	nextID int64
	tokens map[string]string // token -> login
	repos  map[string]bool   // owner/name
	issues []map[string]any
	invs   []map[string]any
	collab map[string]bool
	files  map[string]string // contents API: path -> base64
	nextIv int
}

func newFakeGitHub() *fakeGitHub {
	f := &fakeGitHub{assets: map[int64]*Asset{}, blobs: map[int64][]byte{}, nextID: 100,
		tokens: map[string]string{"tok": "gh-user", "tokA": "alogin", "tokB": "blogin"}, repos: map[string]bool{}, collab: map[string]bool{}, files: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	login := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if r.URL.Path != "/blob" && login == "" {
		http.Error(w, `{"message":"Bad credentials"}`, 401)
		return
	}
	if r.URL.Path == "/blob" && r.Header.Get("Authorization") != "" {
		http.Error(w, "signed URL must not get auth header", 400)
		return
	}
	p := r.URL.Path
	js := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	var body map[string]any
	if r.Body != nil && !strings.HasPrefix(p, "/upload") {
		json.NewDecoder(r.Body).Decode(&body)
	}
	reg := "/repos/alogin/programma-uzbeka-users"
	switch {
	case p == "/user":
		js(map[string]any{"login": login})
	case p == "/user/repos" && r.Method == "GET":
		list := []map[string]any{{"full_name": "o/r", "name": "r", "clone_url": "https://github.com/o/r.git", "pushed_at": "2026-10-19T10:00:00Z",
			"size": 1234, "topics": []string{"programma-uzbeka"}, "owner": map[string]any{"login": "alogin"}},
			{"full_name": login + "/homework", "name": "homework", "clone_url": "x", "owner": map[string]any{"login": login}}}
		for full := range f.repos {
			parts := strings.SplitN(full, "/", 2)
			if parts[0] == login {
				list = append(list, map[string]any{"full_name": full, "name": parts[1], "clone_url": "x", "owner": map[string]any{"login": login}})
			}
		}
		js(list)
	case strings.HasPrefix(p, "/users/"):
		js(map[string]any{"login": strings.TrimPrefix(p, "/users/")})
	case strings.Contains(p, "/contents/") && r.Method == "GET":
		c, ok := f.files[p]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		js(map[string]any{"sha": fmt.Sprint(len(c)), "content": c})
	case strings.Contains(p, "/contents/") && r.Method == "PUT":
		if _, ok := f.files[p]; ok && body["sha"] == nil {
			http.Error(w, `{"message":"sha required"}`, 409)
			return
		}
		f.files[p] = body["content"].(string)
		js(map[string]any{})
	case strings.HasSuffix(p, "/topics"):
		js(map[string]any{})
	case strings.HasPrefix(p, "/repos/o/r/collaborators/") && r.Method == "DELETE":
		delete(f.collab, strings.TrimPrefix(p, "/repos/o/r/collaborators/"))
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/repos/o/r/invitations/") && r.Method == "DELETE":
		n, _ := strconv.Atoi(strings.TrimPrefix(p, "/repos/o/r/invitations/"))
		for i, iv := range f.invs {
			if iv["id"] == n {
				f.invs = append(f.invs[:i], f.invs[i+1:]...)
				break
			}
		}
		w.WriteHeader(204)
	case p == "/user/repos" && r.Method == "POST":
		f.repos[login+"/"+body["name"].(string)] = true
		js(map[string]any{"full_name": login + "/" + body["name"].(string), "clone_url": "x", "html_url": "x"})
	case p == reg && r.Method == "GET":
		if !f.repos["alogin/programma-uzbeka-users"] {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		js(map[string]any{})
	case p == reg+"/issues" && r.Method == "GET":
		if !f.repos["alogin/programma-uzbeka-users"] {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		var open []map[string]any
		for _, is := range f.issues {
			if is["state"] == "open" {
				open = append(open, is)
			}
		}
		if r.URL.Query().Get("page") != "1" {
			open = nil
		}
		js(open)
	case p == reg+"/issues" && r.Method == "POST":
		is := map[string]any{"number": len(f.issues) + 1, "title": body["title"], "body": body["body"], "state": "open", "user": map[string]any{"login": login}}
		f.issues = append(f.issues, is)
		w.WriteHeader(201)
		js(is)
	case strings.HasPrefix(p, reg+"/issues/") && r.Method == "PATCH":
		n, _ := strconv.Atoi(strings.TrimPrefix(p, reg+"/issues/"))
		f.issues[n-1]["state"] = body["state"]
		js(f.issues[n-1])
	case strings.HasPrefix(p, "/repos/o/r/collaborators/") && r.Method == "PUT":
		who := strings.TrimPrefix(p, "/repos/o/r/collaborators/")
		f.nextIv++
		f.invs = append(f.invs, map[string]any{"id": f.nextIv, "invitee": map[string]any{"login": who}, "inviter": map[string]any{"login": login},
			"repository": map[string]any{"full_name": "o/r", "name": "r", "html_url": "https://github.com/o/r"}})
		w.WriteHeader(201)
	case p == "/user/repository_invitations":
		var mine []map[string]any
		for _, i := range f.invs {
			if i["invitee"].(map[string]any)["login"] == login {
				mine = append(mine, i)
			}
		}
		js(mine)
	case strings.HasPrefix(p, "/user/repository_invitations/") && r.Method == "PATCH":
		n, _ := strconv.Atoi(strings.TrimPrefix(p, "/user/repository_invitations/"))
		for i, iv := range f.invs {
			if iv["id"] == n {
				f.collab[iv["invitee"].(map[string]any)["login"].(string)] = true
				f.invs = append(f.invs[:i], f.invs[i+1:]...)
				break
			}
		}
		w.WriteHeader(204)
	case p == "/repos/o/r/collaborators":
		list := []map[string]any{{"login": "alogin", "permissions": map[string]any{"admin": true}}}
		for l := range f.collab {
			list = append(list, map[string]any{"login": l})
		}
		js(list)
	case p == "/repos/o/r/invitations":
		js(f.invs)
	case p == "/repos/o/r/releases" && r.Method == "GET":
		if f.relID == 0 {
			js([]any{})
		} else {
			js([]map[string]any{{"id": f.relID, "tag_name": storageTag, "upload_url": f.srv.URL + "/upload/1/assets{?name,label}"}})
		}
	case p == "/repos/o/r/releases" && r.Method == "POST":
		f.relID = 1
		js(map[string]any{"id": 1, "upload_url": f.srv.URL + "/upload/1/assets{?name,label}"})
	case p == "/upload/1/assets":
		b, _ := io.ReadAll(r.Body)
		for _, a := range f.assets {
			if a.Name == r.URL.Query().Get("name") {
				http.Error(w, `{"message":"already_exists"}`, 422)
				return
			}
		}
		f.nextID++
		a := &Asset{ID: f.nextID, Name: r.URL.Query().Get("name"), Label: r.URL.Query().Get("label"), Size: int64(len(b))}
		a.Uploader.Login = "gh-user"
		f.assets[a.ID], f.blobs[a.ID] = a, b
		w.WriteHeader(201)
		js(a)
	case p == "/repos/o/r/releases/1/assets":
		var list []*Asset
		for _, a := range f.assets {
			list = append(list, a)
		}
		js(list)
	case strings.HasPrefix(p, "/repos/o/r/releases/assets/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/repos/o/r/releases/assets/"), 10, 64)
		if f.assets[id] == nil {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		if r.Method == "DELETE" {
			delete(f.assets, id)
			delete(f.blobs, id)
			w.WriteHeader(204)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("%s/blob?id=%d", strings.Replace(f.srv.URL, "127.0.0.1", "localhost", 1), id), 302)
	case p == "/blob":
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		w.Write(f.blobs[id])
	default:
		http.Error(w, "unknown "+r.Method+" "+p, 404)
	}
}

func TestCollaboration(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := newFakeGitHub()
	defer gh.srv.Close()
	ghAPIBase = gh.srv.URL
	repoSlug = func(string) (string, string, bool) { return "o", "r", true }

	remote := filepath.Join(root, "remote.git")
	must(t, exec.Command("git", "init", "--bare", "-b", "main", remote).Run())
	aDir := filepath.Join(root, "A", "Трек")
	os.MkdirAll(aDir, 0o755)
	os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP("/nowhere/kick.wav"), 0o644)

	// A has a plugin bundle with a symlink inside (like real macOS bundles)
	plugDir := filepath.Join(root, "A-plugins")
	bundle := filepath.Join(plugDir, "Serum.vst3", "Contents")
	os.MkdirAll(filepath.Join(bundle, "Resources"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Resources", "data.bin"), []byte("serum-binary"), 0o755)
	os.Symlink("Resources", filepath.Join(bundle, "Current"))

	cfgA := &Config{UserName: "tetramino", Token: "tok", ExtraPluginDirs: []string{plugDir}}
	cfgB := &Config{UserName: "friend", Token: "tok"}
	jA, jB := &Job{}, &Job{}
	A := &Project{Dir: aDir, Cfg: cfgA, Job: jA}
	ScanInstalled(cfgA, true)
	must(t, A.Init(remote))
	_, err := A.Send("start", false)
	must(t, err)
	os.MkdirAll(filepath.Join(root, "B"), 0o755)
	bDir, err := Clone(cfgB, jB, remote, filepath.Join(root, "B"))
	must(t, err)
	B := &Project{Dir: bDir, Cfg: cfgB, Job: jB}

	// --- comments while A has unsaved work in the project ---
	os.WriteFile(filepath.Join(aDir, "Track.flp"), append(synthFLP("/x.wav"), 0), 0o644)
	before, _ := os.ReadFile(filepath.Join(aDir, "Track.flp"))
	_, err = A.AddComment("todo", "Нужен вокал во втором куплете")
	must(t, err)
	must(t, A.SetStatus("Аранжировка", "дроп готов, нужен бридж"))
	after, _ := os.ReadFile(filepath.Join(aDir, "Track.flp"))
	if string(before) != string(after) {
		t.Fatal("comment touched the working folder")
	}
	must(t, B.git().Fetch())
	cs, st := B.Comments()
	if len(cs) != 1 || cs[0].Kind != "todo" || st == nil || st.Stage != "Аранжировка" {
		t.Fatalf("B sees %+v %+v", cs, st)
	}
	done := true
	must(t, B.UpdateComment(cs[0].ID, &done, false))
	_, err = B.AddComment("note", "Залил свой вариант баса")
	must(t, err)
	A.git().Fetch()
	cs, _ = A.Comments()
	if len(cs) != 2 || !cs[1].Done || cs[1].DoneBy != "friend" {
		t.Fatalf("A sees %+v", cs)
	}
	if err := B.UpdateComment(cs[1].ID, nil, true); err == nil {
		t.Fatal("B must not delete A's comment")
	}
	// the project branch must be untouched by meta writes
	if out, _ := A.git().RunQuiet("log", "--oneline", "origin/main"); strings.Count(out, "\n") != 0 {
		t.Fatalf("project branch changed: %s", out)
	}

	// --- preview: one file per project, replaced on upload ---
	wav := filepath.Join(root, "mix v1.wav")
	os.WriteFile(wav, []byte("RIFF-wave-v1"), 0o644)
	A.Job = jA
	_, err = A.UploadPreview(wav)
	must(t, err)
	mp3 := filepath.Join(root, "mix v2.mp3")
	os.WriteFile(mp3, []byte("ID3-mp3-v2"), 0o644)
	info, err := A.UploadPreview(mp3)
	must(t, err)
	if len(gh.assets) != 1 || info.FileName != "mix v2.mp3" || info.By != "tetramino" {
		t.Fatalf("preview not replaced: %d assets, %+v", len(gh.assets), info)
	}
	os.RemoveAll(filepath.Join(ConfigDir(), "cache"))
	path, err := B.PreviewFile(jB)
	must(t, err)
	if b, _ := os.ReadFile(path); string(b) != "ID3-mp3-v2" {
		t.Fatalf("B got %q", b)
	}

	// --- plugin transfer with permission ---
	if _, err := B.RequestPlugin("Serum", "VST3", "Xfer Records", "nobody"); err == nil {
		t.Fatal("unknown owner accepted")
	}
	req, err := B.RequestPlugin("Serum", "VST3", "Xfer Records", "tetramino")
	must(t, err)
	if err := B.ApproveRequest(req.ID, "", false); err == nil {
		t.Fatal("requester must not approve own request")
	}
	if _, err := B.InstallRequest(req.ID); err == nil {
		t.Fatal("install before approval must fail")
	}
	A.git().Fetch()
	var pend []PluginRequest
	for _, r := range A.Requests() {
		if r.To == "tetramino" && r.Status == ReqPending {
			pend = append(pend, r)
		}
	}
	if len(pend) != 1 {
		t.Fatalf("A doesn't see the request: %+v", A.Requests())
	}
	ScanInstalled(cfgA, true)
	if err := A.ApproveRequest(req.ID, "", false); err != nil {
		t.Fatal(err, dump(jA))
	}
	bHome := filepath.Join(root, "B-home")
	os.Setenv("HOME", bHome)
	ScanInstalled(cfgB, true)
	dst, err := B.InstallRequest(req.ID)
	if err != nil {
		t.Fatal(err, dump(jB))
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Contents", "Resources", "data.bin")); string(b) != "serum-binary" {
		t.Fatal("plugin content wrong")
	}
	if l, err := os.Readlink(filepath.Join(dst, "Contents", "Current")); err != nil || l != "Resources" {
		t.Fatal("symlink lost", l, err)
	}
	if st, _ := os.Stat(filepath.Join(dst, "Contents", "Resources", "data.bin")); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("exec bit lost")
	}
	if len(gh.assets) != 1 {
		t.Fatalf("transfer file not cleaned up: %d assets", len(gh.assets))
	}
	B.git().Fetch()
	r, _ := B.request(req.ID)
	t.Logf("request final: %+v", *r)
	if r.Status != ReqInstalled {
		t.Fatal("status not installed")
	}
	if FindInstalled(cfgB, "Serum", "VST3") == nil {
		t.Fatal("installed plugin not found by scan")
	}
	t.Log(dump(jB))
}
