// Package fakegh is a tiny stand-in for GitHub used in tests and demos:
// users with tokens, personal repos (local bare repositories — git and LFS
// work with file paths), collaborators and invitations, release assets.
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
)

type Asset struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Label string `json:"label"`
	Size  int64  `json:"size"`
}

type repo struct {
	owner, name, dir string
	collab           map[string]bool
	relID            int64
	topics           []string
}

type invitation struct {
	ID      int64
	Repo    *repo
	Inviter string
	Invitee string
}

type Server struct {
	mu     sync.Mutex
	Srv    *httptest.Server
	root   string
	Tokens map[string]string // token -> login
	// NoDeleteScope: у этих логинов токен без галочки delete_repo
	NoDeleteScope map[string]bool
	repos         map[string]*repo // owner/name (lower case)
	invs          map[int64]*invitation
	Assets        map[int64]*Asset
	owner         map[int64]*repo
	blobs         map[int64][]byte
	nextID        int64
}

// New starts a fake GitHub (close it with Close).
func New(root string) *Server {
	f := &Server{root: root, Tokens: map[string]string{}, NoDeleteScope: map[string]bool{}, repos: map[string]*repo{}, invs: map[int64]*invitation{},
		Assets: map[int64]*Asset{}, owner: map[int64]*repo{}, blobs: map[int64][]byte{}, nextID: 100}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *Server) Close() { f.Srv.Close() }

// AddUser makes a GitHub account with a token.
func (f *Server) AddUser(token, login string) {
	f.mu.Lock()
	f.Tokens[token] = login
	f.mu.Unlock()
}

func (f *Server) Collaborators(full string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	if r := f.repos[strings.ToLower(full)]; r != nil {
		for c := range r.collab {
			out = append(out, c)
		}
	}
	return out
}

