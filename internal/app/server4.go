package app

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------- background: inbox from the server, automatic plugin transfers ----------

func (s *Server) setHubErr(err error) {
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	if err == nil {
		s.hubError = ""
	} else {
		s.hubError = err.Error()
	}
}

func (s *Server) hubErr() string {
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	return s.hubError
}

// HubLoop polls the server: invitations, plugin requests to me, plugins sent to me.
func (s *Server) HubLoop(every time.Duration) {
	seenInv := map[string]bool{}
	first := true
	for {
		h := s.cfg.Hub()
		if h.Registered() {
			in, err := h.Inbox()
			s.setHubErr(err)
			if err == nil {
				for _, i := range in.Invites {
					if !seenInv[i.ID] && !first {
						LogActivity("", "info", fmt.Sprintf("📨 %s приглашает в проект «%s»", i.From.Nick, i.Name))
					}
					seenInv[i.ID] = true
				}
				first = false
				s.autoSend(in.Incoming)
				s.autoInstall(in.Ready)
				go h.PutInventory(ScanInstalled(s.cfg, false))
			} else if he, ok := err.(*HubError); ok && he.Code == 401 {
				// the server forgot this device (e.g. the admin removed it)
				s.setHubErr(errors.New("сервер не узнаёт это устройство — зарегистрируйся заново (⚙ Настройки)"))
			}
		}
		time.Sleep(every)
	}
}

func (s *Server) sharesProject(uid string) bool {
	for _, age := range []time.Duration{time.Minute, 5 * time.Second} {
		ps, err := s.cfg.Hub().Projects(age)
		if err != nil {
			return false
		}
		for _, p := range ps {
			for _, m := range p.Members {
				if m.ID == uid {
					return true // the list only has my projects
				}
			}
		}
	}
	return false
}

// autoSend: plugin sharing allowed in the settings → send without asking
// (only when the plugin itself fits the other system; installers need a human).
func (s *Server) autoSend(incoming []PluginRequest) {
	mode := s.cfg.Sharing
	if mode != "members" && mode != "all" {
		return
	}
	for _, r := range incoming {
		if _, busy := s.busyReq.Load(r.ID); busy {
			continue
		}
		if mode == "members" && !s.sharesProject(r.From) {
			s.busyReq.Store(r.ID, "human")
			continue
		}
		in := InspectRequest(s.cfg, &r)
		if !in.Compatible {
			s.busyReq.Store(r.ID, "human")
			continue
		}
		s.busyReq.Store(r.ID, "sending")
		rr := r
		s.startJobOpt("Отправка "+rr.Plugin+" для "+rr.FromNick+" (без вопросов)", "", false, func(j *Job) (any, error) {
			err := ApproveRequest(s.cfg, j, rr.ID, "")
			if err != nil {
				s.busyReq.Store(rr.ID, "human")
			}
			return "ok", err
		})
	}
}

// autoInstall: a plugin I asked for has arrived → install it right away.
func (s *Server) autoInstall(ready []PluginRequest) {
	for _, r := range ready {
		if r.Kind != "plugin" {
			continue // installers are programs: the user starts them
		}
		if _, busy := s.busyReq.LoadOrStore(r.ID, "installing"); busy {
			continue
		}
		rr := r
		s.startJobOpt("Установка "+rr.Plugin+" от "+rr.ToNick, "", false, func(j *Job) (any, error) {
			dst, err := InstallRequest(s.cfg, j, rr.ID)
			if err != nil {
				s.busyReq.Store(rr.ID, "human")
			}
			return dst, err
		})
	}
}

func (s *Server) busyMap() map[string]string {
	out := map[string]string{}
	s.busyReq.Range(func(k, v any) bool { out[k.(string)] = v.(string); return true })
	return out
}

// ---------- local API: account, people, projects on the server, plugins ----------

func formatRank(f string) int {
	switch f {
	case "VST3":
		return 0
	case "CLAP":
		return 1
	case "VST2", "VST":
		return 2
	case "AU":
		if OSName() == "macOS" {
			return 1
		}
		return 9
	}
	return 5
}

