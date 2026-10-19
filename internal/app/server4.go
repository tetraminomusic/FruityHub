package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

var profMu sync.Mutex

func profileSig(p *Profile) string {
	c := *p
	c.Updated, c.Device = 0, ""
	b, _ := json.Marshal(c)
	return string(b)
}

// ProfileSync keeps the account profile on GitHub in step with this computer.
func (s *Server) ProfileSync() {
	var lastSig string
	var lastPull time.Time
	for {
		if s.cfg.Token != "" && s.cfg.GitHubLogin != "" && s.cfg.UserName != "" {
			profMu.Lock()
			if time.Since(lastPull) > 2*time.Minute || s.profile == nil {
				if p, _, err := LoadProfile(s.cfg); err == nil {
					lastPull = time.Now()
					if p != nil && p.Updated > s.cfg.ProfileUpdated && (lastSig == "" || profileSig(BuildProfile(s.cfg, s.profile)) == lastSig) {
						ApplyProfile(s.cfg, p) // changed on another computer
						s.cfg.Save()
						LogActivity("", "info", "Настройки обновлены с другого компьютера ("+p.Device+")")
					}
					s.profile = p
					if p != nil {
						lastSig = profileSig(p)
					}
				}
			}
			cur := BuildProfile(s.cfg, s.profile)
			if sig := profileSig(cur); sig != lastSig {
				if err := SaveProfile(s.cfg); err == nil {
					lastSig = sig
					s.profile = cur
				}
			}
			profMu.Unlock()
		}
		time.Sleep(15 * time.Second)
	}
}

func (s *Server) routes4(api func(string, func(*http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }

	api("/api/login", func(r *http.Request) (any, error) {
		b := body(r)
		tok := strings.TrimSpace(str(b, "token"))
		if tok == "" {
			return nil, errors.New("вставь GitHub-токен")
		}
		profMu.Lock()
		defer profMu.Unlock()
		res, err := Login(s.cfg, tok, strings.TrimSpace(str(b, "registry")))
		if err != nil {
			return nil, err
		}
		s.profile, _, _ = LoadProfile(s.cfg)
		LogActivity("", "ok", "Вход: "+res.Nick+" (GitHub @"+res.Login+")")
		return res, nil
	})
	api("/api/available", func(r *http.Request) (any, error) {
		profMu.Lock()
		prof := s.profile
		profMu.Unlock()
		return AvailableRepos(s.cfg, prof)
	})
	api("/api/available/clone", func(r *http.Request) (any, error) {
		b := body(r)
		parent := strings.TrimSpace(str(b, "parent"))
		if parent == "" {
			return nil, errors.New("выбери папку, куда скачать проект")
		}
		return s.startJob("Скачиваю «"+str(b, "name")+"»", "", func(j *Job) (any, error) {
			s.cfg.LastParent = parent
			dir, err := Clone(s.cfg, j, str(b, "cloneUrl"), parent)
			if dir != "" && s.cfg.Project(dir) == nil {
				s.cfg.Projects = append(s.cfg.Projects, LocalProject{Path: dir, RelinkOnReceive: true, Group: str(b, "group")})
			}
			s.cfg.Save()
			return map[string]string{"path": dir}, err
		}), nil
	})
	api("/api/project/remove-participant", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		id, _ := b["inviteId"].(float64)
		err = RemoveParticipant(s.cfg, p.git().Remote(), str(b, "login"), int64(id))
		if err == nil {
			LogActivity(p.Dir, "ok", "Участник убран из проекта: "+str(b, "name"))
		}
		return "ok", err
	})
}
