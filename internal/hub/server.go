package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Options struct {
	DataDir   string
	PublicURL string // https://example.com — used for the GitHub App setup callbacks
	GitHubAPI string // https://api.github.com (overridable in tests)
	GitHubWeb string // https://github.com
	MaxFile   int64  // largest plugin/installer file (bytes)
	MaxTotal  int64  // all transfer files together (bytes)
	FileTTL   time.Duration
	AdminKey  string
}

type Server struct {
	opt   Options
	st    *Store
	gh    *GitHubApp
	files string
}

func New(opt Options) (*Server, error) {
	if opt.GitHubAPI == "" {
		opt.GitHubAPI = "https://api.github.com"
	}
	if opt.GitHubWeb == "" {
		opt.GitHubWeb = "https://github.com"
	}
	if opt.MaxFile == 0 {
		opt.MaxFile = 1500 << 20
	}
	if opt.MaxTotal == 0 {
		opt.MaxTotal = 7 << 30
	}
	if opt.FileTTL == 0 {
		opt.FileTTL = 72 * time.Hour
	}
	st, err := OpenStore(opt.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{opt: opt, st: st, gh: LoadGitHubApp(opt.DataDir, opt.GitHubAPI), files: filepath.Join(opt.DataDir, "files")}
	os.MkdirAll(s.files, 0o700)
	if s.opt.AdminKey == "" {
		kp := filepath.Join(opt.DataDir, "admin-key")
		if b, err := os.ReadFile(kp); err == nil {
			s.opt.AdminKey = strings.TrimSpace(string(b))
		} else {
			s.opt.AdminKey = newSecret()[:20]
			os.WriteFile(kp, []byte(s.opt.AdminKey), 0o600)
		}
	}
	return s, nil
}

func (s *Server) AdminKey() string { return s.opt.AdminKey }

// GitHubApp exposes the app config (setup / tests).
func (s *Server) GitHubApp() *GitHubApp { return s.gh }

// ---------- helpers ----------

type apiErr struct {
	code int
	msg  string
}

func (e *apiErr) Error() string { return e.msg }

func bad(msg string) error       { return &apiErr{400, msg} }
func forbidden(msg string) error { return &apiErr{403, msg} }
func notFound(msg string) error  { return &apiErr{404, msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

type handler func(w http.ResponseWriter, r *http.Request, me *User) (any, error)

func (s *Server) wrap(auth bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var me *User
		if auth {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			s.st.mu.Lock()
			me = s.st.userByToken(tok)
			if me != nil && now()-me.LastSeen > 20 {
				me.LastSeen = now()
				s.st.save()
			}
			s.st.mu.Unlock()
			if me == nil {
				writeJSON(w, 401, map[string]string{"error": "устройство не зарегистрировано на сервере"})
				return
			}
		}
		res, err := h(w, r, me)
		if err != nil {
			code := 500
			var ae *apiErr
			if errors.As(err, &ae) {
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

func decode(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return bad("неверный запрос")
	}
	return nil
}

// public views

type UserView struct {
	ID       string `json:"id"`
	Nick     string `json:"nick"`
	OS       string `json:"os"`
	LastSeen int64  `json:"lastSeen"`
	Online   bool   `json:"online"`
	Sharing  string `json:"sharing"`
}

func view(u *User) UserView {
	if u == nil {
		return UserView{Nick: "?"}
	}
	sh := u.Sharing
	if sh == "" {
		sh = "ask"
	}
	return UserView{ID: u.ID, Nick: u.Nick, OS: u.OS, LastSeen: u.LastSeen, Online: now()-u.LastSeen < 90, Sharing: sh}
}

type ProjectView struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Repo     string       `json:"repo"`
	CloneURL string       `json:"cloneUrl"`
	Owner    UserView     `json:"owner"`
	Members  []UserView   `json:"members"`
	Invited  []InviteView `json:"invited"`
	IsOwner  bool         `json:"isOwner"`
}

type InviteView struct {
	ID      string   `json:"id"`
	Project string   `json:"project"`
	Name    string   `json:"name"`
	From    UserView `json:"from"`
	To      UserView `json:"to"`
	Created int64    `json:"created"`
}

func (s *Server) projectView(p *Project, me *User) ProjectView {
	v := ProjectView{ID: p.ID, Name: p.Name, Repo: p.Repo, CloneURL: p.CloneURL, Owner: view(s.st.db.Users[p.Owner]), IsOwner: p.Owner == me.ID}
	for _, m := range p.Members {
		v.Members = append(v.Members, view(s.st.db.Users[m]))
	}
	for _, i := range s.st.db.Invites {
		if i.Project == p.ID {
			v.Invited = append(v.Invited, s.inviteView(i))
		}
	}
	return v
}

func (s *Server) inviteView(i *Invite) InviteView {
	name := ""
	if p := s.st.db.Projects[i.Project]; p != nil {
		name = p.Name
	}
	return InviteView{ID: i.ID, Project: i.Project, Name: name, From: view(s.st.db.Users[i.From]), To: view(s.st.db.Users[i.To]), Created: i.Created}
}

type ReqView struct {
	PluginReq
	FromNick string `json:"fromNick"`
	ToNick   string `json:"toNick"`
	ToOS     string `json:"toOs"`
}

func (s *Server) reqView(r *PluginReq) ReqView {
	v := ReqView{PluginReq: *r}
	if u := s.st.db.Users[r.From]; u != nil {
		v.FromNick = u.Nick
	}
	if u := s.st.db.Users[r.To]; u != nil {
		v.ToNick, v.ToOS = u.Nick, u.OS
	}
	return v
}

// ---------- routes ----------

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "service": "prog-uzbeka", "github": s.gh.Ready()})
	})
	mux.HandleFunc("POST /api/register", s.wrap(false, s.register))
	mux.HandleFunc("GET /api/me", s.wrap(true, func(w http.ResponseWriter, r *http.Request, me *User) (any, error) { return view(me), nil }))
	mux.HandleFunc("PATCH /api/me", s.wrap(true, s.updateMe))
	mux.HandleFunc("GET /api/users", s.wrap(true, s.users))
	mux.HandleFunc("PUT /api/inventory", s.wrap(true, s.putInventory))
	mux.HandleFunc("GET /api/users/{id}/inventory", s.wrap(true, s.getInventory))
	mux.HandleFunc("GET /api/projects", s.wrap(true, s.listProjects))
	mux.HandleFunc("POST /api/projects", s.wrap(true, s.createProject))
	mux.HandleFunc("GET /api/projects/{id}/token", s.wrap(true, s.projectToken))
	mux.HandleFunc("POST /api/projects/{id}/invite", s.wrap(true, s.invite))
	mux.HandleFunc("DELETE /api/projects/{id}/members/{uid}", s.wrap(true, s.removeMember))
	mux.HandleFunc("POST /api/invites/{id}/accept", s.wrap(true, s.acceptInvite))
	mux.HandleFunc("POST /api/invites/{id}/decline", s.wrap(true, s.declineInvite))
	mux.HandleFunc("GET /api/inbox", s.wrap(true, s.inbox))
	mux.HandleFunc("GET /api/plugin-requests", s.wrap(true, s.listRequests))
	mux.HandleFunc("GET /api/plugin-requests/{id}", s.wrap(true, s.getRequest))
	mux.HandleFunc("POST /api/plugin-requests", s.wrap(true, s.createRequests))
	mux.HandleFunc("POST /api/plugin-requests/{id}/decline", s.wrap(true, s.declineRequest))
	mux.HandleFunc("POST /api/plugin-requests/{id}/cancel", s.wrap(true, s.cancelRequest))
	mux.HandleFunc("PUT /api/plugin-requests/{id}/file", s.wrap(true, s.uploadFile))
	mux.HandleFunc("GET /api/plugin-requests/{id}/file", s.wrap(true, s.downloadFile))
	mux.HandleFunc("POST /api/plugin-requests/{id}/done", s.wrap(true, s.doneRequest))
	s.setupRoutes(mux)
	return mux
}

