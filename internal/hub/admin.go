package hub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Администрирование сервера: ключ администратора (data/admin-key), просмотр и удаление пользователей
// и проектов, бэкап базы, переезд на другой сервер и раздача обновлений клиентских приложений.
//
// Всё, что сервер знает, лежит в одной папке данных: db.json (пользователи, проекты, приглашения,
// передачи), server-code, admin-key, settings (переезд), updates/ (сборки клиентов), certs/ (HTTPS),
// backups/ (ежедневные копии db.json). Перенос сервера = перенос этой папки.

var platformRe = regexp.MustCompile(`^(windows|darwin|linux)-(amd64|arm64)$`)

func (s *Server) adminKeyPath() string { return filepath.Join(s.opt.DataDir, "admin-key") }

func (s *Server) loadAdminKey() {
	if s.opt.AdminKey != "" {
		return
	}
	if b, err := os.ReadFile(s.adminKeyPath()); err == nil {
		s.opt.AdminKey = strings.TrimSpace(string(b))
		return
	}
	s.opt.AdminKey = newSecret()[:20]
	os.WriteFile(s.adminKeyPath(), []byte(s.opt.AdminKey+"\n"), 0o600)
}

// AdminKey — ключ для режима разработчика в приложении (админка сервера).
func (s *Server) AdminKey() string { return s.opt.AdminKey }

func (s *Server) admin(h func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		k := r.Header.Get("X-Admin-Key")
		if s.opt.AdminKey == "" || subtle.ConstantTimeCompare([]byte(k), []byte(s.opt.AdminKey)) != 1 {
			time.Sleep(300 * time.Millisecond)
			s.Logf("error", "server", "админка: неверный ключ (%s %s, %s)", r.Method, r.URL.Path, r.RemoteAddr)
			writeJSON(w, 403, map[string]string{"error": "неверный ключ администратора"})
			return
		}
		res, err := h(w, r)
		if err != nil {
			code := 500
			if ae, ok := err.(*apiErr); ok {
				code = ae.code
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		if res != nil {
			writeJSON(w, 200, res)
		}
	}
}

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/overview", s.admin(s.adminOverview))
	mux.HandleFunc("GET /api/admin/logs", s.admin(s.adminLogs))
	mux.HandleFunc("GET /api/admin/logs/file", s.admin(s.adminLogFile))
	mux.HandleFunc("DELETE /api/admin/users/{id}", s.admin(s.adminDeleteUser))
	mux.HandleFunc("DELETE /api/admin/projects/{id}", s.admin(s.adminDeleteProject))
	mux.HandleFunc("DELETE /api/admin/requests", s.admin(s.adminClearRequests))
	mux.HandleFunc("GET /api/admin/backup", s.admin(s.adminBackup))
	mux.HandleFunc("POST /api/admin/move", s.admin(s.adminMove))
	mux.HandleFunc("PUT /api/admin/updates/{platform}", s.admin(s.adminPutUpdate))
	mux.HandleFunc("DELETE /api/admin/updates", s.admin(s.adminClearUpdates))
	mux.HandleFunc("GET /api/updates/latest", s.wrap(true, s.latestUpdate))
	mux.HandleFunc("GET /api/updates/{platform}/download", s.wrap(true, s.downloadUpdate))
}

// ---------- обзор ----------

type AdminUser struct {
	UserView
	Device   string   `json:"device"`
	Created  int64    `json:"created"`
	Projects []string `json:"projects"`
	Plugins  int      `json:"plugins"`
}

type AdminProject struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Repo    string   `json:"repo"`
	Owner   string   `json:"owner"`
	Members []string `json:"members"`
	Created int64    `json:"created"`
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	users := []AdminUser{}
	for _, u := range sortedUsers(s.st.db.Users) {
		au := AdminUser{UserView: view(u), Device: u.Device, Created: u.Created, Projects: []string{}, Plugins: len(u.Inventory)}
		for _, p := range s.st.db.Projects {
			if isMember(p, u.ID) {
				au.Projects = append(au.Projects, p.Name)
			}
		}
		sort.Strings(au.Projects)
		users = append(users, au)
	}
	projects := []AdminProject{}
	for _, p := range s.st.db.Projects {
		ap := AdminProject{ID: p.ID, Name: p.Name, Repo: p.Repo, Owner: view(s.st.db.Users[p.Owner]).Nick, Members: []string{}, Created: p.Created}
		for _, m := range p.Members {
			ap.Members = append(ap.Members, view(s.st.db.Users[m]).Nick)
		}
		projects = append(projects, ap)
	}
	sort.Slice(projects, func(i, j int) bool { return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name) })
	active := 0
	for _, q := range s.st.db.Requests {
		if q.Status == "pending" || q.Status == "approved" || q.Status == "streaming" {
			active++
		}
	}
	var dbSize int64
	if st, err := os.Stat(s.st.path); err == nil {
		dbSize = st.Size()
	}
	return map[string]any{
		"users": users, "projects": projects, "invites": len(s.st.db.Invites),
		"requests": len(s.st.db.Requests), "activeRequests": active,
		"dataDir": s.opt.DataDir, "dbSize": dbSize, "started": s.started.Unix(),
		"code": s.opt.Code, "moveTo": s.st.db.Settings.MoveTo, "update": s.manifest(),
	}, nil
}