func (f *Server) repoJSON(r *repo) map[string]any {
	topics := r.topics
	if topics == nil {
		topics = []string{}
	}
	return map[string]any{"full_name": r.owner + "/" + r.name, "name": r.name, "clone_url": r.dir, "private": true,
		"html_url": "https://github.com/" + r.owner + "/" + r.name, "owner": map[string]any{"login": r.owner},
		"topics": topics, "size": 10, "pushed_at": "2026-10-20T10:00:00Z"}
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
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
	login := f.Tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if login == "" {
		js(401, map[string]string{"message": "Bad credentials"})
		return
	}
	var body map[string]any
	if !strings.HasPrefix(p, "/upload") {
		json.NewDecoder(r.Body).Decode(&body)
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	canSee := func(rp *repo) bool { return strings.EqualFold(rp.owner, login) || rp.collab[strings.ToLower(login)] }
	switch {
	case p == "/user":
		js(200, map[string]any{"login": login})
	case p == "/user/repos" && r.Method == "POST":
		name := body["name"].(string)
		key := strings.ToLower(login + "/" + name)
		if f.repos[key] != nil {
			js(422, map[string]any{"message": "Repository creation failed.", "errors": []map[string]string{{"message": "name already exists on this account"}}})
			return
		}
		dir := filepath.Join(f.root, "github", login, name+".git")
		os.MkdirAll(filepath.Dir(dir), 0o755)
		exec.Command("git", "init", "-q", "--bare", "-b", "main", dir).Run()
		rp := &repo{owner: login, name: name, dir: dir, collab: map[string]bool{}}
		f.repos[key] = rp
		js(201, f.repoJSON(rp))
	case p == "/user/repos" && r.Method == "GET":
		out := []map[string]any{}
		if r.URL.Query().Get("page") == "1" || r.URL.Query().Get("page") == "" {
			for _, rp := range f.repos {
				if strings.EqualFold(rp.owner, login) {
					out = append(out, f.repoJSON(rp))
				}
			}
		}
		js(200, out)
	case p == "/user/repository_invitations" && r.Method == "GET":
		out := []map[string]any{}
		for _, iv := range f.invs {
			if strings.EqualFold(iv.Invitee, login) {
				out = append(out, map[string]any{"id": iv.ID, "repository": f.repoJSON(iv.Repo), "inviter": map[string]any{"login": iv.Inviter}})
			}
		}
		js(200, out)
	case len(parts) == 3 && parts[0] == "user" && parts[1] == "repository_invitations" && r.Method == "PATCH":
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		iv := f.invs[id]
		if iv == nil || !strings.EqualFold(iv.Invitee, login) {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		iv.Repo.collab[strings.ToLower(login)] = true
		delete(f.invs, id)
		w.WriteHeader(204)
	case len(parts) == 3 && parts[0] == "upload":
		relID, _ := strconv.ParseInt(parts[1], 10, 64)
		var rp *repo
		for _, x := range f.repos {
			if x.relID == relID {
				rp = x
			}
		}
		if rp == nil || !canSee(rp) {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.nextID++
		a := &Asset{ID: f.nextID, Name: r.URL.Query().Get("name"), Label: r.URL.Query().Get("label"), Size: int64(len(b))}
		f.Assets[a.ID], f.blobs[a.ID], f.owner[a.ID] = a, b, rp
		js(201, a)
	case len(parts) >= 3 && parts[0] == "repos":
		rp := f.repos[strings.ToLower(parts[1]+"/"+parts[2])]
		if rp == nil || !canSee(rp) {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		isOwner := strings.EqualFold(rp.owner, login)
		rest := "/" + strings.Join(parts[3:], "/")
		switch {
		case rest == "/" && r.Method == "GET":
			m := f.repoJSON(rp)
			m["permissions"] = map[string]bool{"admin": isOwner, "push": true}
			js(200, m)
		case rest == "/" && r.Method == "DELETE":
			if !isOwner {
				js(403, map[string]string{"message": "Must have admin rights to Repository."})
				return
			}
			if f.NoDeleteScope[strings.ToLower(login)] {
				js(403, map[string]string{"message": "Must have admin rights to Repository."})
				return
			}
			delete(f.repos, strings.ToLower(rp.owner+"/"+rp.name))
			os.RemoveAll(rp.dir)
			w.WriteHeader(204)
		case rest == "/topics":
			if r.Method == "PUT" {
				if names, ok := body["names"].([]any); ok {
					rp.topics = nil
					for _, n := range names {
						rp.topics = append(rp.topics, fmt.Sprint(n))
					}
				}
			}
			js(200, map[string]any{})
		case rest == "/collaborators" && r.Method == "GET":
			out := []map[string]any{{"login": rp.owner, "permissions": map[string]bool{"admin": true}}}
			for c := range rp.collab {
				out = append(out, map[string]any{"login": c, "permissions": map[string]bool{"admin": false}})
			}
			js(200, out)
		case strings.HasPrefix(rest, "/collaborators/"):
			who := strings.ToLower(strings.TrimPrefix(rest, "/collaborators/"))
			if !isOwner && !(r.Method == "DELETE" && who == strings.ToLower(login)) {
				js(403, map[string]string{"message": "Must have admin rights to Repository."})
				return
			}
			if r.Method == "DELETE" {
				delete(rp.collab, who)
				for id, iv := range f.invs {
					if iv.Repo == rp && strings.EqualFold(iv.Invitee, who) {
						delete(f.invs, id)
					}
				}
				w.WriteHeader(204)
				return
			}
			if rp.collab[who] {
				w.WriteHeader(204)
				return
			}
			f.nextID++
			f.invs[f.nextID] = &invitation{ID: f.nextID, Repo: rp, Inviter: login, Invitee: who}
			js(201, map[string]any{"id": f.nextID})
		case rest == "/invitations" && r.Method == "GET":
			out := []map[string]any{}
			for _, iv := range f.invs {
				if iv.Repo == rp {
					out = append(out, map[string]any{"id": iv.ID, "invitee": map[string]any{"login": iv.Invitee}})
				}
			}
			js(200, out)
		case rest == "/releases" && r.Method == "GET":
			if rp.relID != 0 {
				js(200, []map[string]any{{"id": rp.relID, "tag_name": "flsync-storage", "upload_url": fmt.Sprintf("%s/upload/%d/assets{?name,label}", f.Srv.URL, rp.relID)}})
			} else {
				js(200, []any{})
			}
		case rest == "/releases" && r.Method == "POST":
			f.nextID++
			rp.relID = f.nextID
			js(201, map[string]any{"id": f.nextID, "upload_url": fmt.Sprintf("%s/upload/%d/assets{?name,label}", f.Srv.URL, f.nextID)})
		case strings.HasPrefix(rest, "/releases/assets/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(rest, "/releases/assets/"), 10, 64)
			if f.Assets[id] == nil || f.owner[id] != rp {
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
				if f.owner[id] == rp {
					list = append(list, a)
				}
			}
			js(200, list)
		default:
			js(404, map[string]string{"message": "unknown " + r.Method + " " + p})
		}
	default:
		js(404, map[string]string{"message": "unknown " + r.Method + " " + p})
	}
}