// ----- users -----

func validNick(n string) error {
	n = strings.TrimSpace(n)
	if l := utf8.RuneCountInString(n); l < 2 || l > 32 {
		return bad("ник должен быть от 2 до 32 символов")
	}
	if strings.ContainsAny(n, `<>:"/\|?*@`) {
		return bad(`в нике нельзя использовать символы < > : " / \ | ? * @`)
	}
	return nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request, _ *User) (any, error) {
	var req struct{ Nick, OS, Device string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Nick = strings.TrimSpace(req.Nick)
	if err := validNick(req.Nick); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if s.st.userByNick(req.Nick) != nil {
		return nil, bad(fmt.Sprintf("ник «%s» уже занят — выбери другой", req.Nick))
	}
	tok := newSecret()
	u := &User{ID: newID(), Nick: req.Nick, OS: req.OS, Device: req.Device, TokenHash: hashToken(tok), Created: now(), LastSeen: now(), Sharing: "ask"}
	s.st.db.Users[u.ID] = u
	if err := s.st.save(); err != nil {
		return nil, err
	}
	return map[string]any{"token": tok, "user": view(u)}, nil
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Nick    *string `json:"nick"`
		Sharing *string `json:"sharing"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if req.Nick != nil {
		n := strings.TrimSpace(*req.Nick)
		if err := validNick(n); err != nil {
			return nil, err
		}
		if o := s.st.userByNick(n); o != nil && o.ID != me.ID {
			return nil, bad(fmt.Sprintf("ник «%s» уже занят", n))
		}
		me.Nick = n
	}
	if req.Sharing != nil {
		switch *req.Sharing {
		case "off", "ask", "members", "all":
			me.Sharing = *req.Sharing
		default:
			return nil, bad("неизвестный режим")
		}
	}
	return view(me), s.st.save()
}

func (s *Server) users(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := []UserView{}
	for _, u := range sortedUsers(s.st.db.Users) {
		out = append(out, view(u))
	}
	return out, nil
}

func (s *Server) putInventory(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var items []InvItem
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil || json.Unmarshal(b, &items) != nil {
		return nil, bad("неверный список плагинов")
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	me.Inventory, me.InvUpdated = items, now()
	return map[string]int{"count": len(items)}, s.st.save()
}

func (s *Server) getInventory(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	u := s.st.db.Users[r.PathValue("id")]
	if u == nil {
		return nil, notFound("пользователь не найден")
	}
	inv := u.Inventory
	if inv == nil {
		inv = []InvItem{}
	}
	return map[string]any{"user": view(u), "updated": u.InvUpdated, "plugins": inv}, nil
}

// ----- projects -----

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := []ProjectView{}
	for _, p := range s.st.db.Projects {
		if isMember(p, me.ID) {
			out = append(out, s.projectView(p, me))
		}
	}
	return out, nil
}

var translit = map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h",
	'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ы': "y", 'э': "e", 'ю': "yu", 'я': "ya"}

func repoName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		default:
			b.WriteString(translit[r])
		}
	}
	n := strings.Trim(b.String(), "-.")
	if n == "" {
		n = "project"
	}
	return n
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, bad("пустое название")
	}
	if !s.gh.Ready() {
		return nil, &apiErr{503, "сервер ещё не подключён к GitHub — администратору нужно открыть страницу настройки сервера"}
	}
	full, clone, err := s.gh.CreateRepo(repoName(req.Name)+"-"+strings.ToLower(repoName(me.Nick)), "FL Studio проект «"+req.Name+"» (прога узбека)")
	if err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p := &Project{ID: newID(), Name: req.Name, Repo: full, CloneURL: clone, Owner: me.ID, Members: []string{me.ID}, Created: now()}
	s.st.db.Projects[p.ID] = p
	return s.projectView(p, me), s.st.save()
}

func (s *Server) memberProject(r *http.Request, me *User) (*Project, error) {
	p := s.st.db.Projects[r.PathValue("id")]
	if p == nil {
		return nil, notFound("проект не найден")
	}
	if !isMember(p, me.ID) {
		return nil, forbidden("ты не участник этого проекта")
	}
	return p, nil
}

func (s *Server) projectToken(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	p, err := s.memberProject(r, me)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	name := p.Repo
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	t, err := s.gh.RepoToken(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": t.Token, "expiresAt": t.Expires, "cloneUrl": p.CloneURL, "repo": p.Repo}, nil
}

func (s *Server) invite(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct{ Nick string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p, err := s.memberProject(r, me)
	if err != nil {
		return nil, err
	}
	u := s.st.userByNick(req.Nick)
	if u == nil {
		return nil, notFound(fmt.Sprintf("пользователь «%s» не найден", strings.TrimSpace(req.Nick)))
	}
	if isMember(p, u.ID) {
		return nil, bad(u.Nick + " уже в проекте")
	}
	for _, i := range s.st.db.Invites {
		if i.Project == p.ID && i.To == u.ID {
			return s.inviteView(i), nil
		}
	}
	i := &Invite{ID: newID(), Project: p.ID, From: me.ID, To: u.ID, Created: now()}
	s.st.db.Invites[i.ID] = i
	return s.inviteView(i), s.st.save()
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p, err := s.memberProject(r, me)
	if err != nil {
		return nil, err
	}
	uid := r.PathValue("uid")
	if uid == p.Owner {
		return nil, bad("владельца проекта убрать нельзя")
	}
	if uid != me.ID && p.Owner != me.ID {
		return nil, forbidden("убирать участников может только владелец проекта")
	}
	var rest []string
	for _, m := range p.Members {
		if m != uid {
			rest = append(rest, m)
		}
	}
	p.Members = rest
	for id, i := range s.st.db.Invites { // also cancels a pending invitation
		if i.Project == p.ID && i.To == uid {
			delete(s.st.db.Invites, id)
		}
	}
	return map[string]bool{"ok": true}, s.st.save()
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	i := s.st.db.Invites[r.PathValue("id")]
	if i == nil || i.To != me.ID {
		return nil, notFound("приглашение не найдено")
	}
	p := s.st.db.Projects[i.Project]
	delete(s.st.db.Invites, i.ID)
	if p == nil {
		s.st.save()
		return nil, notFound("проект удалён")
	}
	if !isMember(p, me.ID) {
		p.Members = append(p.Members, me.ID)
	}
	return s.projectView(p, me), s.st.save()
}

func (s *Server) declineInvite(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	i := s.st.db.Invites[r.PathValue("id")]
	if i == nil || i.To != me.ID {
		return nil, notFound("приглашение не найдено")
	}
	delete(s.st.db.Invites, i.ID)
	return map[string]bool{"ok": true}, s.st.save()
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := struct {
		Invites  []InviteView `json:"invites"`
		Incoming []ReqView    `json:"incoming"` // asked from me
		Ready    []ReqView    `json:"ready"`    // my requests with a file waiting
		Me       UserView     `json:"me"`
	}{Invites: []InviteView{}, Incoming: []ReqView{}, Ready: []ReqView{}, Me: view(me)}
	for _, i := range s.st.db.Invites {
		if i.To == me.ID {
			out.Invites = append(out.Invites, s.inviteView(i))
		}
	}
	for _, q := range s.st.db.Requests {
		if q.To == me.ID && q.Status == "pending" {
			out.Incoming = append(out.Incoming, s.reqView(q))
		}
		if q.From == me.ID && q.Status == "approved" {
			out.Ready = append(out.Ready, s.reqView(q))
		}
	}
	return out, nil
}

// ----- plugin transfers -----

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := []ReqView{}
	for _, q := range s.st.db.Requests {
		if q.From == me.ID || q.To == me.ID {
			out = append(out, s.reqView(q))
		}
	}
	return out, nil
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q := s.st.db.Requests[r.PathValue("id")]
	if q == nil || (q.From != me.ID && q.To != me.ID) {
		return nil, notFound("запрос не найден")
	}
	return s.reqView(q), nil
}

func (s *Server) createRequests(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		To    string `json:"to"` // nick
		Items []struct {
			Plugin, Format, Vendor string
		} `json:"items"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	to := s.st.userByNick(req.To)
	if to == nil {
		return nil, notFound("пользователь не найден")
	}
	if to.ID == me.ID {
		return nil, bad("нельзя попросить плагин у самого себя")
	}
	if to.Sharing == "off" {
		return nil, forbidden(to.Nick + " запретил скачивать плагины со своего компьютера")
	}
	out := []ReqView{}
	for _, it := range req.Items {
		if strings.TrimSpace(it.Plugin) == "" {
			continue
		}
		dup := false
		for _, q := range s.st.db.Requests {
			if q.From == me.ID && q.To == to.ID && strings.EqualFold(q.Plugin, it.Plugin) && (q.Status == "pending" || q.Status == "approved") {
				dup = true
			}
		}
		if dup {
			continue
		}
		q := &PluginReq{ID: newID(), From: me.ID, To: to.ID, FromOS: me.OS, Plugin: it.Plugin, Format: it.Format, Vendor: it.Vendor,
			Status: "pending", Created: now(), Updated: now()}
		s.st.db.Requests[q.ID] = q
		out = append(out, s.reqView(q))
	}
	return out, s.st.save()
}

func (s *Server) myReq(r *http.Request, me *User, owner bool) (*PluginReq, error) {
	q := s.st.db.Requests[r.PathValue("id")]
	if q == nil {
		return nil, notFound("запрос не найден")
	}
	if (owner && q.To != me.ID) || (!owner && q.From != me.ID) {
		return nil, forbidden("это не твой запрос")
	}
	return q, nil
}

func (s *Server) declineRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct{ Reason string }
	decode(r, &req)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q, err := s.myReq(r, me, true)
	if err != nil {
		return nil, err
	}
	q.Status, q.Note, q.Updated = "declined", strings.TrimSpace(req.Reason), now()
	s.dropFile(q)
	return s.reqView(q), s.st.save()
}