// ---------- удаление ----------

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	id := r.PathValue("id")
	u := s.st.db.Users[id]
	if u == nil {
		return nil, notFound("пользователь не найден")
	}
	delete(s.st.db.Users, id)
	removedProjects := 0
	for pid, p := range s.st.db.Projects {
		var keep []string
		for _, m := range p.Members {
			if m != id {
				keep = append(keep, m)
			}
		}
		p.Members = keep
		if p.Owner == id {
			if len(keep) == 0 {
				delete(s.st.db.Projects, pid)
				removedProjects++
				continue
			}
			p.Owner = keep[0] // проект остаётся у участников; репозиторий на GitHub — у прежнего владельца
		}
	}
	for iid, i := range s.st.db.Invites {
		if i.From == id || i.To == id || s.st.db.Projects[i.Project] == nil {
			delete(s.st.db.Invites, iid)
		}
	}
	for qid, q := range s.st.db.Requests {
		if q.From == id || q.To == id {
			delete(s.st.db.Requests, qid)
		}
	}
	return map[string]any{"deleted": u.Nick, "projectsRemoved": removedProjects}, s.st.save()
}

func (s *Server) adminDeleteProject(w http.ResponseWriter, r *http.Request) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	id := r.PathValue("id")
	p := s.st.db.Projects[id]
	if p == nil {
		return nil, notFound("проект не найден")
	}
	delete(s.st.db.Projects, id)
	for iid, i := range s.st.db.Invites {
		if i.Project == id {
			delete(s.st.db.Invites, iid)
		}
	}
	return map[string]any{"deleted": p.Name}, s.st.save()
}

func (s *Server) adminClearRequests(w http.ResponseWriter, r *http.Request) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	n := 0
	for id, q := range s.st.db.Requests {
		if q.Status != "streaming" {
			delete(s.st.db.Requests, id)
			n++
		}
	}
	return map[string]any{"deleted": n}, s.st.save()
}

// ---------- бэкап и переезд ----------

// Backup — всё, что нужно, чтобы поднять сервер в другом месте без потерь.
type Backup struct {
	Format   string `json:"format"`
	Created  int64  `json:"created"`
	Code     string `json:"serverCode"`
	AdminKey string `json:"adminKey"`
	DB       DB     `json:"db"`
}

func (s *Server) backup() Backup {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	b, _ := json.Marshal(s.st.db) // глубокая копия
	var db DB
	json.Unmarshal(b, &db)
	return Backup{Format: "fruityhub-server-backup/1", Created: now(), Code: s.opt.Code, AdminKey: s.opt.AdminKey, DB: db}
}

func (s *Server) adminBackup(w http.ResponseWriter, r *http.Request) (any, error) {
	w.Header().Set("Content-Disposition", `attachment; filename="fruityhub-backup-`+time.Now().Format("2006-01-02")+`.json"`)
	return s.backup(), nil
}

// RestoreBackup кладёт бэкап в папку данных (до запуска сервера). Используется командой -restore.
func RestoreBackup(dataDir, file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var bk Backup
	if err := json.Unmarshal(b, &bk); err != nil || !strings.HasPrefix(bk.Format, "fruityhub-server-backup/") {
		return &apiErr{400, "это не бэкап сервера FruityHub"}
	}
	os.MkdirAll(dataDir, 0o700)
	bk.DB.Settings.MoveTo = "" // на новом месте переезд не нужен
	db, _ := json.MarshalIndent(bk.DB, "", " ")
	if err := os.WriteFile(filepath.Join(dataDir, "db.json"), db, 0o600); err != nil {
		return err
	}
	if bk.Code != "" {
		os.WriteFile(filepath.Join(dataDir, "server-code"), []byte(bk.Code+"\n"), 0o600)
	}
	if bk.AdminKey != "" {
		os.WriteFile(filepath.Join(dataDir, "admin-key"), []byte(bk.AdminKey+"\n"), 0o600)
	}
	return nil
}

// adminMove: сервер переехал. Приложения, которые ещё ходят сюда, сами переключатся на новый адрес
// (если там их узнают — то есть на новый сервер восстановлен бэкап).
func (s *Server) adminMove(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ URL string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	u := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return nil, bad("адрес должен начинаться с https://")
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	s.st.db.Settings.MoveTo = u
	return map[string]string{"moveTo": u}, s.st.save()
}

