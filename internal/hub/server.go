package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Options struct {
	DataDir    string
	GitHubAPI  string        // https://api.github.com (overridable in tests)
	Code       string        // server code needed to register (empty: read/generate data/server-code)
	StreamWait time.Duration // how long one side of a plugin transfer waits for the other
	AdminKey   string        // ключ администратора (пусто: data/admin-key, создаётся сам)
}

type Server struct {
	opt Options
	st  *Store

	relayMu sync.Mutex
	relays  map[string]*relay
	negoMu  sync.Mutex
	negos   map[string]*nego
	log     *serverLog
	started time.Time
}

func New(opt Options) (*Server, error) {
	if opt.GitHubAPI == "" {
		opt.GitHubAPI = "https://api.github.com"
	}
	if opt.StreamWait == 0 {
		opt.StreamWait = 2 * time.Minute
	}
	st, err := OpenStore(opt.DataDir)
	if err != nil {
		return nil, err
	}
	if opt.Code == "" {
		p := filepath.Join(opt.DataDir, "server-code")
		if b, err := os.ReadFile(p); err == nil {
			opt.Code = strings.TrimSpace(string(b))
		} else {
			opt.Code = newSecret()[:8]
			os.WriteFile(p, []byte(opt.Code+"\n"), 0o600)
		}
	}
	srv := &Server{opt: opt, st: st, relays: map[string]*relay{}, started: time.Now(), log: newServerLog(opt.DataDir)}
	srv.loadAdminKey()
	return srv, nil
}

// Code is what a new user types at registration (keeps strangers out).
func (s *Server) Code() string { return s.opt.Code }

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
				s.Logf("info", "—", "%s %s → 401 (устройство не зарегистрировано, %s)", r.Method, r.URL.Path, r.RemoteAddr)
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
			who := "—"
			if me != nil {
				s.st.mu.Lock()
				who = view(me).Nick
				s.st.mu.Unlock()
			}
			level := "error"
			if code == 408 || code == 410 || code == 409 {
				level = "info" // «получатель не в сети», «отменили», «занято» — это не поломка
			}
			s.Logf(level, who, "%s %s → %d: %s", r.Method, r.URL.Path, code, err.Error())
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		if res != nil {
			writeJSON(w, 200, res)
		}
	}
}