func (s *Server) cancelRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q, err := s.myReq(r, me, false)
	if err != nil {
		return nil, err
	}
	q.Status, q.Updated = "cancelled", now()
	s.dropFile(q)
	return s.reqView(q), s.st.save()
}

func (s *Server) filePath(q *PluginReq) string { return filepath.Join(s.files, q.ID) }

func (s *Server) dropFile(q *PluginReq) { os.Remove(s.filePath(q)) }

func (s *Server) usedSpace() int64 {
	var n int64
	ents, _ := os.ReadDir(s.files)
	for _, e := range ents {
		if i, err := e.Info(); err == nil {
			n += i.Size()
		}
	}
	return n
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	q, err := s.myReq(r, me, true)
	if err == nil && q.Status != "pending" {
		err = bad("запрос уже обработан")
	}
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if r.ContentLength > s.opt.MaxFile {
		return nil, &apiErr{413, fmt.Sprintf("файл больше %d МБ — сервер такой не примет", s.opt.MaxFile>>20)}
	}
	if s.usedSpace()+r.ContentLength > s.opt.MaxTotal {
		return nil, &apiErr{507, "на сервере сейчас не хватает места для передачи — попробуй позже"}
	}
	tmp := s.filePath(q) + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, s.opt.MaxFile+1))
	f.Close()
	if err != nil || n > s.opt.MaxFile {
		os.Remove(tmp)
		if err == nil {
			err = &apiErr{413, "файл слишком большой"}
		}
		return nil, err
	}
	if err := os.Rename(tmp, s.filePath(q)); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q.Status, q.Size, q.Updated = "approved", n, now()
	q.Kind = r.URL.Query().Get("kind")
	q.FileName = filepath.Base(r.URL.Query().Get("name"))
	q.Note = r.URL.Query().Get("note")
	if v := r.URL.Query().Get("format"); v != "" {
		q.Format = v
	}
	return s.reqView(q), s.st.save()
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	q, err := s.myReq(r, me, false)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(s.filePath(q))
	if err != nil {
		return nil, notFound("файла уже нет на сервере")
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	io.Copy(w, f)
	return nil, nil
}