func (s *Server) routes4(api func(string, func(*http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }
	h := func() *Hub { return s.cfg.Hub() }
	jobAPI := func(path, title string, lock bool, fn func(b map[string]any, j *Job) (any, error)) {
		api(path, func(r *http.Request) (any, error) {
			b := body(r)
			p, _ := b["path"].(string)
			if t := str(b, "title"); t != "" {
				title = t
			}
			return s.startJobOpt(title, p, lock, func(j *Job) (any, error) { return fn(b, j) }), nil
		})
	}
	addLocal := func(dir, group string) {
		if dir != "" && s.cfg.Project(dir) == nil {
			s.cfg.Projects = append(s.cfg.Projects, LocalProject{Path: dir, RelinkOnReceive: true, Group: group})
			sort.Slice(s.cfg.Projects, func(i, j int) bool {
				return strings.ToLower(filepath.Base(s.cfg.Projects[i].Path)) < strings.ToLower(filepath.Base(s.cfg.Projects[j].Path))
			})
		}
		s.cfg.Save()
	}

	// ----- account: one device = one user -----
	api("/api/account/register", func(r *http.Request) (any, error) {
		b := body(r)
		if srv := strings.TrimRight(strings.TrimSpace(str(b, "server")), "/"); srv != "" && srv != s.cfg.ServerURL() {
			if !strings.HasPrefix(srv, "http://") && !strings.HasPrefix(srv, "https://") {
				srv = "https://" + srv
			}
			if err := h().Ping(srv); err != nil {
				return nil, err
			}
			s.cfg.Server = srv
			if srv == strings.TrimRight(DefaultServer, "/") {
				s.cfg.Server = ""
			}
		}
		if h().Registered() {
			return nil, errors.New("это устройство уже зарегистрировано как " + s.cfg.UserName)
		}
		u, err := h().Register(str(b, "nick"))
		if err != nil {
			return nil, err
		}
		LogActivity("", "ok", "Регистрация на сервере: "+u.Nick)
		go h().PutInventory(ScanInstalled(s.cfg, false))
		go h().Inbox()
		return u, nil
	})
	api("/api/account/update", func(r *http.Request) (any, error) {
		var req struct {
			Nick    *string `json:"nick"`
			Sharing *string `json:"sharing"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		u, err := h().UpdateMe(req.Nick, req.Sharing)
		if err == nil && req.Sharing != nil {
			s.busyReq.Range(func(k, v any) bool { // re-check waiting requests with the new rule
				if v == "human" {
					s.busyReq.Delete(k)
				}
				return true
			})
		}
		return u, err
	})
	api("/api/users", func(r *http.Request) (any, error) {
		us, err := h().Users()
		if err != nil {
			return nil, err
		}
		if us == nil {
			us = []HubUser{}
		}
		return map[string]any{"users": us, "me": s.cfg.UserID}, nil
	})
	// plugins the other user has and I don't
	api("/api/user/missing", func(r *http.Request) (any, error) {
		inv, err := h().Inventory(r.URL.Query().Get("id"))
		if err != nil {
			return nil, err
		}
		mine := map[string]bool{}
		for _, p := range ScanInstalled(s.cfg, false) {
			mine[normName(p.Name)] = true
		}
		best := map[string]InstalledPlugin{}
		for _, p := range inv.Plugins {
			k := normName(p.Name)
			if k == "" || mine[k] {
				continue
			}
			if o, ok := best[k]; !ok || formatRank(p.Format) < formatRank(o.Format) {
				best[k] = p
			}
		}
		out := []InstalledPlugin{}
		for _, p := range best {
			if formatRank(p.Format) < 9 {
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
		return map[string]any{"user": inv.User, "updated": inv.Updated, "missing": out, "total": len(inv.Plugins)}, nil
	})

	// ----- projects on the server -----
	jobAPI("/api/project/server-create", "Создаю проект на сервере", true, func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(str(b, "name"))
		if name == "" {
			name = filepath.Base(p.Dir)
		}
		j.Logf("Создаю место для проекта на GitHub…")
		hp, err := h().CreateProject(name)
		if err != nil {
			return nil, err
		}
		j.Logf("Проект «%s» создан", hp.Name)
		LogActivity(p.Dir, "ok", "Проект создан на сервере")
		for _, n := range strings.FieldsFunc(str(b, "invite"), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			if _, err := h().Invite(hp.ID, n); err != nil {
				j.Logf("⚠ Не удалось пригласить %s: %v", n, err)
			} else {
				j.Logf("Приглашение отправлено: %s", n)
			}
		}
		if err := p.Init(hp.CloneURL); err != nil {
			return nil, err
		}
		if _, err := p.commitAll("Начало проекта (" + s.cfg.MachineID() + ")"); err != nil {
			return nil, err
		}
		return p.Send("Первая отправка", false)
	})
	jobAPI("/api/project/move-to-server", "Переношу проект на сервер", true, func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		return p.MoveToServer(str(b, "name"))
	})
	api("/api/project/participants", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		hp := h().ProjectForRemote(p.git().Remote())
		if hp == nil {
			return nil, errors.New("проект не на сервере проги узбека — участников можно звать только в проекты на сервере")
		}
		return map[string]any{"project": hp, "me": s.cfg.UserID}, nil
	})
	serverProject := func(path string) (*HubProject, error) {
		p, err := s.project(path, nil)
		if err != nil {
			return nil, err
		}
		hp := h().ProjectForRemote(p.git().Remote())
		if hp == nil {
			return nil, errors.New("проект не на сервере проги узбека")
		}
		return hp, nil
	}
	api("/api/project/invite", func(r *http.Request) (any, error) {
		b := body(r)
		hp, err := serverProject(str(b, "path"))
		if err != nil {
			return nil, err
		}
		inv, err := h().Invite(hp.ID, str(b, "nick"))
		if err == nil {
			LogActivity(str(b, "path"), "ok", "Приглашение отправлено: "+inv.To.Nick)
		}
		return inv, err
	})
	api("/api/project/remove-participant", func(r *http.Request) (any, error) {
		b := body(r)
		hp, err := serverProject(str(b, "path"))
		if err != nil {
			return nil, err
		}
		err = h().RemoveMember(hp.ID, str(b, "userId"))
		if err == nil {
			LogActivity(str(b, "path"), "ok", "Участник убран из проекта: "+str(b, "name"))
		}
		return "ok", err
	})
	api("/api/invitations/decline", func(r *http.Request) (any, error) {
		err := h().DeclineInvite(str(body(r), "id"))
		go h().Inbox()
		return "ok", err
	})
	jobAPI("/api/invitations/accept", "Принимаю приглашение", true, func(b map[string]any, j *Job) (any, error) {
		parent := strings.TrimSpace(str(b, "parent"))
		if parent == "" {
			return nil, errors.New("не выбрана папка, куда скачать проект")
		}
		hp, err := h().AcceptInvite(str(b, "id"))
		if err != nil {
			return nil, err
		}
		go h().Inbox()
		j.Logf("Приглашение принято")
		LogActivity("", "ok", "Принято приглашение в «"+hp.Name+"» от "+hp.Owner.Nick)
		s.cfg.LastParent = parent
		dir, err := CloneNamed(s.cfg, j, hp.CloneURL, parent, hp.Name)
		addLocal(dir, str(b, "group"))
		return map[string]string{"path": dir}, err
	})
	api("/api/available", func(r *http.Request) (any, error) {
		if !h().Registered() {
			return []any{}, nil
		}
		ps, err := h().Projects(10 * time.Second)
		if err != nil {
			return nil, err
		}
		local := map[string]bool{}
		for _, lp := range s.cfg.Projects {
			if exists(lp.Path) {
				if rm := (&Git{Dir: lp.Path, Cfg: s.cfg}).Remote(); rm != "" {
					local[normRemote(rm)] = true
				}
			}
		}
		out := []HubProject{}
		for _, p := range ps {
			if !local[normRemote(p.CloneURL)] {
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
		return out, nil
	})
	jobAPI("/api/available/clone", "Скачиваю проект", true, func(b map[string]any, j *Job) (any, error) {
		parent := strings.TrimSpace(str(b, "parent"))
		if parent == "" {
			return nil, errors.New("выбери папку, куда скачать проект")
		}
		hp, err := h().Project(str(b, "id"))
		if err != nil {
			return nil, err
		}
		s.cfg.LastParent = parent
		dir, err := CloneNamed(s.cfg, j, hp.CloneURL, parent, hp.Name)
		addLocal(dir, str(b, "group"))
		return map[string]string{"path": dir}, err
	})

	// ----- plugin transfers -----
	api("/api/plugin/request", func(r *http.Request) (any, error) {
		var req struct {
			To    string    `json:"to"`
			Items []ReqItem `json:"items"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		out, err := h().RequestPlugins(req.To, req.Items)
		if err == nil && len(out) > 0 {
			LogActivity("", "info", fmt.Sprintf("🔌 Запрошено у %s плагинов: %d", req.To, len(out)))
		}
		go h().Inbox()
		if out == nil {
			out = []PluginRequest{}
		}
		return out, err
	})
	api("/api/plugin/requests", func(r *http.Request) (any, error) {
		rs, err := h().Requests()
		if err != nil {
			return nil, err
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i].Created > rs[j].Created })
		if rs == nil {
			rs = []PluginRequest{}
		}
		return map[string]any{"requests": rs, "me": s.cfg.UserID, "busy": s.busyMap()}, nil
	})
	api("/api/plugin/inspect", func(r *http.Request) (any, error) {
		q, err := s.cfg.myRequest(r.URL.Query().Get("id"), true)
		if err != nil {
			return nil, err
		}
		return InspectRequest(s.cfg, q), nil
	})
	api("/api/plugin/decline", func(r *http.Request) (any, error) {
		b := body(r)
		err := h().DeclineRequest(str(b, "id"), str(b, "reason"))
		go h().Inbox()
		return "ok", err
	})
	api("/api/plugin/cancel", func(r *http.Request) (any, error) {
		err := h().CancelRequest(str(body(r), "id"))
		go h().Inbox()
		return "ok", err
	})
	jobAPI("/api/plugin/approve", "Отправка плагина", false, func(b map[string]any, j *Job) (any, error) {
		id := str(b, "id")
		s.busyReq.Store(id, "sending")
		err := ApproveRequest(s.cfg, j, id, str(b, "file"))
		if err != nil {
			s.busyReq.Store(id, "human")
		}
		go h().Inbox()
		return "ok", err
	})
	jobAPI("/api/plugin/install", "Установка плагина", false, func(b map[string]any, j *Job) (any, error) {
		id := str(b, "id")
		s.busyReq.Store(id, "installing")
		dst, err := InstallRequest(s.cfg, j, id)
		if err != nil {
			s.busyReq.Store(id, "human")
		}
		go h().Inbox()
		return dst, err
	})
}

