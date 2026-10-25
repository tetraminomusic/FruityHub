package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
		if str(b, "mode") == "local" { // только у себя: на GitHub и у друзей остаётся
			n, freed, err := p.TrashLocal(strs(b, "files"))
			if n > 0 {
				LogActivity(p.Dir, "ok", fmt.Sprintf("Убрано только с этого компьютера неиспользуемых аудио: %d (освободилось %s)", n, fmtBytes(freed)))
			}
			return map[string]any{"n": n, "freed": freed}, err
		}
		n, err := p.TrashUnused(strs(b, "files"))
		if n > 0 {
			LogActivity(p.Dir, "ok", "Удалено в корзину неиспользуемых аудио: "+itoa(n))
		}
		return n, err
	})
	api("/api/project/hidden", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		l := p.HiddenLocal()
		if l == nil {
			l = []string{}
		}
		return l, nil
	})
	jobAPI("/api/project/restore-hidden", "Возвращаю аудио", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		return "ok", p.RestoreLocal(strs(b, "files"))
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

	api("/api/pickanyfile", func(r *http.Request) (any, error) {
		p, _ := PickAnyFile()
		return map[string]string{"path": p, "name": filepath.Base(p)}, nil
	})
}

func itoa(n int) string { return strconv.Itoa(n) }