func (s *Server) doneRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		OK   bool   `json:"ok"`
		Note string `json:"note"`
	}
	decode(r, &req)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q, err := s.myReq(r, me, false)
	if err != nil {
		return nil, err
	}
	q.Status, q.Updated = "installed", now()
	if !req.OK {
		q.Status = "failed"
	}
	if req.Note != "" {
		q.Note = req.Note
	}
	s.dropFile(q)
	return s.reqView(q), s.st.save()
}

// Janitor removes transfer files nobody picked up.
func (s *Server) Janitor() {
	for {
		s.st.mu.Lock()
		for _, q := range s.st.db.Requests {
			if q.Status == "approved" && time.Since(time.Unix(q.Updated, 0)) > s.opt.FileTTL {
				s.dropFile(q)
				q.Status, q.Note, q.Updated = "failed", "файл пролежал на сервере слишком долго и удалён — попроси ещё раз", now()
			}
			// forget finished requests after a month
			if q.Status != "pending" && q.Status != "approved" && time.Since(time.Unix(q.Updated, 0)) > 30*24*time.Hour {
				delete(s.st.db.Requests, q.ID)
			}
		}
		s.st.save()
		s.st.mu.Unlock()
		time.Sleep(time.Hour)
	}
}

// ---------- GitHub App setup (one time, by the server owner) ----------

