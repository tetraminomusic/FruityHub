package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

func (s *Server) routes3(api func(string, func(*http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	strs := func(m map[string]any, k string) []string {
		var out []string
		if arr, ok := m[k].([]any); ok {
			for _, v := range arr {
				if x, ok := v.(string); ok {
					out = append(out, x)
				}
			}
		}
		return out
	}
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }
	jobAPI := func(path, title string, fn func(b map[string]any, j *Job) (any, error)) {
		api(path, func(r *http.Request) (any, error) {
			b := body(r)
			p, _ := b["path"].(string)
			return s.startJob(title, p, func(j *Job) (any, error) { return fn(b, j) }), nil
		})
	}

	// ----- registration & people -----
	api("/api/register", func(r *http.Request) (any, error) {
		b := body(r)
		s.mu.Lock()
		defer s.mu.Unlock()
		if v := strings.TrimSpace(str(b, "userName")); v != "" {
			s.cfg.UserName = v
		}
		if v := strings.TrimSpace(str(b, "token")); v != "" {
			s.cfg.Token = v
		}
		if v, ok := b["registry"].(string); ok {
			s.cfg.Registry = strings.TrimSpace(v)
		}
		note, err := RegisterNick(s.cfg)
		s.cfg.Save()
		if err != nil {
			return nil, err
		}
		LogActivity("", "ok", "Регистрация: "+s.cfg.UserName+" (GitHub @"+s.cfg.GitHubLogin+")")
		return map[string]any{"login": s.cfg.GitHubLogin, "registered": s.cfg.Registered, "note": note}, nil
	})
	api("/api/whoami", func(r *http.Request) (any, error) {
		tok := str(body(r), "token")
		if tok == "" {
			tok = s.cfg.Token
		}
		login, err := GitHubLogin(tok)
		return map[string]string{"login": login}, err
	})
	api("/api/lookup", func(r *http.Request) (any, error) {
		u, reg, err := ResolvePerson(s.cfg, r.URL.Query().Get("nick"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"nick": u.Nick, "login": u.Login, "registered": reg}, nil
	})
	api("/api/project/participants", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		return Participants(s.cfg, p.git().Remote())
	})
	api("/api/project/invite", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		u, err := InviteToProject(s.cfg, p.git().Remote(), str(b, "nick"))
		if err == nil {
			LogActivity(p.Dir, "ok", "Приглашение отправлено: "+u.Nick)
		}
		return u, err
	})
	jobAPI("/api/invitations/accept", "Принимаю приглашение", func(b map[string]any, j *Job) (any, error) {
		id, _ := b["id"].(float64)
		if err := AcceptInvitation(s.cfg, int64(id)); err != nil {
			return nil, err
		}
		j.Logf("Приглашение принято")
		parent := str(b, "parent")
		if parent == "" {
			return nil, errors.New("не выбрана папка, куда скачать проект")
		}
		s.cfg.LastParent = parent
		dir, err := Clone(s.cfg, j, str(b, "cloneUrl"), parent)
		if dir != "" && s.cfg.Project(dir) == nil {
			s.cfg.Projects = append(s.cfg.Projects, LocalProject{Path: dir, RelinkOnReceive: true, Group: str(b, "group")})
		}
		s.cfg.Save()
		s.notesMu.Lock()
		var rest []Invitation
		for _, inv := range s.invites {
			if inv.ID != int64(id) {
				rest = append(rest, inv)
			}
		}
		s.invites = rest
		s.notesMu.Unlock()
		return map[string]string{"path": dir}, err
	})

	// ----- existing repo -----
	api("/api/project/adopt-info", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return p.AdoptInfo(), nil
	})
	jobAPI("/api/project/adopt", "Подключаю репозиторий", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		return "ok", p.Adopt()
	})

	// ----- layout -----
	jobAPI("/api/project/split", "Разделяю проекты", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		group := ""
		if lp := s.cfg.Project(p.Dir); lp != nil {
			group = lp.Group
		}
		return p.Split(strs(b, "extract"), strs(b, "keep"), group)
	})
	jobAPI("/api/project/organize", "Навожу порядок в папке", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		moved, err := p.OrganizeAudio()
		if err != nil {
			return nil, err
		}
		if len(moved) > 0 {
			p.Relink()
			j.Logf("Перенесено в %s/: %d файл(ов), пути в .flp обновлены", audioDir, len(moved))
		} else {
			j.Logf("В корне проекта нет аудио — всё уже в порядке")
		}
		return len(moved), nil
	})
	api("/api/project/trash", func(r *http.Request) (any, error) {
		b := body(r)
		s.mu.Lock()
		defer s.mu.Unlock()
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		n, err := p.TrashUnused(strs(b, "files"))
		if n > 0 {
			LogActivity(p.Dir, "ok", "Удалено в корзину неиспользуемых аудио: "+itoa(n))
		}
		return n, err
	})

	// ----- folders in the project list -----
	api("/api/groups/create", func(r *http.Request) (any, error) {
		name := strings.TrimSpace(str(body(r), "name"))
		if name == "" {
			return nil, errors.New("пустое название")
		}
		b := make([]byte, 4)
		rand.Read(b)
		g := Group{ID: hex.EncodeToString(b), Name: name}
		s.cfg.Groups = append(s.cfg.Groups, g)
		return g, s.cfg.Save()
	})
	api("/api/groups/update", func(r *http.Request) (any, error) {
		b := body(r)
		for i := range s.cfg.Groups {
			if s.cfg.Groups[i].ID == str(b, "id") {
				if n := strings.TrimSpace(str(b, "name")); n != "" {
					s.cfg.Groups[i].Name = n
				}
				if c, ok := b["collapsed"].(bool); ok {
					s.cfg.Groups[i].Collapsed = c
				}
			}
		}
		return "ok", s.cfg.Save()
	})
	api("/api/groups/delete", func(r *http.Request) (any, error) {
		id := str(body(r), "id")
		var out []Group
		for _, g := range s.cfg.Groups {
			if g.ID != id {
				out = append(out, g)
			}
		}
		s.cfg.Groups = out
		for i := range s.cfg.Projects {
			if s.cfg.Projects[i].Group == id {
				s.cfg.Projects[i].Group = "" // projects stay, just move to the top level
			}
		}
		return "ok", s.cfg.Save()
	})
	api("/api/projects/move", func(r *http.Request) (any, error) {
		b := body(r)
		lp := s.cfg.Project(str(b, "path"))
		if lp == nil {
			return nil, errors.New("проект не найден")
		}
		lp.Group = str(b, "group")
		return "ok", s.cfg.Save()
	})

	// ----- journal -----
	api("/api/activity", func(r *http.Request) (any, error) { return Activities(), nil })

	// ----- plugins -----
	api("/api/plugin/inspect", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		return p.Inspect(r.URL.Query().Get("id"))
	})
	api("/api/plugin/untrust", func(r *http.Request) (any, error) {
		nick := str(body(r), "nick")
		var out []string
		for _, t := range s.cfg.TrustedRequesters {
			if !strings.EqualFold(t, nick) {
				out = append(out, t)
			}
		}
		s.cfg.TrustedRequesters = out
		return "ok", s.cfg.Save()
	})
	api("/api/pickanyfile", func(r *http.Request) (any, error) {
		p, _ := PickAnyFile()
		return map[string]string{"path": p, "name": filepath.Base(p)}, nil
	})
}

func itoa(n int) string { return strconv.Itoa(n) }
