package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Локальное API для: своего названия проекта, запуска, удаления, названия в .flp, версии FL, своего фона,
// перетаскивания папок, файлов к ссылкам плагинов, обновлений, режима разработчика и админки сервера.

// ---------- админка сервера (ключ администратора) ----------

func (h *Hub) admin(method, path string, body io.Reader, out any) error {
	if h.cfg.AdminKey == "" {
		return errors.New("впиши ключ администратора сервера (он в server.txt после server-up.sh)")
	}
	req, err := http.NewRequest(method, h.cfg.ServerURL()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Admin-Key", h.cfg.AdminKey)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("сервер недоступен: %v", err)
	}
	defer resp.Body.Close()
	return readHubResp(resp, out)
}

// adminRaw — ответ админского API как есть (файл журнала).
func (h *Hub) adminRaw(method, path string) ([]byte, error) {
	if h.cfg.AdminKey == "" {
		return nil, errors.New("впиши ключ администратора сервера")
	}
	req, err := http.NewRequest(method, h.cfg.ServerURL()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Admin-Key", h.cfg.AdminKey)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("сервер недоступен: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, readHubResp(resp, nil)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
}

func (s *Server) routes5(api func(string, func(r *http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }
	h := func() *Hub { return s.cfg.Hub() }
	jobAPI := func(path, title string, fn func(b map[string]any, j *Job) (any, error)) {
		api(path, func(r *http.Request) (any, error) {
			b := body(r)
			j := s.startJobOpt(title, str(b, "path"), false, func(j *Job) (any, error) { return fn(b, j) })
			return map[string]string{"id": j.ID, "job": j.ID, "title": title}, nil // интерфейс ждёт id
		})
	}

	// ----- проекты -----
	api("/api/project/rename", func(r *http.Request) (any, error) {
		b := body(r)
		lp := s.cfg.Project(str(b, "path"))
		if lp == nil {
			return nil, errors.New("проект не найден")
		}
		lp.Name = strings.TrimSpace(str(b, "name"))
		if lp.Name == filepath.Base(lp.Path) {
			lp.Name = ""
		}
		if err := s.cfg.Save(); err != nil {
			return nil, err
		}
		res := map[string]any{"name": lp.DisplayName()}
		p := &Project{Dir: lp.Path, Cfg: s.cfg}
		m := rawTrackMeta(lp.Path)
		if b["flTitle"] == true { // то же название — в Project info .flp и в метаданные трека
			if _, err := p.SetFLTitle(lp.DisplayName()); err != nil {
				res["flError"] = err.Error()
			}
			m.Title = lp.DisplayName()
		}
		if a, ok := b["artist"].(string); ok { // артист — в .flp и в метаданные трека
			if _, err := p.SetFLArtist(a); err != nil {
				res["flError"] = err.Error()
			}
			m.Artist = strings.TrimSpace(a)
		}
		if b["artist"] != nil || b["flTitle"] == true && exists(metaPath(lp.Path)) {
			SaveTrackMeta(lp.Path, m) // как ввели — кириллицей (в .flp — латиницей)
		}
		return res, nil
	})
	api("/api/project/launch", func(r *http.Request) (any, error) {
		lb := body(r)
		p, err := s.project(str(lb, "path"), nil)
		if err != nil {
			return nil, err
		}
		var rel string
		if app := str(lb, "app"); app != "" {
			rel, err = p.LaunchWith(app)
		} else {
			rel, err = p.Launch()
		}
		return map[string]any{"flp": rel, "compat": p.FLCompat()}, err
	})
	api("/api/project/delete-files", func(r *http.Request) (any, error) {
		path := str(body(r), "path")
		if s.cfg.Project(path) == nil {
			return nil, errors.New("проекта нет в списке")
		}
		s.mu.Lock() // не во время отправки/получения
		defer s.mu.Unlock()
		return "ok", s.TrashProject(path)
	})
	api("/api/project/fltitle", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		changed := false
		if t, ok := b["title"].(string); ok {
			c, err := p.SetFLTitle(t)
			if err != nil {
				return nil, err
			}
			changed = changed || c
		}
		if a, ok := b["artist"].(string); ok {
			c, err := p.SetFLArtist(a)
			if err != nil {
				return nil, err
			}
			changed = changed || c
			m := rawTrackMeta(p.Dir) // артист как ввели — для списка проектов и рендеров
			m.Artist = strings.TrimSpace(a)
			SaveTrackMeta(p.Dir, m)
		}
		_, okT := b["title"].(string)
		_, okA := b["artist"].(string)
		if okT || okA {
			// что на самом деле записано в .flp (кириллица — латиницей)
			res := map[string]any{"changed": changed}
			for _, k := range []string{"title", "artist"} {
				if v, ok := b[k].(string); ok && hasCyrillic(v) {
					res["latin"] = toLatin(strings.TrimSpace(v))
				}
			}
			return res, nil
		}
		t, err := p.FLTitle()
		return map[string]any{"title": t, "artist": p.FLArtist(), "flp": p.MainFLP(), "compat": p.FLCompat()}, err
	})
	api("/api/flversions", func(r *http.Request) (any, error) { return InstalledFLVersions(), nil })
	api("/api/flapps", func(r *http.Request) (any, error) {
		a := InstalledFLApps()
		if a == nil {
			a = []FLApp{}
		}
		return a, nil
	})

	// ----- репозитории на GitHub -----
	api("/api/github/repos", func(r *http.Request) (any, error) {
		rs, err := MyProjectRepos(s.cfg)
		if rs == nil {
			rs = []MyRepo{}
		}
		return rs, err
	})
	api("/api/github/delete-repo", func(r *http.Request) (any, error) {
		b := body(r)
		full := str(b, "repo")
		if p := str(b, "path"); p != "" && full == "" { // из настроек проекта: его репозиторий
			pr, err := s.project(p, nil)
			if err != nil {
				return nil, err
			}
			o, n, ok := repoSlug(pr.git().Remote())
			if !ok {
				return nil, errors.New("у проекта нет репозитория на GitHub")
			}
			full = o + "/" + n
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := DeleteGitHubRepo(s.cfg, full); err != nil {
			return nil, err
		}
		if p := str(b, "path"); p != "" && b["trash"] == true {
			if err := s.TrashProject(p); err != nil {
				return map[string]any{"deleted": full, "trashError": err.Error()}, nil
			}
		} else if p != "" && b["unlist"] == true { // убрать из списка FruityHub, файлы на месте
			var keep []LocalProject
			for _, lp := range s.cfg.Projects {
				if !samePath(lp.Path, p) {
					keep = append(keep, lp)
				}
			}
			s.cfg.Projects = keep
			s.cfg.Save()
		}
		return map[string]any{"deleted": full}, nil
	})

	// ----- перетаскивание папки -----
	api("/api/locate-dropped", func(r *http.Request) (any, error) {
		var d DroppedFolder
		if err := decode(r, &d); err != nil {
			return nil, err
		}
		found := s.LocateDropped(d)
		DevLog("info", 0, len(found) == 0, "поиск перетащенной папки «%s» (%d файлов внутри): найдено %d", d.Name, len(d.Children), len(found))
		if found == nil {
			found = []string{}
		}
		return found, nil
	})

	// ----- фон -----
	api("/api/background", func(r *http.Request) (any, error) {
		b := body(r)
		switch str(b, "action") {
		case "pick":
			p, err := PickImageFile()
			if errors.Is(err, errPickerOpen) {
				return nil, err
			}
			if err != nil || strings.TrimSpace(p) == "" {
				return map[string]any{"ok": false}, nil
			}
			if err := SetBackground(strings.TrimSpace(p)); err != nil {
				return nil, err
			}
			stampReset()
		case "reset":
			ResetBackground()
			stampReset()
		}
		if v, ok := b["dim"].(float64); ok {
			s.cfg.BgDim = int(v)
			s.cfg.Save()
		}
		return map[string]any{"ok": true, "custom": customBackground() != "", "dim": s.cfg.BgDim, "stamp": s.assetStamp()}, nil
	})

	// ----- файлы к ссылкам плагинов -----
	jobAPI("/api/plugin/linkfile/add", "Прикрепляю файл к плагину", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		file := str(b, "file")
		if file == "" {
			if file, err = PickAnyFile(); err != nil || strings.TrimSpace(file) == "" {
				return nil, errors.New("файл не выбран")
			}
		}
		return p.AttachLinkFile(j, str(b, "plugin"), strings.TrimSpace(file))
	})
	jobAPI("/api/plugin/linkfile/get", "Скачиваю файл плагина", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		dst, err := p.FetchLinkFile(j, str(b, "plugin"), str(b, "name"))
		if err == nil {
			OpenFolder(filepath.Dir(dst))
		}
		return dst, err
	})
	api("/api/plugin/linkfile/remove", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		return "ok", p.RemoveLinkFile(str(b, "plugin"), str(b, "name"))
	})

	// ----- обновления -----
	api("/api/update/check", func(r *http.Request) (any, error) {
		m, err := h().LatestUpdate()
		if err != nil {
			return nil, err
		}
		_, has := m.Files[Platform()]
		return map[string]any{"current": Version, "latest": m.Version, "newer": newerVersion(m.Version, Version) && has, "notes": m.Notes}, nil
	})
	api("/api/update/skip", func(r *http.Request) (any, error) {
		s.cfg.SkipUpdate = str(body(r), "version")
		return "ok", s.cfg.Save()
	})
	// «Обновить и перезапустить»: если обновление уже идёт (в том числе автоматическое) — та же задача, второй раз не качаем
	api("/api/update/apply", func(r *http.Request) (any, error) {
		j := s.startUpdateJob("Обновляю FruityHub", func(j *Job) (any, error) {
			v, err := ApplyUpdate(s.cfg, j)
			if err != nil {
				return nil, err
			}
			j.Logf("Перезапускаю FruityHub %s…", v)
			go func() { time.Sleep(1500 * time.Millisecond); s.Stop() }()
			return v, nil
		})
		return map[string]string{"id": j.ID, "job": j.ID, "title": j.Title}, nil
	})

	// ----- режим разработчика -----
	api("/api/dev/settings", func(r *http.Request) (any, error) {
		b := body(r)
		if v, ok := b["devMode"].(bool); ok {
			s.cfg.DevMode = v
		}
		if v, ok := b["adminKey"].(string); ok {
			s.cfg.AdminKey = strings.TrimSpace(v)
		}
		if v, ok := b["verbose"].(bool); ok {
			DevVerbose.Store(v)
		}
		return map[string]any{"devMode": s.cfg.DevMode, "hasAdminKey": s.cfg.AdminKey != "", "verbose": DevVerbose.Load()}, s.cfg.Save()
	})
	api("/api/dev/logs", func(r *http.Request) (any, error) {
		return map[string]any{"entries": DevLogSince(int64(atoiDef(r.URL.Query().Get("since"), 0))), "file": DevLogPath(), "verbose": DevVerbose.Load()}, nil
	})
	api("/api/dev/clear", func(r *http.Request) (any, error) { DevLogClear(); return "ok", nil })
	api("/api/dev/openlogs", func(r *http.Request) (any, error) { return "ok", OpenFolder(filepath.Dir(DevLogPath())) })
	api("/api/dev/info", func(r *http.Request) (any, error) {
		return map[string]any{"version": Version, "platform": Platform(), "configDir": ConfigDir(), "server": s.cfg.ServerURL(),
			"userId": s.cfg.UserID, "nick": s.cfg.UserName, "github": s.cfg.GitHubLogin, "flVersions": InstalledFLVersions(),
			"gitTools": CheckTools(), "projects": len(s.cfg.Projects)}, nil
	})

	// ----- админка сервера -----
	api("/api/admin/overview", func(r *http.Request) (any, error) {
		var out map[string]any
		return out, h().admin("GET", "/api/admin/overview", nil, &out)
	})
	api("/api/admin/logs", func(r *http.Request) (any, error) {
		var out json.RawMessage
		err := h().admin("GET", "/api/admin/logs?since="+url.QueryEscape(r.URL.Query().Get("since")), nil, &out)
		return out, err
	})
	api("/api/admin/logs/save", func(r *http.Request) (any, error) {
		b, err := h().adminRaw("GET", "/api/admin/logs/file")
		if err != nil {
			return nil, err
		}
		home, _ := os.UserHomeDir()
		dst := filepath.Join(home, "Downloads", "fruityhub-server-"+time.Now().Format("2006-01-02-150405")+".log")
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return nil, err
		}
		return dst, nil
	})
	api("/api/admin/delete-user", func(r *http.Request) (any, error) {
		var out map[string]any
		err := h().admin("DELETE", "/api/admin/users/"+url.PathEscape(str(body(r), "id")), nil, &out)
		h().forget()
		return out, err
	})
	api("/api/admin/delete-project", func(r *http.Request) (any, error) {
		var out map[string]any
		err := h().admin("DELETE", "/api/admin/projects/"+url.PathEscape(str(body(r), "id")), nil, &out)
		h().forget()
		return out, err
	})
	api("/api/admin/clear-requests", func(r *http.Request) (any, error) {
		var out map[string]any
		return out, h().admin("DELETE", "/api/admin/requests", nil, &out)
	})
	api("/api/admin/move", func(r *http.Request) (any, error) {
		var out map[string]any
		b, _ := json.Marshal(map[string]string{"url": str(body(r), "url")})
		return out, h().admin("POST", "/api/admin/move", strings.NewReader(string(b)), &out)
	})
	api("/api/admin/backup", func(r *http.Request) (any, error) {
		var raw json.RawMessage
		if err := h().admin("GET", "/api/admin/backup", nil, &raw); err != nil {
			return nil, err
		}
		home, _ := os.UserHomeDir()
		dst, _, _ := uniqueTarget(filepath.Join(home, "Downloads"), "fruityhub-backup-"+time.Now().Format("2006-01-02")+".json", "")
		if err := os.WriteFile(dst, raw, 0o600); err != nil {
			return nil, err
		}
		OpenFolder(filepath.Dir(dst))
		return dst, nil
	})
	jobAPI("/api/admin/publish", "Выкладываю обновление на сервер", func(b map[string]any, j *Job) (any, error) {
		file, pl, ver := str(b, "file"), str(b, "platform"), strings.TrimSpace(str(b, "version"))
		if ver == "" || pl == "" {
			return nil, errors.New("укажи версию и систему")
		}
		if file == "" {
			var err error
			if file, err = PickAnyFile(); err != nil || strings.TrimSpace(file) == "" {
				return nil, errors.New("файл не выбран")
			}
		}
		f, err := os.Open(strings.TrimSpace(file))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		st, _ := f.Stat()
		j.Logf("Отправляю %s (%s) для %s…", filepath.Base(file), fmtBytes(st.Size()), pl)
		q := url.Values{"version": {ver}, "name": {filepath.Base(file)}, "notes": {str(b, "notes")}}
		var out map[string]any
		err = h().admin("PUT", "/api/admin/updates/"+url.PathEscape(pl)+"?"+q.Encode(), &progressReader{r: f, total: st.Size(), job: j, verb: "Отправка"}, &out)
		if err == nil {
			j.Logf("✓ Версия %s для %s выложена — приложения увидят её в течение минуты", ver, pl)
		}
		return out, err
	})
}