func (s *Server) setupRoutes(mux *http.ServeMux) {
	page := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>прога узбека — сервер</title>
<style>body{font:15px/1.5 system-ui,sans-serif;background:#141518;color:#e6e7ea;max-width:680px;margin:40px auto;padding:0 16px}a{color:#ffbe5c}
input{font:inherit;padding:8px;border-radius:8px;border:1px solid #444;background:#111;color:#eee;width:100%%;box-sizing:border-box}
button{font:inherit;padding:9px 16px;border-radius:8px;border:0;background:#f7a531;font-weight:600;cursor:pointer;margin-top:10px}
.ok{color:#7fe0b0}.muted{color:#8c9099}code{background:#222;padding:1px 5px;border-radius:4px}</style>%s`, body)
	}
	keyOK := func(r *http.Request) bool { return r.URL.Query().Get("key") == s.opt.AdminKey }
	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		if !keyOK(r) {
			page(w, `<h2>Нужен ключ администратора</h2><p class="muted">Открой ссылку, которую сервер напечатал при запуске (или ключ из файла <code>admin-key</code> в папке данных).</p>`)
			return
		}
		st := "не подключён"
		if s.gh.Ready() {
			st = fmt.Sprintf(`<span class="ok">подключён</span> — организация <b>%s</b>, приложение <b>%s</b>`, html.EscapeString(s.gh.Org), html.EscapeString(s.gh.Slug))
		}
		org := html.EscapeString(r.URL.Query().Get("org"))
		if org == "" {
			org = html.EscapeString(s.gh.Org)
		}
		manifest, _ := json.Marshal(map[string]any{
			"name":         "prog-uzbeka-" + newID()[:6],
			"url":          s.opt.PublicURL,
			"redirect_url": s.opt.PublicURL + "/setup/callback",
			"setup_url":    s.opt.PublicURL + "/setup/installed?key=" + s.opt.AdminKey,
			"public":       false,
			"hook_attributes": map[string]any{"url": s.opt.PublicURL + "/setup/hook", "active": false},
			"default_permissions": map[string]string{"contents": "write", "administration": "write", "metadata": "read"},
		})
		page(w, fmt.Sprintf(`<h1>прога узбека — сервер</h1><p>GitHub: %s</p>
<h3>1. Организация на GitHub</h3><p class="muted">Все проекты будут храниться в одной бесплатной организации GitHub. Создай её, если ещё нет: <a href="%s/account/organizations/new?plan=free" target="_blank">создать организацию (Free)</a>.</p>
<form id="f" method="post"><input id="org" placeholder="название организации, напр. uzbek-beats" value="%s">
<input type="hidden" name="manifest" value="%s"><button type="button" onclick="go()">2. Создать GitHub App для этой организации</button></form>
<p class="muted">GitHub попросит подтвердить, затем предложит установить приложение — выбери «All repositories». После этого сервер готов.</p>
<script>function go(){const o=document.getElementById('org').value.trim();if(!o)return alert('Впиши организацию');
fetch('/setup/org?key=%s&org='+encodeURIComponent(o),{method:'POST'}).then(()=>{const f=document.getElementById('f');
f.action='%s/organizations/'+encodeURIComponent(o)+'/settings/apps/new?state=%s';f.submit()})}</script>`,
			st, s.opt.GitHubWeb, org, html.EscapeString(string(manifest)), s.opt.AdminKey, s.opt.GitHubWeb, s.opt.AdminKey))
	})
	mux.HandleFunc("POST /setup/org", func(w http.ResponseWriter, r *http.Request) {
		if !keyOK(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		s.gh.Org = strings.TrimSpace(r.URL.Query().Get("org"))
		s.gh.Save()
	})
	mux.HandleFunc("GET /setup/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != s.opt.AdminKey {
			http.Error(w, "forbidden", 403)
			return
		}
		if err := s.gh.ConvertManifest(r.URL.Query().Get("code")); err != nil {
			page(w, "<h2>Не получилось</h2><p>"+html.EscapeString(err.Error())+"</p>")
			return
		}
		http.Redirect(w, r, s.opt.GitHubWeb+"/apps/"+url.PathEscape(s.gh.Slug)+"/installations/new", http.StatusFound)
	})
	mux.HandleFunc("GET /setup/installed", func(w http.ResponseWriter, r *http.Request) {
		if !keyOK(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		if id, err := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64); err == nil {
			s.gh.InstallationID = id
			s.gh.Save()
		}
		page(w, `<h1 class="ok">Готово ✓</h1><p>Сервер подключён к GitHub. Теперь можно создавать проекты в программе.</p>`)
	})
}
