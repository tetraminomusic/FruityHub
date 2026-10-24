package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------- проект переехал: найти папку заново ----------
//
// Для каждого проекта FruityHub запоминает имя главного .flp и адрес репозитория. Если папку перенесли
// или переименовали, её можно найти по ним: в «папках с проектами» (Настройки → Проекты) и рядом со старым
// местом. Нашлась одна подходящая — путь в списке меняется сам; несколько — пользователь выбирает.

// FoundProject — папка с проектом FL, найденная при поиске.
type FoundProject struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	FLPs      []string `json:"flps"`
	Remote    string   `json:"remote,omitempty"`
	Match     string   `json:"match,omitempty"` // remote — тот же репозиторий, flp — тот же .flp
	Known     bool     `json:"known,omitempty"` // уже в списке FruityHub
	Moved     string   `json:"moved,omitempty"` // это, похоже, переехавший проект из списка (его старый путь)
	MovedName string   `json:"movedName,omitempty"`
}

// topFLPs — .flp в корне папки (без автосохранений).
func topFLPs(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		l := strings.ToLower(e.Name())
		if !e.IsDir() && strings.HasSuffix(l, ".flp") && !strings.Contains(l, "autosave") && !strings.Contains(l, "overwritten") && !strings.Contains(l, "(конфликт") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// scanProjectRoots ищет папки проектов FL (в корне есть .flp) в roots на 4 уровня вглубь.
func scanProjectRoots(cfg *Config, roots []string) []FoundProject {
	var out []FoundProject
	seen := map[string]bool{}
	deadline := time.Now().Add(8 * time.Second)
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if time.Now().After(deadline) || seen[strings.ToLower(dir)] {
			return
		}
		seen[strings.ToLower(dir)] = true
		if f := topFLPs(dir); len(f) > 0 {
			fp := FoundProject{Path: dir, Name: filepath.Base(dir), FLPs: f}
			if g := (&Git{Dir: dir, Cfg: cfg}); exists(filepath.Join(dir, ".git")) {
				fp.Remote = g.Remote()
			}
			fp.Known = cfg.Project(dir) != nil
			out = append(out, fp)
			return // внутрь проекта не идём
		}
		if depth >= 4 {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() || strings.HasPrefix(n, ".") || skipDir(n) || strings.EqualFold(n, "Audio") || strings.HasSuffix(strings.ToLower(n), ".app") {
				continue
			}
			walk(filepath.Join(dir, n), depth+1)
		}
	}
	for _, r := range roots {
		if r = strings.TrimSpace(r); r != "" && dirExists(r) {
			walk(r, 0)
		}
	}
	return out
}

// rememberProject запоминает приметы проекта (главный .flp, репозиторий) — по ним его найдут после переезда.
func (s *Server) rememberProject(lp *LocalProject) bool {
	if !dirExists(lp.Path) {
		return false
	}
	changed := false
	if f := topFLPs(lp.Path); len(f) > 0 {
		main, best := f[0], time.Time{} // главный — тот, что сохраняли последним
		for _, n := range f {
			if st, err := os.Stat(filepath.Join(lp.Path, n)); err == nil && st.ModTime().After(best) {
				main, best = n, st.ModTime()
			}
		}
		if lp.MainFLP != main {
			lp.MainFLP, changed = main, true
		}
	}
	if r := (&Git{Dir: lp.Path, Cfg: s.cfg}).Remote(); r != "" && lp.Remote != r {
		lp.Remote, changed = r, true
	}
	return changed
}

// locateCandidates — где может быть проект, которого нет на старом месте.
func (s *Server) locateCandidates(lp *LocalProject) []FoundProject {
	roots := append([]string{}, s.cfg.ProjectRoots...)
	// рядом со старым местом: папку часто просто переименовывают или переносят на уровень выше/ниже
	parent := filepath.Dir(lp.Path)
	for i := 0; i < 2 && parent != "" && parent != filepath.Dir(parent); i++ {
		if dirExists(parent) {
			roots = append(roots, parent)
			break
		}
		parent = filepath.Dir(parent)
	}
	var out []FoundProject
	for _, f := range scanProjectRoots(s.cfg, roots) {
		if f.Known {
			continue
		}
		switch {
		case lp.Remote != "" && f.Remote != "" && normRemote(f.Remote) == normRemote(lp.Remote):
			f.Match = "remote"
		case lp.MainFLP != "" && containsFold(f.FLPs, lp.MainFLP):
			if lp.Remote != "" && f.Remote != "" { // это другой репозиторий с таким же .flp — не он
				continue
			}
			f.Match = "flp"
		case lp.MainFLP == "" && containsFold(f.FLPs, filepath.Base(lp.Path)+".flp"): // старая запись без приметы — по имени папки
			f.Match = "name" // только предложить, сам не переносим
		default:
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Match == "remote" && out[j].Match != "remote" })
	return out
}

