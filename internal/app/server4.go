package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- background: inbox from the server, plugin transfers ----------

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

// ReceivedInstaller is an installer that arrived and waits to be started.
type ReceivedInstaller struct {
	ID     string `json:"id"`
	Plugin string `json:"plugin"`
	From   string `json:"from"`
	Path   string `json:"path"`
}

var (
	installersMu sync.Mutex
	installers   []ReceivedInstaller
	retryAt      sync.Map // request id -> time: when to try sending again
)

// HubLoop polls the server: invitations, plugin requests and transfers.
func (s *Server) HubLoop(every time.Duration) {
	seenInv := map[string]bool{}
	first := true
	for {
		h := s.cfg.Hub()
		wait := every
		if h.Registered() {
			in, err := h.Inbox()
			s.setHubErr(err)
			if err == nil {
				for _, i := range in.Invites {
					if !seenInv[i.ID] && !first {
						LogActivity("", "info", fmt.Sprintf("%s приглашает в проект «%s»", i.From.Nick, i.Name))
					}
					seenInv[i.ID] = true
				}
				first = false
				if in.MoveTo != "" {
					old := s.cfg.ServerURL()
					if err := h.FollowMove(in.MoveTo); err == nil && s.cfg.ServerURL() != old {
						LogActivity("", "ok", "Сервер переехал: теперь "+s.cfg.ServerURL())
						DevLog("info", 0, false, "сервер переехал: %s → %s", old, s.cfg.ServerURL())
					} else if err != nil {
						DevLog("info", 0, true, "сервер просит переехать на %s, но там не получилось: %v", in.MoveTo, err)
					}
				}
				s.autoApprove(in.Incoming)
				s.sendAllowed(in.ToSend)
				s.receiveAllowed(in.Ready)
				s.answerOffers(in.Ready)
				s.reconcileClaims(in.Ready)
				s.serveFetches(in.Fetches)
				go flushErrorReports(s.cfg)
				s.maybeAutoUpdate(in.Update)
				go h.PutInventory(ScanInstalled(s.cfg, false))
				if len(in.Incoming)+len(in.ToSend)+len(in.Ready)+len(in.Fetches) > 0 {
					wait = 2 * time.Second // a transfer is going on: react fast
				}
			} else if he, ok := err.(*HubError); ok && he.Code == 401 {
				s.setHubErr(errors.New("сервер не узнаёт это устройство — зарегистрируйся заново (Настройки)"))
			}
		}
		time.Sleep(wait)
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

// autoApprove: plugin sharing allowed in the settings → say yes without asking
// (only when the plugin itself fits the other system; installers need a human).
func (s *Server) autoApprove(incoming []PluginRequest) {
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
		s.busyReq.Store(r.ID, "approving")
		if err := ApproveRequest(s.cfg, nil, r.ID, ""); err != nil {
			s.busyReq.Store(r.ID, "human")
		}
	}
}

// sendAllowed: what I allowed goes out as soon as the other side is online.
func (s *Server) sendAllowed(list []PluginRequest) {
	for _, r := range list {
		if !r.FromOnline {
			continue
		}
		if t, ok := retryAt.Load(r.ID); ok && time.Now().Before(t.(time.Time)) {
			continue
		}
		if v, busy := s.busyReq.Load(r.ID); busy && v == "sending" {
			continue
		}
		s.busyReq.Store(r.ID, "sending")
		rr := r
		s.startJobOpt("Передаю "+rr.Plugin+" → "+rr.FromNick, "", false, func(j *Job) (any, error) {
			s.reqJobs.Store(rr.ID, j)
			err := SendApproved(s.cfg, j, &rr)
			s.reqJobs.Delete(rr.ID)
			s.busyReq.Delete(rr.ID)
			if errors.Is(err, errCancelled) {
				return nil, err
			}
			if err != nil {
				retryAt.Store(rr.ID, time.Now().Add(30*time.Second))
			}
			if errors.Is(err, errNotOnline) {
				return nil, errors.New(rr.FromNick + " не принял передачу — попробую ещё раз, когда он будет в сети")
			}
			return "ok", err
		})
	}
}

// receiveAllowed: the owner's app is sending → take it and install.
func (s *Server) receiveAllowed(list []PluginRequest) {
	for _, r := range list {
		if r.Status != ReqStreaming {
			continue
		}
		if _, busy := s.busyReq.LoadOrStore(r.ID, "receiving"); busy {
			continue
		}
		rr := r
		title := "Получаю " + rr.Plugin + " от " + rr.ToNick
		s.startJobOpt(title, "", false, func(j *Job) (any, error) {
			s.reqJobs.Store(rr.ID, j)
			dst, err := ReceiveRequest(s.cfg, j, &rr)
			s.reqJobs.Delete(rr.ID)
			s.busyReq.Delete(rr.ID)
			if err == nil && rr.Kind == "installer" {
				installersMu.Lock()
				installers = append(installers, ReceivedInstaller{ID: rr.ID, Plugin: rr.Plugin, From: rr.ToNick, Path: dst})
				installersMu.Unlock()
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

func pendingInstallers() []ReceivedInstaller {
	installersMu.Lock()
	defer installersMu.Unlock()
	return append([]ReceivedInstaller{}, installers...)
}

var availMu sync.Mutex

// ---------- local API: account, people, projects, plugins ----------

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
			t := title
			if v := str(b, "title"); v != "" {
				t = v
			}
			return s.startJobOpt(t, p, lock, func(j *Job) (any, error) { return fn(b, j) }), nil
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
		tok := strings.TrimSpace(str(b, "token"))
		if tok == "" {
			tok = s.cfg.Token
		}
		code := str(b, "code")
		if code == "" {
			code = DefaultServerCode
		}
		u, err := h().Register(str(b, "nick"), code, tok)
		if err != nil {
			return nil, err
		}
		LogActivity("", "ok", "Регистрация на сервере: "+u.Nick+" (GitHub @"+u.GitHub+")")
		go h().PutInventory(ScanInstalled(s.cfg, false))
		go h().Inbox()
		return u, nil
	})
	api("/api/account/update", func(r *http.Request) (any, error) {
		var req struct {
			Nick    *string `json:"nick"`
			Sharing *string `json:"sharing"`
			Token   *string `json:"token"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		u, err := h().UpdateMe(req.Nick, req.Sharing, req.Token)
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
		myVer := map[string]InstalledPlugin{}
		for _, p := range ScanInstalled(s.cfg, false) {
			mine[normName(p.Name)] = true
			if p.Version != "" {
				myVer[normName(p.Name)+"|"+p.Format] = p
			}
		}
		// есть у обоих, но версии разные — тоже можно скачать
		versions := []map[string]string{}
		seenV := map[string]bool{}
		for _, p := range inv.Plugins {
			k := normName(p.Name)
			m, ok := myVer[k+"|"+p.Format]
			if !ok || p.Version == "" || p.Version == m.Version || seenV[k] {
				continue
			}
			seenV[k] = true
			versions = append(versions, map[string]string{"name": p.Name, "format": p.Format, "vendor": p.Vendor, "version": p.Version, "mine": m.Version,
				"newer": fmt.Sprint(newerVersion(p.Version, m.Version))})
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
		sort.Slice(versions, func(i, j int) bool {
			return strings.ToLower(versions[i]["name"]) < strings.ToLower(versions[j]["name"])
		})
		return map[string]any{"user": inv.User, "updated": inv.Updated, "missing": out, "versions": versions, "total": len(inv.Plugins)}, nil
	})

	// ----- projects: own GitHub repo per project, people through the server -----
	jobAPI("/api/project/server-create", "Создаю проект", true, func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		return p.CreateOnGitHub(str(b, "name"), inviteList(b["invite"]))
	})
	jobAPI("/api/project/move-to-server", "Подключаю проект к серверу", true, func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		return p.ConnectToServer(str(b, "name"))
	})
	api("/api/project/participants", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		hp := h().ProjectForRemote(p.git().Remote())
		if hp == nil {
			return nil, errors.New("проект не подключён к серверу FruityHub — участников можно звать только в проекты на сервере")
		}
		return map[string]any{"project": hp, "me": s.cfg.UserID}, nil
	})
	api("/api/project/invite", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		inv, err := p.Invite(str(b, "nick"))
		if err == nil {
			LogActivity(p.Dir, "ok", "Приглашение отправлено: "+inv.To.Nick)
		}
		return inv, err
	})
	api("/api/project/invite-all", func(r *http.Request) (any, error) {
		p, err := s.project(str(body(r), "path"), nil)
		if err != nil {
			return nil, err
		}
		res, err := p.InviteAll()
		if err == nil {
			if l, _ := res["invited"].([]string); len(l) > 0 {
				LogActivity(p.Dir, "ok", "Приглашения отправлены: "+strings.Join(l, ", "))
			}
		}
		return res, err
	})
	api("/api/project/remove-participant", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		err = p.RemoveParticipant(str(b, "userId"))
		if err == nil {
			LogActivity(p.Dir, "ok", "Участник убран из проекта: "+str(b, "name"))
		}
		return "ok", err
	})
	api("/api/invitations/decline", func(r *http.Request) (any, error) {
		err := h().DeclineInvite(str(body(r), "id"))
		go h().Inbox()
		return "ok", err
	})
	jobAPI("/api/invitations/accept", "Принимаю приглашение", false, func(b map[string]any, j *Job) (any, error) {
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
		return s.cloneOnce(j, hp, parent, str(b, "group"), addLocal)
	})
	api("/api/available", func(r *http.Request) (any, error) {
		if !h().Registered() {
			return []any{}, nil
		}
		if added, err := h().Discover(r.URL.Query().Get("force") == "1"); err == nil {
			for _, a := range added {
				LogActivity("", "info", "Найден проект твоего GitHub: «"+a.Name+"» — появился в «Доступных»")
			}
		}
		availMu.Lock() // частые «обновить» не должны перемешивать списки
		defer availMu.Unlock()
		ps, err := h().Projects(10 * time.Second)
		if err != nil {
			return nil, err
		}
		// «уже на этом компьютере» сверяем с тем же списком ps, а не с отдельно запрошенным
		var remotes []string
		for _, lp := range s.cfg.Projects {
			if exists(lp.Path) {
				if r := (&Git{Dir: lp.Path, Cfg: s.cfg}).Remote(); r != "" {
					remotes = append(remotes, r)
				}
			}
		}
		out := []HubProject{}
		for _, p := range ps {
			here := false
			for _, r := range remotes {
				o, n, isGH := repoSlug(r)
				if normRemote(r) == normRemote(p.CloneURL) || isGH && strings.EqualFold(p.Repo, o+"/"+n) {
					here = true
					break
				}
			}
			if !here {
				_, p.Downloading = s.cloning.Load(p.ID)
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
		return out, nil
	})
	jobAPI("/api/available/clone", "Скачиваю проект", false, func(b map[string]any, j *Job) (any, error) {
		parent := strings.TrimSpace(str(b, "parent"))
		if parent == "" {
			return nil, errors.New("выбери папку, куда скачать проект")
		}
		hp, err := h().Project(str(b, "id"))
		if err != nil {
			return nil, err
		}
		s.cfg.LastParent = parent
		return s.cloneOnce(j, hp, parent, str(b, "group"), addLocal)
	})
	// «папка уже есть» → убрать её в Корзину (дальше UI запускает скачивание заново)
	api("/api/clone/trash-existing", func(r *http.Request) (any, error) {
		path := str(body(r), "path")
		if _, ok := s.fixPaths.Load(path); !ok {
			return nil, errors.New("эту папку так удалить нельзя")
		}
		home, _ := os.UserHomeDir()
		clean := filepath.Clean(path)
		if clean == filepath.Clean(home) || len(clean) < 4 || filepath.Dir(clean) == clean {
			return nil, errors.New("эту папку удалять нельзя")
		}
		if err := moveDirToTrash(clean); err != nil {
			return nil, fmt.Errorf("не получилось убрать в Корзину (закрой проект в FL Studio): %w", err)
		}
		s.fixPaths.Delete(path)
		var keep []LocalProject
		for _, lp := range s.cfg.Projects {
			if !samePath(lp.Path, path) {
				keep = append(keep, lp)
			}
		}
		s.cfg.Projects = keep
		s.cfg.Save()
		return "ok", nil
	})

	// ----- что проверено после установки плагина, и система -----
	api("/api/plugin/report-seen", func(r *http.Request) (any, error) {
		markReportSeen(str(body(r), "id"))
		return "ok", nil
	})
	api("/api/plugin/reports", func(r *http.Request) (any, error) { return allReports(), nil })
	jobAPI("/api/system/speedtest", "Проверяю скорость связи с сервером", false, func(b map[string]any, j *Job) (any, error) {
		return SpeedTest(s.cfg, j)
	})
	api("/api/system/check", func(r *http.Request) (any, error) {
		sysGit := gitBinary() != filepath.Join(toolsDir(), "mingit", "cmd", "git.exe")
		return map[string]any{"os": OSName(), "arch": hostArch(), "vcredistMissing": VCRedistMissing(), "gpus": GPUs(), "basicGpu": BasicGPU(),
			"gitCrashes": gitCrashes.Load(), "systemGit": sysGit}, nil
	})
	jobAPI("/api/system/install-vcredist", "Ставлю Microsoft Visual C++", true, func(b map[string]any, j *Job) (any, error) {
		return "ok", InstallVCRedist(j)
	})

	// ----- plugin transfers -----
	api("/api/plugin/request", func(r *http.Request) (any, error) {
		var req struct {
			To    string    `json:"to"`
			Path  string    `json:"path"` // из какого проекта просят — чтобы история передач была по проекту
			Items []ReqItem `json:"items"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Path != "" {
			key := (&Project{Dir: req.Path, Cfg: s.cfg}).requestKey()
			for i := range req.Items {
				if req.Items[i].Project == "" {
					req.Items[i].Project = key
				}
			}
		}
		out, err := h().RequestPlugins(req.To, req.Items)
		if err == nil && len(out) > 0 {
			LogActivity("", "info", fmt.Sprintf("Запрошено у %s плагинов: %d", req.To, len(out)))
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
	jobAPI("/api/plugin/retry-data", "Кладу данные плагина", false, func(req map[string]any, j *Job) (any, error) {
		return "ok", RetryPendingData(j, str(req, "id"))
	})
	api("/api/plugin/uninstall-plan", func(r *http.Request) (any, error) {
		b := body(r)
		return uninstallPlan(s.cfg, str(b, "name"), str(b, "format"))
	})
	jobAPI("/api/plugin/uninstall", "Удаляю плагин", false, func(b map[string]any, j *Job) (any, error) {
		var paths []string
		if l, ok := b["paths"].([]any); ok {
			for _, x := range l {
				if v, ok := x.(string); ok {
					paths = append(paths, v)
				}
			}
		}
		return UninstallPlugin(s.cfg, j, str(b, "name"), str(b, "format"), paths)
	})
	jobAPI("/api/plugin/diag", "Диагностика плагина", false, func(b map[string]any, j *Job) (any, error) {
		return PluginDiag(s.cfg, j, str(b, "name"), str(b, "format"))
	})
	api("/api/plugin/diag/send", func(r *http.Request) (any, error) { // отчёт — в журнал сервера (админка → Логи сервера)
		txt := str(body(r), "text")
		var lines []reportLine
		for len(txt) > 0 && len(lines) < 60 {
			n := min(len(txt), 2800)
			for n < len(txt) && n > 0 && txt[n]&0xC0 == 0x80 { // не резать букву пополам
				n--
			}
			lines = append(lines, reportLine{time.Now().UnixMilli(), "[диагностика] " + txt[:n]})
			txt = txt[n:]
		}
		return "ok", h().do("POST", "/api/log", map[string]any{"lines": lines, "version": Version, "os": OSName()}, nil)
	})
	jobAPI("/api/plugin/check", "Проверяю плагин", false, func(b map[string]any, j *Job) (any, error) {
		return CheckPlugin(s.cfg, j, str(b, "name"), str(b, "format"))
	})
	api("/api/plugin/deps", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		return PluginDeps(s.cfg, q.Get("name"), q.Get("format"))
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
		id := str(body(r), "id")
		err := h().CancelRequest(id) // сервер обрывает поток у обеих сторон
		if j, ok := s.reqJobs.Load(id); ok {
			j.(*Job).Cancel()
		}
		go h().Inbox()
		return "ok", err
	})
	api("/api/plugin/retry", func(r *http.Request) (any, error) {
		b := body(r)
		id := str(b, "id")
		retryAt.Delete(id)
		var err error
		if re, ok := b["reinstall"].(bool); ok {
			err = h().RetryReinstall(id, re)
		} else {
			err = h().RetryRequest(id)
		}
		go h().Inbox()
		return "ok", err
	})
	// идущие сейчас передачи плагинов — чтобы предупредить перед закрытием или обновлением
	api("/api/transfers/active", func(r *http.Request) (any, error) {
		out := []map[string]string{}
		s.reqJobs.Range(func(k, v any) bool {
			j := v.(*Job)
			j.mu.Lock()
			done := j.Done
			j.mu.Unlock()
			if !done {
				out = append(out, map[string]string{"id": k.(string), "title": j.Title})
			}
			return true
		})
		return out, nil
	})
	api("/api/plugin/approve", func(r *http.Request) (any, error) {
		b := body(r)
		id := str(b, "id")
		err := ApproveRequest(s.cfg, nil, id, str(b, "file"))
		if err != nil {
			s.busyReq.Store(id, "human")
			return nil, err
		}
		s.busyReq.Delete(id)
		go h().Inbox()
		return "ok", nil
	})
	api("/api/plugin/run-installer", func(r *http.Request) (any, error) {
		id := str(body(r), "id")
		installersMu.Lock()
		var it *ReceivedInstaller
		rest := installers[:0]
		for i := range installers {
			if installers[i].ID == id {
				x := installers[i]
				it = &x
			} else {
				rest = append(rest, installers[i])
			}
		}
		installers = rest
		installersMu.Unlock()
		if it == nil || r.URL.Query().Get("skip") == "1" || str(body(r), "skip") == "1" {
			return "ok", nil
		}
		if strings.HasSuffix(strings.ToLower(it.Path), ".zip") {
			return "ok", OpenFolder(filepath.Dir(it.Path))
		}
		return "ok", OpenURL(it.Path)
	})
}

// ---------- projects: own GitHub repos + our server ----------

// CreateOnGitHub makes a private repo on the user's GitHub, registers it on
// our server, invites people and sends the first version.
func (p *Project) CreateOnGitHub(name string, invite []string) (any, error) {
	h, j := p.Cfg.Hub(), p.Job
	if name = strings.TrimSpace(name); name == "" {
		name = filepath.Base(p.Dir)
	}
	// check the nicks first, so a typo doesn't leave a half-made project
	var people []*HubUser
	for _, n := range invite {
		if strings.TrimSpace(n) == "*" {
			us, err := h.Users()
			if err != nil {
				return nil, err
			}
			for i := range us {
				if us[i].ID != p.Cfg.UserID {
					people = append(people, &us[i])
				}
			}
			continue
		}
		if strings.TrimSpace(n) == "" {
			continue
		}
		u := h.UserByNick(n)
		if u == nil {
			return nil, fmt.Errorf("пользователь «%s» не зарегистрирован в FruityHub", n)
		}
		people = append(people, u)
	}
	var hp *HubProject
	if prev := p.unfinishedServerProject(name); prev != nil {
		// прошлая попытка создала репозиторий и записала проект на сервер, но не дошла до конца — продолжаем её
		hp = prev
		j.Logf("Продолжаю с уже созданным репозиторием %s", hp.Repo)
	} else {
		j.Logf("Создаю приватный репозиторий на твоём GitHub…")
		full, clone, err := GitHubCreateRepo(p.Cfg, name)
		if err != nil {
			return nil, err
		}
		j.Logf("Создан репозиторий %s", full)
		if hp, err = h.AddProject(name, full, clone); err != nil {
			return nil, err
		}
	}
	clone := hp.CloneURL
	if err := p.Init(clone); err != nil {
		return nil, err
	}
	if _, err := p.commitAll("Начало проекта (" + p.Cfg.MachineID() + ")"); err != nil {
		return nil, err
	}
	res, err := p.Send("Первая отправка", false)
	if err != nil {
		return nil, err
	}
	for _, u := range people {
		if _, err := p.inviteTo(hp, u.Nick); err != nil {
			j.Logf("Внимание: Не удалось пригласить %s: %v", u.Nick, err)
		} else {
			j.Logf("Приглашение отправлено: %s", u.Nick)
		}
	}
	LogActivity(p.Dir, "ok", "Проект создан: "+hp.Repo)
	return res, nil
}

// ConnectToServer: a project from before (own repo, maybe already shared on
// GitHub) gets onto our server; everyone who already has access on GitHub and
// is registered becomes a member. A folder without a GitHub repo gets one.
func (p *Project) ConnectToServer(name string) (any, error) {
	h, j := p.Cfg.Hub(), p.Job
	g := p.git()
	if !g.IsRepo() {
		return nil, errors.New("в папке нет репозитория")
	}
	if name = strings.TrimSpace(name); name == "" {
		name = filepath.Base(p.Dir)
	}
	remote := g.Remote()
	if remote == "" {
		return p.CreateOnGitHub(name, nil)
	}
	if h.ProjectForRemote(remote) != nil {
		return nil, errors.New("проект уже на сервере")
	}
	owner, repo, ok := repoSlug(remote)
	if !ok {
		return nil, errors.New("подключить к серверу можно только проект с репозиторием на GitHub")
	}
	full := owner + "/" + repo
	admin, clone, err := ghRepoInfo(p.Cfg, full)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, errors.New("подключить проект к серверу может только его владелец на GitHub (" + owner + ")")
	}
	if clone == "" {
		clone = remote
	}
	hp, err := h.AddProject(name, full, clone)
	if err != nil {
		return nil, err
	}
	if logins, err := ghCollaborators(p.Cfg, full); err == nil {
		if hp2, err := h.SyncMembers(hp.ID, logins); err == nil {
			hp = hp2
		}
	}
	var nicks []string
	for _, m := range hp.Members {
		nicks = append(nicks, m.Nick)
	}
	j.Logf("Проект на сервере. Участники: %s", strings.Join(nicks, ", "))
	LogActivity(p.Dir, "ok", "Проект подключён к серверу FruityHub")
	return hp, nil
}

func (p *Project) serverProject() (*HubProject, error) {
	hp := p.Cfg.Hub().ProjectForRemote(p.git().Remote())
	if hp == nil {
		return nil, errors.New("проект не подключён к серверу FruityHub")
	}
	return hp, nil
}

// Invite: an invitation in our server + a collaborator invitation on GitHub.
func (p *Project) Invite(nick string) (*HubInvite, error) {
	hp, err := p.serverProject()
	if err != nil {
		return nil, err
	}
	return p.inviteTo(hp, nick)
}

// InviteAll зовёт в проект всех зарегистрированных, кто ещё не участник и не приглашён.
func (p *Project) InviteAll() (map[string]any, error) {
	hp, err := p.serverProject()
	if err != nil {
		return nil, err
	}
	us, err := p.Cfg.Hub().Users()
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{p.Cfg.UserID: true}
	for _, m := range hp.Members {
		skip[m.ID] = true
	}
	for _, i := range hp.Invited {
		skip[i.To.ID] = true
	}
	var done, failed []string
	for _, u := range us {
		if skip[u.ID] {
			continue
		}
		if _, err := p.inviteTo(hp, u.Nick); err != nil {
			failed = append(failed, u.Nick+": "+err.Error())
		} else {
			done = append(done, u.Nick)
		}
	}
	return map[string]any{"invited": done, "failed": failed}, nil
}

func (p *Project) inviteTo(hp *HubProject, nick string) (*HubInvite, error) {
	h := p.Cfg.Hub()
	u := h.UserByNick(nick)
	if u == nil {
		return nil, fmt.Errorf("пользователь «%s» не найден", strings.TrimSpace(nick))
	}
	if u.ID == p.Cfg.UserID {
		return nil, errors.New("это ты сам ")
	}
	if !strings.EqualFold(u.GitHub, p.Cfg.GitHubLogin) {
		if err := ghAddCollaborator(p.Cfg, hp.Repo, u.GitHub); err != nil {
			return nil, err
		}
	}
	return h.Invite(hp.ID, u.Nick)
}

func (p *Project) RemoveParticipant(userID string) error {
	hp, err := p.serverProject()
	if err != nil {
		return err
	}
	login, err := p.Cfg.Hub().RemoveMember(hp.ID, userID)
	if err != nil {
		return err
	}
	switch {
	case userID == p.Cfg.UserID: // leaving: GitHub may refuse, the owner can remove the access
		ghRemoveCollaborator(p.Cfg, hp.Repo, p.Cfg.GitHubLogin)
	case hp.IsOwner && !strings.EqualFold(login, p.Cfg.GitHubLogin):
		return ghRemoveCollaborator(p.Cfg, hp.Repo, login)
	}
	return nil
}

// CloneServerProject accepts the GitHub invitation (if any) and downloads.
// cloneOnce: один и тот же проект не скачивается двумя задачами сразу (приглашение + «Доступные»).
func (s *Server) cloneOnce(j *Job, hp *HubProject, parent, group string, addLocal func(dir, group string)) (any, error) {
	if other, busy := s.cloning.LoadOrStore(hp.ID, j.ID); busy && other != j.ID {
		j.Logf("Внимание: «%s» уже скачивается — смотри соседнюю задачу", hp.Name)
		return map[string]string{"path": "", "already": other.(string)}, nil
	}
	defer s.cloning.Delete(hp.ID)
	for _, lp := range s.cfg.Projects { // уже есть на этом компьютере
		if exists(lp.Path) && normRemote((&Git{Dir: lp.Path, Cfg: s.cfg}).Remote()) == normRemote(hp.CloneURL) && hp.CloneURL != "" {
			j.Logf("Внимание: «%s» уже есть на этом компьютере: %s", hp.Name, lp.Path)
			return map[string]string{"path": lp.Path}, nil
		}
	}
	dir, err := CloneServerProject(s.cfg, j, hp, parent)
	var fe *FixError
	if errors.As(err, &fe) && fe.Fix != nil && fe.Fix.Path != "" {
		s.fixPaths.Store(fe.Fix.Path, true)
	}
	addLocal(dir, group)
	if err == nil {
		s.autoRequestMissing(j, dir)
	}
	return map[string]string{"path": dir}, err
}

func CloneServerProject(cfg *Config, j *Job, hp *HubProject, parent string) (string, error) {
	if err := ghAcceptInvitation(cfg, hp.Repo); err != nil {
		j.Logf("Внимание: GitHub: %v", err)
	}
	url := hp.CloneURL
	if url == "" {
		url = "https://github.com/" + hp.Repo + ".git"
	}
	return CloneNamed(cfg, j, url, parent, hp.Name)
}

// unfinishedServerProject — проект на сервере, который эта папка уже начала создавать, но не доделала:
// у папки уже прописан его репозиторий, либо это мой проект с таким же именем репозитория,
// которого нет ни в одной папке на этом компьютере (прошлая попытка упала до записи адреса в папку).
func (p *Project) unfinishedServerProject(name string) *HubProject {
	h := p.Cfg.Hub()
	g := p.git()
	if g.IsRepo() {
		if r := g.Remote(); r != "" {
			if hp := h.ProjectForRemote(r); hp != nil && hp.IsOwner {
				return hp
			}
			return nil // у папки уже есть другой репозиторий — его не трогаем
		}
	}
	ps, err := h.Projects(0)
	if err != nil {
		return nil
	}
	used := map[string]bool{}
	for _, lp := range p.Cfg.Projects {
		if r := (&Git{Dir: lp.Path, Cfg: p.Cfg}).Remote(); r != "" {
			used[normRemote(r)] = true
		}
	}
	want := strings.ToLower(repoNameFor(name))
	for i := range ps {
		hp := ps[i]
		if !hp.IsOwner || used[normRemote(hp.CloneURL)] {
			continue
		}
		_, repo, _ := strings.Cut(hp.Repo, "/")
		if strings.EqualFold(hp.Name, name) || strings.ToLower(repo) == want {
			return &hp
		}
	}
	return nil
}

// inviteList — ники из запроса: список (ники могут быть с пробелами — «Кирюха на MSI») или, по-старому,
// строка через запятую / точку с запятой / перевод строки. По пробелам не делим.
func inviteList(v any) []string {
	var out []string
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				add(s)
			}
		}
	case string:
		for _, n := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
			add(n)
		}
	}
	return out
}
