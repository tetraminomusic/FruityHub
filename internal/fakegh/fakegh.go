// Package fakegh is a tiny stand-in for GitHub used in tests and demos: a
// GitHub App installed on one organization, repos as local bare repositories,
// release assets in memory.
package fakegh

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
	"time"
)

type Asset struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Label string `json:"label"`
	Size  int64  `json:"size"`
}

func (f *Server) Close() { f.Srv.Close() }

// fakeGitHub: a GitHub App installed on one organization. Repos are local bare
// repositories (git and LFS work with file paths), releases are kept in memory.
type Server struct {
	mu     sync.Mutex
	Srv    *httptest.Server
	root   string
	Org    string
	Tokens map[string][]string // installation token -> repos it may touch (nil = all)
	repos  map[string]string   // org/name -> clone url
	rel    map[string]int64    // repo -> release id
	Assets map[int64]*Asset
	owner  map[int64]string // asset -> repo
	blobs  map[int64][]byte
	nextID int64
}

// New starts a fake GitHub (close it with Close).
func New(root string) *Server {
	f := &Server{root: root, Org: "uzbek-org", Tokens: map[string][]string{}, repos: map[string]string{}, rel: map[string]int64{},
		Assets: map[int64]*Asset{}, owner: map[int64]string{}, blobs: map[int64][]byte{}, nextID: 100}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	js := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	if p == "/blob" {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		w.Write(f.blobs[id])
		return
	}
	var body map[string]any
	if !strings.HasPrefix(p, "/upload") {
		json.NewDecoder(r.Body).Decode(&body)
	}
	// app-level calls (JWT)
	if strings.HasPrefix(p, "/app/") {
		if strings.Count(auth, ".") != 2 {
			js(401, map[string]string{"message": "A JSON web token could not be decoded"})
			return
		}
		switch {
		case p == "/app/installations":
			js(200, []map[string]any{{"id": 7, "account": map[string]any{"login": f.Org}}})
		case p == "/app/installations/7/access_tokens":
			tok := fmt.Sprintf("ghs_%d", len(f.Tokens)+1)
			var repos []string
			if rs, ok := body["repositories"].([]any); ok {
				for _, x := range rs {
					repos = append(repos, f.Org+"/"+x.(string))
				}
			}
			f.Tokens[tok] = repos
			js(201, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
		default:
			js(404, map[string]string{"message": "Not Found"})
		}
		return
	}
	scope, ok := f.Tokens[auth]
	if !ok && !strings.HasPrefix(p, "/upload") {
		js(401, map[string]string{"message": "Bad credentials"})
		return
	}
	allowed := func(repo string) bool {
		if scope == nil {
			return true
		}
		for _, s := range scope {
			if s == repo {
				return true
			}
		}
		return false
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case p == "/orgs/"+f.Org+"/repos" && r.Method == "POST":
		name := body["name"].(string)
		full := f.Org + "/" + name
		if _, ok := f.repos[full]; ok {
			js(422, map[string]any{"message": "Repository creation failed.", "errors": []map[string]string{{"message": "name already exists on this account"}}})
			return
		}
		dir := filepath.Join(f.root, "github", f.Org, name+".git")
		os.MkdirAll(filepath.Dir(dir), 0o755)
		exec.Command("git", "init", "-q", "--bare", "-b", "main", dir).Run()
		f.repos[full] = dir
		js(201, map[string]any{"full_name": full, "clone_url": dir})
	case len(parts) >= 3 && parts[0] == "repos":
		repo := parts[1] + "/" + parts[2]
		if !allowed(repo) {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		rest := "/" + strings.Join(parts[3:], "/")
		switch {
		case rest == "/topics":
			js(200, map[string]any{})
		case rest == "/releases" && r.Method == "GET":
			if id := f.rel[repo]; id != 0 {
				js(200, []map[string]any{{"id": id, "tag_name": "flsync-storage", "upload_url": fmt.Sprintf("%s/upload/%d/assets{?name,label}", f.Srv.URL, id)}})
			} else {
				js(200, []any{})
			}
		case rest == "/releases" && r.Method == "POST":
			f.nextID++
			f.rel[repo] = f.nextID
			js(201, map[string]any{"id": f.nextID, "upload_url": fmt.Sprintf("%s/upload/%d/assets{?name,label}", f.Srv.URL, f.nextID)})
		case strings.HasPrefix(rest, "/releases/assets/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(rest, "/releases/assets/"), 10, 64)
			if f.Assets[id] == nil || f.owner[id] != repo {
				js(404, map[string]string{"message": "Not Found"})
				return
			}
			if r.Method == "DELETE" {
				delete(f.Assets, id)
				delete(f.blobs, id)
				w.WriteHeader(204)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("%s/blob?id=%d", strings.Replace(f.Srv.URL, "127.0.0.1", "localhost", 1), id), 302)
		case strings.HasPrefix(rest, "/releases/") && strings.HasSuffix(rest, "/assets"):
			list := []*Asset{}
			for id, a := range f.Assets {
				if f.owner[id] == repo {
					list = append(list, a)
				}
			}
			js(200, list)
		default:
			js(404, map[string]string{"message": "unknown " + r.Method + " " + p})
		}
	case len(parts) == 3 && parts[0] == "upload":
		if _, ok := f.Tokens[auth]; !ok {
			js(401, map[string]string{"message": "Bad credentials"})
			return
		}
		relID, _ := strconv.ParseInt(parts[1], 10, 64)
		repo := ""
		for k, v := range f.rel {
			if v == relID {
				repo = k
			}
		}
		b, _ := io.ReadAll(r.Body)
		f.nextID++
		a := &Asset{ID: f.nextID, Name: r.URL.Query().Get("name"), Label: r.URL.Query().Get("label"), Size: int64(len(b))}
		f.Assets[a.ID], f.blobs[a.ID], f.owner[a.ID] = a, b, repo
		js(201, a)
	default:
		js(404, map[string]string{"message": "unknown " + r.Method + " " + p})
	}
}