func decode(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
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

// ---------- public views ----------

type UserView struct {
	ID       string `json:"id"`
	Nick     string `json:"nick"`
	GitHub   string `json:"github"`
	OS       string `json:"os"`
	LastSeen int64  `json:"lastSeen"`
	Online   bool   `json:"online"`
	Sharing  string `json:"sharing"`
}

func online(u *User) bool { return u != nil && now()-u.LastSeen < 60 }

func view(u *User) UserView {
	if u == nil {
		return UserView{Nick: "?"}
	}
	sh := u.Sharing
	if sh == "" {
		sh = "ask"
	}
	return UserView{ID: u.ID, Nick: u.Nick, GitHub: u.GitHub, OS: u.OS, LastSeen: u.LastSeen, Online: online(u), Sharing: sh}
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
	Lock     *LockView    `json:"lock"`
}

type InviteView struct {
	ID       string   `json:"id"`
	Project  string   `json:"project"`
	Name     string   `json:"name"`
	Repo     string   `json:"repo"`
	CloneURL string   `json:"cloneUrl"`
	From     UserView `json:"from"`
	To       UserView `json:"to"`
	Created  int64    `json:"created"`
}

func (s *Server) projectView(p *Project, me *User) ProjectView {
	v := ProjectView{ID: p.ID, Name: p.Name, Repo: p.Repo, CloneURL: p.CloneURL, Owner: view(s.st.db.Users[p.Owner]),
		IsOwner: p.Owner == me.ID, Members: []UserView{}, Invited: []InviteView{}, Lock: s.lockView(p.Lock)}
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
	v := InviteView{ID: i.ID, Project: i.Project, From: view(s.st.db.Users[i.From]), To: view(s.st.db.Users[i.To]), Created: i.Created}
	if p := s.st.db.Projects[i.Project]; p != nil {
		v.Name, v.Repo, v.CloneURL = p.Name, p.Repo, p.CloneURL
	}
	return v
}

type ReqView struct {
	PluginReq
	FromNick   string `json:"fromNick"`
	FromOnline bool   `json:"fromOnline"`
	ToNick     string `json:"toNick"`
	ToOS       string `json:"toOs"`
	ToOnline   bool   `json:"toOnline"`
	OfferSeq   int    `json:"offerSeq,omitempty"` // отправитель ждёт ответа: какие файлы зависимостей уже есть
}

func (s *Server) reqView(r *PluginReq) ReqView {
	v := ReqView{PluginReq: *r, OfferSeq: s.offerSeq(r.ID)}
	if u := s.st.db.Users[r.From]; u != nil {
		v.FromNick, v.FromOnline = u.Nick, online(u)
	}
	if u := s.st.db.Users[r.To]; u != nil {
		v.ToNick, v.ToOS, v.ToOnline = u.Nick, u.OS, online(u)
	}
	return v
}

// ---------- routes ----------

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "service": "prog-uzbeka", "name": "FruityHub", "version": 4})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.st.mu.Lock()
		n := len(s.st.db.Users)
		s.st.mu.Unlock()
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>FruityHub — сервер</title><body style="font:16px system-ui;background:#141518;color:#eee;padding:40px">
<h1>FruityHub — сервер работает</h1><p>Пользователей: %d. Этот адрес вписывается в FruityHub при регистрации.</p>`, n)
	})
	mux.HandleFunc("POST /api/register", s.wrap(false, s.register))
	mux.HandleFunc("GET /api/me", s.wrap(true, func(w http.ResponseWriter, r *http.Request, me *User) (any, error) { return view(me), nil }))
	mux.HandleFunc("PATCH /api/me", s.wrap(true, s.updateMe))
	mux.HandleFunc("GET /api/users", s.wrap(true, s.users))
	mux.HandleFunc("PUT /api/inventory", s.wrap(true, s.putInventory))
	mux.HandleFunc("GET /api/users/{id}/inventory", s.wrap(true, s.getInventory))
	mux.HandleFunc("GET /api/projects", s.wrap(true, s.listProjects))
	mux.HandleFunc("POST /api/projects", s.wrap(true, s.addProject))
	mux.HandleFunc("POST /api/projects/discover", s.wrap(true, s.discover))
	mux.HandleFunc("POST /api/projects/{id}/sync-members", s.wrap(true, s.syncMembers))
	mux.HandleFunc("DELETE /api/projects/{id}", s.wrap(true, s.deleteProject))
	mux.HandleFunc("POST /api/projects/{id}/invite", s.wrap(true, s.invite))
	mux.HandleFunc("DELETE /api/projects/{id}/members/{uid}", s.wrap(true, s.removeMember))
	mux.HandleFunc("POST /api/invites/{id}/accept", s.wrap(true, s.acceptInvite))
	mux.HandleFunc("POST /api/invites/{id}/decline", s.wrap(true, s.declineInvite))
	mux.HandleFunc("GET /api/inbox", s.wrap(true, s.inbox))
	mux.HandleFunc("GET /api/plugin-requests", s.wrap(true, s.listRequests))
	mux.HandleFunc("GET /api/plugin-requests/{id}", s.wrap(true, s.getRequest))
	mux.HandleFunc("POST /api/plugin-requests", s.wrap(true, s.createRequests))
	mux.HandleFunc("POST /api/plugin-requests/{id}/approve", s.wrap(true, s.approveRequest))
	mux.HandleFunc("POST /api/plugin-requests/{id}/decline", s.wrap(true, s.declineRequest))
	mux.HandleFunc("POST /api/plugin-requests/{id}/cancel", s.wrap(true, s.cancelRequest))
	mux.HandleFunc("POST /api/plugin-requests/{id}/retry", s.wrap(true, s.retryRequest))
	mux.HandleFunc("POST /api/log", s.wrap(true, s.clientLog))
	mux.HandleFunc("PUT /api/plugin-requests/{id}/offer", s.wrap(true, s.putOffer))
	mux.HandleFunc("GET /api/plugin-requests/{id}/offer", s.wrap(true, s.getOffer))
	mux.HandleFunc("PUT /api/plugin-requests/{id}/have", s.wrap(true, s.putHave))
	mux.HandleFunc("GET /api/plugin-requests/{id}/have", s.wrap(true, s.getHave))
	mux.HandleFunc("POST /api/projects/{id}/lock", s.wrap(true, s.takeLock))
	mux.HandleFunc("DELETE /api/projects/{id}/lock", s.wrap(true, s.releaseLock))
	mux.HandleFunc("POST /api/plugin-requests/{id}/done", s.wrap(true, s.doneRequest))
	mux.HandleFunc("PUT /api/plugin-requests/{id}/stream", s.wrap(true, s.streamSend))
	mux.HandleFunc("GET /api/plugin-requests/{id}/stream", s.wrap(true, s.streamRecv))
	s.adminRoutes(mux)
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

// githubLogin asks GitHub whose token this is. The token is not stored.
func (s *Server) githubLogin(token string) (string, error) {
	req, _ := http.NewRequest("GET", s.opt.GitHubAPI+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", &apiErr{502, "сервер не смог связаться с GitHub"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return "", bad("GitHub не принял токен — проверь, что он скопирован целиком и не истёк")
	}
	var u struct {
		Login string `json:"login"`
	}
	if resp.StatusCode >= 300 || json.NewDecoder(resp.Body).Decode(&u) != nil || u.Login == "" {
		return "", &apiErr{502, "GitHub ответил ошибкой " + resp.Status}
	}
	return u.Login, nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request, _ *User) (any, error) {
	var req struct{ Nick, OS, Device, Code, GitHubToken string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if s.opt.Code != "" && strings.TrimSpace(req.Code) != s.opt.Code {
		return nil, forbidden("неверный код сервера — спроси его у того, кто поднял сервер")
	}
	req.Nick = strings.TrimSpace(req.Nick)
	if err := validNick(req.Nick); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	taken := s.st.userByNick(req.Nick) != nil
	s.st.mu.Unlock()
	if taken {
		return nil, bad(fmt.Sprintf("ник «%s» уже занят — выбери другой", req.Nick))
	}
	if strings.TrimSpace(req.GitHubToken) == "" {
		return nil, bad("нужен GitHub-токен")
	}
	login, err := s.githubLogin(strings.TrimSpace(req.GitHubToken))
	if err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if s.st.userByNick(req.Nick) != nil {
		return nil, bad(fmt.Sprintf("ник «%s» уже занят — выбери другой", req.Nick))
	}
	tok := newSecret()
	u := &User{ID: newID(), Nick: req.Nick, GitHub: login, OS: req.OS, Device: req.Device, TokenHash: hashToken(tok), Created: now(), LastSeen: now(), Sharing: "ask"}
	s.st.db.Users[u.ID] = u
	if err := s.st.save(); err != nil {
		return nil, err
	}
	return map[string]any{"token": tok, "user": view(u)}, nil
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Nick        *string `json:"nick"`
		Sharing     *string `json:"sharing"`
		GitHubToken *string `json:"githubToken"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	login := ""
	if req.GitHubToken != nil {
		l, err := s.githubLogin(strings.TrimSpace(*req.GitHubToken))
		if err != nil {
			return nil, err
		}
		login = l
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
	if login != "" {
		me.GitHub = login
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

// ----- projects (the repos themselves live on the owners' GitHub) -----

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

// addProject registers a GitHub repo the caller created (or owns) as a project.
func (s *Server) addProject(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct{ Name, Repo, CloneURL string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Name, req.Repo = strings.TrimSpace(req.Name), strings.TrimSpace(req.Repo)
	if req.Name == "" || req.Repo == "" || !strings.Contains(req.Repo, "/") {
		return nil, bad("нужны название и репозиторий")
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if p := s.st.projectByRepo(req.Repo); p != nil {
		if isMember(p, me.ID) {
			return s.projectView(p, me), nil
		}
		return nil, &apiErr{409, "этот репозиторий уже подключил " + view(s.st.db.Users[p.Owner]).Nick + " — попроси его пригласить тебя"}
	}
	p := &Project{ID: newID(), Name: req.Name, Repo: req.Repo, CloneURL: req.CloneURL, Owner: me.ID, Members: []string{me.ID}, Created: now()}
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

// syncMembers: the owner reports who already has access on GitHub; registered
// users with those GitHub logins become members.
// discover добавляет пользователя во все проекты, к репозиториям которых у него уже есть доступ на GitHub:
// проекты его же GitHub-аккаунта (зашёл с другого устройства) и репозитории, где он коллаборатор.
// Доступ проверяется токеном пользователя (сам токен сервер не хранит).
func (s *Server) discover(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct{ GitHubToken string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GitHubToken) == "" || me.GitHub == "" {
		return nil, bad("нужен GitHub-токен")
	}
	login, err := s.githubLogin(req.GitHubToken)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(login, me.GitHub) {
		return nil, bad("токен от другого GitHub-аккаунта (" + login + "), чем при регистрации — обнови токен в настройках")
	}
	type cand struct{ id, repo string }
	var direct, check []cand
	s.st.mu.Lock()
	for _, p := range s.st.db.Projects {
		if isMember(p, me.ID) {
			continue
		}
		same := strings.EqualFold(strings.SplitN(p.Repo, "/", 2)[0], login)
		for _, m := range p.Members {
			if u := s.st.db.Users[m]; u != nil && strings.EqualFold(u.GitHub, login) {
				same = true
			}
		}
		if same {
			direct = append(direct, cand{p.ID, p.Repo})
		} else {
			check = append(check, cand{p.ID, p.Repo})
		}
	}
	s.st.mu.Unlock()
	tok := strings.TrimSpace(req.GitHubToken)
	for _, c := range check { // чужой репозиторий — спрашиваем GitHub, видит ли его этот токен
		hr, _ := http.NewRequest("GET", s.opt.GitHubAPI+"/repos/"+c.repo, nil)
		hr.Header.Set("Authorization", "Bearer "+tok)
		hr.Header.Set("Accept", "application/vnd.github+json")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(hr)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			direct = append(direct, c)
		}
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	added := []ProjectView{}
	for _, c := range direct {
		p := s.st.db.Projects[c.id]
		if p == nil || isMember(p, me.ID) {
			continue
		}
		p.Members = append(p.Members, me.ID)
		for id, i := range s.st.db.Invites {
			if i.Project == p.ID && i.To == me.ID {
				delete(s.st.db.Invites, id)
			}
		}
		added = append(added, s.projectView(p, me))
	}
	if len(added) == 0 {
		return added, nil
	}
	return added, s.st.save()
}

// deleteProject: владелец убирает проект с сервера (например, удалил репозиторий на GitHub).
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p, err := s.memberProject(r, me)
	if err != nil {
		return nil, err
	}
	if p.Owner != me.ID {
		return nil, forbidden("удалить проект может только владелец")
	}
	delete(s.st.db.Projects, p.ID)
	for id, i := range s.st.db.Invites {
		if i.Project == p.ID {
			delete(s.st.db.Invites, id)
		}
	}
	return map[string]bool{"ok": true}, s.st.save()
}

func (s *Server) syncMembers(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct{ Logins []string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p, err := s.memberProject(r, me)
	if err != nil {
		return nil, err
	}
	if p.Owner != me.ID {
		return nil, forbidden("это может только владелец проекта")
	}
	for _, l := range req.Logins {
		for _, u := range s.st.usersByGitHub(l) {
			if !isMember(p, u.ID) {
				p.Members = append(p.Members, u.ID)
			}
		}
	}
	return s.projectView(p, me), s.st.save()
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
	return map[string]any{"ok": true, "github": view(s.st.db.Users[uid]).GitHub}, s.st.save()
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
		Invites  []InviteView         `json:"invites"`
		Incoming []ReqView            `json:"incoming"` // asked from me, waiting for my answer
		ToSend   []ReqView            `json:"toSend"`   // I allowed it: my app streams it when the other side is online
		Ready    []ReqView            `json:"ready"`    // my requests that were allowed: my app receives them
		Me       UserView             `json:"me"`
		MoveTo   string               `json:"moveTo,omitempty"` // сервер переехал — приложение переключится само
		Update   string               `json:"update,omitempty"` // последняя версия приложения на сервере
		Locks    map[string]*LockView `json:"locks"`            // кто какой проект взял в работу (id проекта → замок, null — свободен)
	}{Invites: []InviteView{}, Incoming: []ReqView{}, ToSend: []ReqView{}, Ready: []ReqView{}, Me: view(me), MoveTo: s.st.db.Settings.MoveTo, Locks: s.myLocks(me)}
	if m := s.manifest(); m != nil {
		out.Update = m.Version
	}
	for _, i := range s.st.db.Invites {
		if i.To == me.ID {
			out.Invites = append(out.Invites, s.inviteView(i))
		}
	}
	for _, q := range s.st.db.Requests {
		active := q.Status == "approved" || q.Status == "streaming"
		switch {
		case q.To == me.ID && q.Status == "pending":
			out.Incoming = append(out.Incoming, s.reqView(q))
		case q.To == me.ID && active:
			out.ToSend = append(out.ToSend, s.reqView(q))
		case q.From == me.ID && active:
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
			Reinstall              bool
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
			if q.From == me.ID && q.To == to.ID && strings.EqualFold(q.Plugin, it.Plugin) && (q.Status == "pending" || q.Status == "approved" || q.Status == "streaming") {
				dup = true
			}
		}
		if dup {
			continue
		}
		q := &PluginReq{ID: newID(), From: me.ID, To: to.ID, FromOS: me.OS, Plugin: it.Plugin, Format: it.Format, Vendor: it.Vendor,
			Status: "pending", Created: now(), Updated: now(), Reinstall: it.Reinstall}
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

// approveRequest: the owner says yes and what will come (plugin or installer).
func (s *Server) approveRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Kind, Name, Note, Format string
		Size                     int64
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q, err := s.myReq(r, me, true)
	if err != nil {
		return nil, err
	}
	if q.Status != "pending" && q.Status != "approved" {
		return nil, bad("запрос уже обработан")
	}
	q.Status, q.Kind, q.FileName, q.Note, q.Size, q.Updated = "approved", req.Kind, filepath.Base(req.Name), req.Note, req.Size, now()
	if req.Format != "" {
		q.Format = req.Format
	}
	return s.reqView(q), s.st.save()
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
	return s.reqView(q), s.st.save()
}

// cancelRequest: отменить может любая сторона — и пока ждём, и посреди передачи (поток обрывается).
func (s *Server) cancelRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q := s.st.db.Requests[r.PathValue("id")]
	if q == nil || (q.From != me.ID && q.To != me.ID) {
		return nil, notFound("запрос не найден")
	}
	if q.Status != "pending" && q.Status != "approved" && q.Status != "streaming" {
		return s.reqView(q), nil
	}
	q.Status, q.Updated = "cancelled", now()
	q.Note = "отменил " + view(me).Nick
	s.Logf("info", view(me).Nick, "отменил передачу %s", q.Plugin)
	s.stopRelay(q.ID)
	s.dropNego(q.ID)
	return s.reqView(q), s.st.save()
}

// retryRequest: передать ещё раз (после ошибки или отмены) — любая сторона.
// Если владелец уже разрешал — сразу снова «разрешён», и передача начнётся сама, когда оба в сети.
func (s *Server) retryRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Reinstall *bool `json:"reinstall"`
	}
	decode(r, &req)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q := s.st.db.Requests[r.PathValue("id")]
	if q == nil || (q.From != me.ID && q.To != me.ID) {
		return nil, notFound("запрос не найден")
	}
	switch q.Status {
	case "failed", "cancelled", "installed":
	default:
		return nil, bad("передача ещё идёт или ждёт ответа")
	}
	if q.Kind != "" {
		q.Status = "approved"
	} else {
		q.Status = "pending"
	}
	q.Note, q.Updated = "", now()
	if req.Reinstall != nil {
		q.Reinstall = *req.Reinstall
	}
	q.Partial = false
	s.dropNego(q.ID)
	return s.reqView(q), s.st.save()
}

// stopRelay обрывает идущую передачу этого запроса (вызывать под s.st.mu).
func (s *Server) stopRelay(id string) {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	if rl := s.relays[id]; rl != nil {
		rl.stopOnce.Do(func() { close(rl.stop) })
		delete(s.relays, id)
	}
}

func (s *Server) doneRequest(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		OK      bool   `json:"ok"`
		Partial bool   `json:"partial"`
		Note    string `json:"note"`
	}
	decode(r, &req)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	q, err := s.myReq(r, me, false)
	if err != nil {
		return nil, err
	}
	if q.Status == "cancelled" { // отменили — оборванная передача не превращается в «ошибку»
		return s.reqView(q), nil
	}
	q.Status, q.Updated, q.Partial = "installed", now(), req.OK && req.Partial
	s.dropNego(q.ID)
	if req.OK {
		s.Logf("info", view(me).Nick, "установил %s от %s %s", q.Plugin, view(s.st.db.Users[q.To]).Nick, req.Note)
	} else {
		s.Logf("error", view(me).Nick, "не установился %s от %s: %s", q.Plugin, view(s.st.db.Users[q.To]).Nick, req.Note)
	}
	if !req.OK {
		q.Status = "failed"
	}
	if req.Note != "" {
		q.Note = req.Note
	}
	return s.reqView(q), s.st.save()
}

// ---------- the relay: bytes go from the owner's computer straight to the
// requester's computer; nothing is written to the server's disk ----------

type recvSide struct {
	w    http.ResponseWriter
	done chan error
}

type relay struct {
	recv     chan *recvSide // capacity 1: the waiting receiver
	stop     chan struct{}  // закрывается при отмене — поток обрывается
	stopOnce sync.Once
}

// stopReader перестаёт читать, как только передачу отменили.
type stopReader struct {
	r    io.Reader
	stop chan struct{}
}

func (s stopReader) Read(p []byte) (int, error) {
	select {
	case <-s.stop:
		return 0, errors.New("передачу отменили")
	default:
	}
	return s.r.Read(p)
}

func (s *Server) relayFor(id string) *relay {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	rl := s.relays[id]
	if rl == nil {
		rl = &relay{recv: make(chan *recvSide, 1), stop: make(chan struct{})}
		s.relays[id] = rl
	}
	return rl
}

func (s *Server) setStatus(id, from, to string) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if q := s.st.db.Requests[id]; q != nil && q.Status == from {
		q.Status, q.Updated = to, now()
		s.st.save()
	}
}