// dailyBackup — копия db.json раз в сутки в backups/, хранится 14 последних.
func (s *Server) dailyBackup() {
	dir := filepath.Join(s.opt.DataDir, "backups")
	os.MkdirAll(dir, 0o700)
	name := filepath.Join(dir, "db-"+time.Now().Format("2006-01-02")+".json")
	if _, err := os.Stat(name); err == nil {
		return
	}
	b, _ := json.MarshalIndent(s.backup(), "", " ")
	os.WriteFile(name, b, 0o600)
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "db-") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > 14 {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// ---------- обновления приложений ----------

type UpdateFile struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type UpdateManifest struct {
	Version   string                `json:"version"`
	Notes     string                `json:"notes,omitempty"`
	Published int64                 `json:"published"`
	Files     map[string]UpdateFile `json:"files"` // "windows-amd64", "darwin-arm64", "darwin-amd64"
}

func (s *Server) updatesDir() string { return filepath.Join(s.opt.DataDir, "updates") }

func (s *Server) manifest() *UpdateManifest {
	b, err := os.ReadFile(filepath.Join(s.updatesDir(), "manifest.json"))
	if err != nil {
		return nil
	}
	var m UpdateManifest
	if json.Unmarshal(b, &m) != nil || m.Version == "" {
		return nil
	}
	return &m
}

func (s *Server) latestUpdate(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	if m := s.manifest(); m != nil {
		return m, nil
	}
	return map[string]any{"version": ""}, nil
}

func (s *Server) downloadUpdate(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	m := s.manifest()
	pl := r.PathValue("platform")
	if m == nil || !platformRe.MatchString(pl) {
		return nil, notFound("обновления нет")
	}
	f, ok := m.Files[pl]
	if !ok {
		return nil, notFound("для этой системы обновления нет")
	}
	p := filepath.Join(s.updatesDir(), filepath.Base(f.File))
	fh, err := os.Open(p)
	if err != nil {
		return nil, notFound("файл обновления не найден на сервере")
	}
	defer fh.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-SHA256", f.SHA256)
	http.ServeContent(w, r, f.File, time.Unix(m.Published, 0), fh)
	return nil, nil
}

// adminPutUpdate принимает сборку для одной системы: PUT /api/admin/updates/windows-amd64?version=0.7.0&name=FruityHub.exe
func (s *Server) adminPutUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	pl := r.PathValue("platform")
	ver := strings.TrimSpace(r.URL.Query().Get("version"))
	if !platformRe.MatchString(pl) || ver == "" {
		return nil, bad("нужны система (windows-amd64, darwin-arm64…) и version")
	}
	name := filepath.Base(r.URL.Query().Get("name"))
	if name == "." || name == "/" || name == "" {
		name = "update.bin"
	}
	os.MkdirAll(s.updatesDir(), 0o755)
	file := pl + "-" + ver + filepath.Ext(name)
	tmp := filepath.Join(s.updatesDir(), file+".part")
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, 300<<20))
	f.Close()
	if err != nil || n == 0 {
		os.Remove(tmp)
		return nil, bad("файл не дошёл")
	}
	if err := os.Rename(tmp, filepath.Join(s.updatesDir(), file)); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	m := s.manifest()
	if m == nil || m.Version != ver {
		m = &UpdateManifest{Version: ver, Files: map[string]UpdateFile{}}
	}
	if notes := r.URL.Query().Get("notes"); notes != "" {
		m.Notes = notes
	}
	m.Published = now()
	m.Files[pl] = UpdateFile{File: file, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	b, _ := json.MarshalIndent(m, "", " ")
	if err := os.WriteFile(filepath.Join(s.updatesDir(), "manifest.json"), b, 0o644); err != nil {
		return nil, err
	}
	s.pruneUpdates(m)
	return m, nil
}

// pruneUpdates удаляет файлы старых версий (место на сервере ограничено).
func (s *Server) pruneUpdates(m *UpdateManifest) {
	keep := map[string]bool{"manifest.json": true}
	for _, f := range m.Files {
		keep[f.File] = true
	}
	ents, _ := os.ReadDir(s.updatesDir())
	for _, e := range ents {
		if !keep[e.Name()] && !strings.HasSuffix(e.Name(), ".part") {
			os.Remove(filepath.Join(s.updatesDir(), e.Name()))
		}
	}
}

func (s *Server) adminClearUpdates(w http.ResponseWriter, r *http.Request) (any, error) {
	os.RemoveAll(s.updatesDir())
	return map[string]bool{"ok": true}, nil
}