func containsFold(l []string, x string) bool {
	for _, v := range l {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}

// relocateProject меняет путь проекта в списке (название, папка в списке и настройки сохраняются).
func (s *Server) relocateProject(oldPath, newPath string) error {
	newPath = filepath.Clean(strings.TrimSpace(newPath))
	lp := s.cfg.Project(oldPath)
	if lp == nil {
		if s.cfg.Project(newPath) != nil {
			return nil // уже перенесли (сам нашёл в фоне)
		}
		return errors.New("проект не найден в списке")
	}
	if !dirExists(newPath) {
		return fmt.Errorf("папка не найдена: %s", newPath)
	}
	if len(topFLPs(newPath)) == 0 {
		return errors.New("в этой папке нет проекта FL Studio (.flp)")
	}
	if other := s.cfg.Project(newPath); other != nil && other != lp {
		return errors.New("эта папка уже есть в списке отдельным проектом")
	}
	if lp.Remote != "" {
		if r := (&Git{Dir: newPath, Cfg: s.cfg}).Remote(); r != "" && normRemote(r) != normRemote(lp.Remote) {
			return errors.New("в этой папке другой проект: у неё другой репозиторий на GitHub")
		}
	}
	old := lp.Path
	lp.Path = newPath
	forgetRemote(old)
	hubIDCache.Delete(old)
	s.notesMu.Lock()
	delete(s.notes, old)
	s.notesMu.Unlock()
	s.rememberProject(lp)
	LogActivity(newPath, "ok", "Папка проекта переехала: "+old+" → "+newPath)
	return s.cfg.Save()
}

// RelocateWatcher раз в минуту ищет пропавшие проекты; если подходящая папка ровно одна — переносит путь сам.
func (s *Server) RelocateWatcher(every time.Duration) {
	time.Sleep(5 * time.Second)
	for {
		changed := false
		for i := range s.cfg.Projects {
			lp := &s.cfg.Projects[i]
			if dirExists(lp.Path) {
				changed = s.rememberProject(lp) || changed
			}
		}
		if changed {
			s.cfg.Save()
		}
		var missing []string
		for _, lp := range s.cfg.Projects {
			if !dirExists(lp.Path) {
				missing = append(missing, lp.Path)
			}
		}
		for _, p := range missing {
			lp := s.cfg.Project(p)
			if lp == nil {
				continue
			}
			c := s.locateCandidates(lp)
			strong := 0
			for _, f := range c {
				if f.Match == "remote" {
					strong++
				}
			}
			if len(c) == 1 && c[0].Match != "name" || strong == 1 && c[0].Match == "remote" {
				if !s.mu.TryLock() { // идёт синхронизация — попробуем в следующий раз
					continue
				}
				err := s.relocateProject(p, c[0].Path)
				s.mu.Unlock()
				if err == nil {
					DevLog("info", 0, false, "проект нашёлся на новом месте: %s → %s", p, c[0].Path)
				}
			}
		}
		time.Sleep(every)
	}
}

func (s *Server) routesRelocate(api func(string, func(r *http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }
	// где может быть пропавший проект
	api("/api/project/locate", func(r *http.Request) (any, error) {
		lp := s.cfg.Project(str(body(r), "path"))
		if lp == nil {
			return nil, errors.New("проект не найден в списке")
		}
		c := s.locateCandidates(lp)
		if c == nil {
			c = []FoundProject{}
		}
		return map[string]any{"candidates": c, "flp": lp.MainFLP, "roots": len(s.cfg.ProjectRoots)}, nil
	})
	api("/api/project/open-file", func(r *http.Request) (any, error) {
		b := body(r)
		return "ok", s.openProjectFile(str(b, "path"), str(b, "rel"))
	})
	api("/api/project/relocate", func(r *http.Request) (any, error) {
		b := body(r)
		return "ok", s.relocateProject(str(b, "path"), str(b, "newPath"))
	})
	// папки с проектами: GET — проекты в них, которых ещё нет в списке; POST {roots} — сохранить список папок
	api("/api/projectroots", func(r *http.Request) (any, error) {
		if r.Method == "POST" {
			var req struct {
				Roots []string `json:"roots"`
			}
			if err := decode(r, &req); err != nil {
				return nil, err
			}
			var l []string
			for _, x := range req.Roots {
				if x = strings.TrimSpace(x); x != "" {
					l = append(l, filepath.Clean(x))
				}
			}
			s.cfg.ProjectRoots = l
			if err := s.cfg.Save(); err != nil {
				return nil, err
			}
		}
		found := scanProjectRoots(s.cfg, s.cfg.ProjectRoots)
		// уже в списке по репозиторию (папка та же, просто путь записан иначе) — тоже «известные»
		remotes := map[string]bool{}
		for _, lp := range s.cfg.Projects {
			if lp.Remote != "" {
				remotes[normRemote(lp.Remote)] = true
			}
		}
		for i := range found {
			if found[i].Remote != "" && remotes[normRemote(found[i].Remote)] {
				found[i].Known = true
			}
		}
		// пропавшие проекты из списка: найденная папка — их новое место, а не новый проект
		for _, lp := range s.cfg.Projects {
			if dirExists(lp.Path) {
				continue
			}
			lp := lp
			for _, c := range s.locateCandidates(&lp) {
				for i := range found {
					if found[i].Path == c.Path && !found[i].Known && found[i].Moved == "" {
						found[i].Moved, found[i].MovedName = lp.Path, lp.DisplayName()
					}
				}
			}
		}
		if found == nil {
			found = []FoundProject{}
		}
		return map[string]any{"roots": s.cfg.ProjectRoots, "found": found}, nil
	})
}

// openProjectFile открывает файл проекта программой по умолчанию (.flp — в FL Studio): копии конфликта лежат
// в скрытой папке .flsync/conflicts, в Finder их не видно.
func (s *Server) openProjectFile(path, rel string) error {
	if s.cfg.Project(path) == nil {
		return errors.New("проект не найден в списке")
	}
	rel = filepath.ToSlash(rel)
	if rel == "" || strings.Contains(rel, "..") || filepath.IsAbs(rel) {
		return errors.New("неверный файл")
	}
	abs := filepath.Join(path, filepath.FromSlash(rel))
	if !exists(abs) {
		return errors.New("файла уже нет")
	}
	return OpenFile(abs)
}