// streamSend: the owner's app uploads; we wait for the requester and pipe.
func (s *Server) streamSend(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	q, err := s.myReq(r, me, true)
	var size int64
	if err == nil {
		if q.Status != "approved" && q.Status != "streaming" {
			err = bad("запрос не ждёт отправки (" + q.Status + ")")
		}
		size = q.Size
	}
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if n, e := strconv.ParseInt(r.Header.Get("X-Size"), 10, 64); e == nil {
		size = n
	}
	id := q.ID
	s.st.mu.Lock()
	label := fmt.Sprintf("%s: %s → %s", q.Plugin, view(me).Nick, view(s.st.db.Users[q.From]).Nick)
	s.st.mu.Unlock()
	s.Logf("info", "server", "передача %s — отправитель подключился (%.1f МБ), ждём получателя", label, float64(size)/1e6)
	s.setStatus(id, "approved", "streaming")
	rl := s.relayFor(id)
	var rs *recvSide
	select {
	case rs = <-rl.recv:
	case <-time.After(s.opt.StreamWait):
		s.setStatus(id, "streaming", "approved")
		return nil, &apiErr{408, "получатель не подключился — отправлю, когда он будет в сети"}
	case <-rl.stop:
		return nil, &apiErr{410, "передачу отменили"}
	case <-r.Context().Done():
		s.setStatus(id, "streaming", "approved")
		return nil, nil
	}
	rs.w.Header().Set("Content-Type", "application/octet-stream")
	if size > 0 {
		rs.w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	rs.w.WriteHeader(200)
	// если одна из сторон пропала посреди передачи (сеть, сон ноутбука) — не висим вечно: 90 секунд без данных — обрыв
	dr := &deadlineReader{r: stopReader{r.Body, rl.stop}, rd: http.NewResponseController(w), wr: http.NewResponseController(rs.w), d: 90 * time.Second}
	t0 := time.Now()
	n, cerr := io.CopyBuffer(&flushWriter{w: rs.w}, dr, make([]byte, 1<<20))
	dr.clear()
	if el := time.Since(t0).Seconds(); cerr != nil {
		s.Logf("error", "server", "передача %s оборвалась на %.1f из %.1f МБ через %.0f с: %v", label, float64(n)/1e6, float64(size)/1e6, el, cerr)
	} else {
		s.Logf("info", "server", "передача %s: %.1f МБ за %.0f с (%.1f МБ/с)", label, float64(n)/1e6, el, float64(n)/1e6/max(el, 0.001))
	}
	if cerr == nil && size > 0 && n != size {
		cerr = fmt.Errorf("передано %d из %d байт", n, size)
	}
	rs.done <- cerr
	if cerr != nil {
		s.setStatus(id, "streaming", "approved")
		return nil, &apiErr{502, "передача прервалась: " + cerr.Error()}
	}
	return map[string]any{"ok": true, "bytes": n}, nil
}

// deadlineReader продлевает сроки чтения у отправителя и записи получателю перед каждым куском.
type deadlineReader struct {
	r      io.Reader
	rd, wr *http.ResponseController
	d      time.Duration
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	t := time.Now().Add(d.d)
	d.rd.SetReadDeadline(t)
	d.wr.SetWriteDeadline(t)
	return d.r.Read(p)
}

func (d *deadlineReader) clear() {
	d.rd.SetReadDeadline(time.Time{})
	d.wr.SetWriteDeadline(time.Time{})
}

// flushWriter отдаёт данные получателю сразу, но не после каждого кусочка (это тормозило передачу):
// не чаще раза в 50 мс.
type flushWriter struct {
	w    http.ResponseWriter
	last time.Time
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if time.Since(f.last) > 50*time.Millisecond {
		if fl, ok := f.w.(http.Flusher); ok {
			fl.Flush()
		}
		f.last = time.Now()
	}
	return n, err
}

// streamRecv: the requester's app downloads; we wait for the owner.
func (s *Server) streamRecv(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	q, err := s.myReq(r, me, false)
	if err == nil && q.Status != "approved" && q.Status != "streaming" {
		err = bad("плагин ещё не разрешён (" + q.Status + ")")
	}
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	rl := s.relayFor(q.ID)
	me2 := &recvSide{w: w, done: make(chan error, 1)}
	select {
	case rl.recv <- me2:
	default:
		return nil, &apiErr{409, "этот плагин уже принимается"}
	}
	withdraw := func() bool { // true if the sender has not taken us yet
		select {
		case <-rl.recv:
			return true
		default:
			return false
		}
	}
	select {
	case err := <-me2.done:
		_ = err
		return nil, nil // the sender wrote the whole response
	case <-time.After(s.opt.StreamWait):
		if withdraw() {
			return nil, &apiErr{408, "отправитель сейчас не в сети"}
		}
	case <-rl.stop:
		if withdraw() {
			return nil, &apiErr{410, "передачу отменили"}
		}
	case <-r.Context().Done():
		if withdraw() {
			return nil, nil
		}
	}
	<-me2.done // the sender is already writing to us: wait till it is done
	return nil, nil
}

// Janitor forgets old finished requests.
func (s *Server) Janitor() {
	for {
		s.st.mu.Lock()
		for _, q := range s.st.db.Requests {
			if q.Status == "streaming" && time.Since(time.Unix(q.Updated, 0)) > time.Hour {
				q.Status = "approved"
			}
			if q.Status != "pending" && q.Status != "approved" && q.Status != "streaming" && time.Since(time.Unix(q.Updated, 0)) > 30*24*time.Hour {
				delete(s.st.db.Requests, q.ID)
			}
		}
		s.st.save()
		s.st.mu.Unlock()
		s.dailyBackup()
		time.Sleep(time.Hour)
	}
}