// MoveToServer: a project from the old version (own GitHub repo or no remote)
// gets a place on our server; history, audio and comments move along.
func (p *Project) MoveToServer(name string) (*SyncResult, error) {
	h, j := p.Cfg.Hub(), p.Job
	g := p.git()
	if !g.IsRepo() {
		return nil, errors.New("в папке нет репозитория")
	}
	old := g.Remote()
	if old != "" && h.ProjectForRemote(old) != nil {
		return nil, errors.New("проект уже на сервере")
	}
	if old != "" {
		j.Logf("Докачиваю всё аудио со старого сервера…")
		if _, err := g.Run("lfs", "fetch", "--all", "origin"); err != nil {
			j.Logf("⚠ Не всё аудио удалось докачать со старого сервера — перенесу то, что есть на этом компьютере")
		}
	}
	if name = strings.TrimSpace(name); name == "" {
		name = filepath.Base(p.Dir)
	}
	hp, err := h.CreateProject(name)
	if err != nil {
		return nil, err
	}
	j.Logf("Проект «%s» создан на сервере", hp.Name)
	g.RunQuiet("config", "lfs.allowincompletepush", "true")
	if err := p.Init(hp.CloneURL); err != nil {
		return nil, err
	}
	if g.metaHead() != "" { // comments & status move too
		g.RunQuiet("push", "--no-verify", "origin", metaRef+":refs/heads/"+metaBranch)
	}
	res, err := p.Send("Перенос на сервер проги узбека", false)
	if err == nil {
		LogActivity(p.Dir, "ok", "Проект перенесён на сервер проги узбека")
		j.Logf("Теперь позови участников — кнопка «👥 Участники»")
	}
	return res, err
}
