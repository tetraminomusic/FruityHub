package app

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OnFocus is set by main to bring the window to the front.
var OnFocus func()

// ProjectNotes is what the background check found for one project.
type ProjectNotes struct {
	Unread  int    `json:"unread"` // new comments from others
	Behind  int    `json:"behind"`
	Ahead   int    `json:"ahead"`   // мои коммиты, ещё не отправленные (-1 — на GitHub ещё нет ветки)
	Changed int    `json:"changed"` // изменённые, но ещё не отправленные файлы
	Remote  bool   `json:"remote"`  // есть репозиторий на GitHub
	Checked string `json:"checked"`
}

func (s *Server) computeNotes(path string) *ProjectNotes {
	p := &Project{Dir: path, Cfg: s.cfg}
	g := p.git()
	n := &ProjectNotes{Checked: time.Now().Format("15:04")}
	if !g.IsRepo() {
		return n
	}
	me := s.cfg.MachineID()
	var seen int64
	if lp := s.cfg.Project(path); lp != nil {
		seen = lp.SeenComments
	}
	cs, _ := p.Comments()
	for _, c := range cs {
		if c.Author != me && c.Time > seen {
			n.Unread++
		}
	}
	state, err := g.workingState()
	if err != nil {
		state.Changes, _ = g.Status()
		state.Ahead, state.Behind = g.AheadBehind()
	}
	n.Ahead, n.Behind = state.Ahead, state.Behind
	n.Remote = g.Remote() != ""
	for _, e := range state.Changes {
		if !e.Hidden {
			n.Changed++
		}
	}
	s.notesMu.Lock()
	s.notes[path] = n
	s.notesMu.Unlock()
	return n
}

// Poller fetches every project in the background so comments, plugin
// requests and "there is something new" show up without pressing anything.
// It only fetches — it never changes files in the project folder.
func (s *Server) Poller(every time.Duration) {
	for {
		for _, lp := range append([]LocalProject{}, s.cfg.Projects...) {
			if !exists(lp.Path) {
				continue
			}
			g := &Git{Dir: lp.Path, Cfg: s.cfg}
			behindBefore := -1
			s.notesMu.Lock()
			if n := s.notes[lp.Path]; n != nil {
				behindBefore = n.Behind
			}
			s.notesMu.Unlock()
			if g.IsRepo() && g.Remote() != "" {
				// Fetch меняет только remote refs, не рабочие файлы; сеть не держит общий mutex интерфейса.
				g.RunQuiet("fetch", "--no-tags", "--prune", "--no-write-fetch-head", "--no-auto-maintenance", "origin")
			}
			select {
			case <-s.quit:
				return
			case <-time.After(150 * time.Millisecond):
			}
			n := s.computeNotes(lp.Path)
			if behindBefore >= 0 && n.Behind > behindBefore {
				who, _ := g.RunQuiet("log", "-1", "--format=%an: %s", "origin/"+g.Branch())
				LogActivity(lp.Path, "info", "На сервере новое — "+who)
			}
		}
		select {
		case <-s.quit:
			return
		case <-time.After(every):
		}
	}
}

func (s *Server) routes2(api func(string, func(*http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }
	locked := func(fn func() (any, error)) (any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return fn()
	}
	proj := func(path string) (*Project, error) { return s.project(path, nil) }

	api("/api/notifications", func(r *http.Request) (any, error) {
		s.notesMu.Lock()
		notes := map[string]*ProjectNotes{}
		for k, v := range s.notes {
			notes[k] = v
		}
		s.notesMu.Unlock()
		out := map[string]any{"projects": notes, "invites": []HubInvite{}, "incoming": []PluginRequest{}, "ready": []PluginRequest{}, "toSend": []PluginRequest{},
			"installers": pendingInstallers(), "reports": pendingReports(), "update": s.AvailableUpdate()}
		if in := s.cfg.Hub().LastInbox(); in != nil {
			out["invites"], out["ready"], out["toSend"] = in.Invites, in.Ready, in.ToSend
			// with sharing allowed, a request is shown only after the app decided it needs a human
			auto := s.cfg.Sharing == "members" || s.cfg.Sharing == "all"
			inc := []PluginRequest{}
			for _, r := range in.Incoming {
				if _, seen := s.busyReq.Load(r.ID); seen || !auto {
					inc = append(inc, r)
				}
			}
			out["incoming"] = inc
		}
		out["serverError"] = s.hubErr()
		out["updating"] = s.updatingJob() // идёт обновление — кнопку «Обновить и перезапустить» не показываем
		out["busy"] = s.busyMap()
		return out, nil
	})
	api("/api/openurl", func(r *http.Request) (any, error) {
		u := str(body(r), "url")
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return nil, errors.New("плохая ссылка")
		}
		return "ok", OpenURL(u)
	})
	api("/api/pluginfolders", func(r *http.Request) (any, error) {
		if r.Method == "POST" {
			ScanInstalled(s.cfg, true)
		}
		return map[string]any{"folders": ScannedFolders(s.cfg), "db": flPluginDBDir(), "dbCount": len(readFLPluginDB()), "count": len(ScanInstalled(s.cfg, false))}, nil
	})
	api("/api/pickfile", func(r *http.Request) (any, error) {
		p, _ := PickAudioFile()
		return map[string]string{"path": p}, nil
	})

	// ----- comments & status -----
	api("/api/project/comments", func(r *http.Request) (any, error) {
		p, err := proj(r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		if r.URL.Query().Get("fetch") == "1" && s.mu.TryLock() {
			p.git().Fetch()
			s.mu.Unlock()
		}
		cs, st := p.Comments()
		// сменил ник — старые комментарии, задачи и статус показываются под новым
		name := s.nickResolver()
		for i := range cs {
			cs[i].Author = name(cs[i].AuthorID, cs[i].Author)
			if cs[i].DoneBy != "" {
				cs[i].DoneBy = name(cs[i].DoneByID, cs[i].DoneBy)
			}
		}
		if st != nil {
			st.By = name(st.ByID, st.By)
		}
		return map[string]any{"comments": cs, "status": st, "me": s.cfg.MachineID()}, nil
	})
	api("/api/project/seen", func(r *http.Request) (any, error) {
		p, err := proj(str(body(r), "path"))
		if err != nil {
			return nil, err
		}
		cs, _ := p.Comments()
		if lp := s.cfg.Project(p.Dir); lp != nil && len(cs) > 0 {
			for _, c := range cs {
				if c.Time > lp.SeenComments {
					lp.SeenComments = c.Time
				}
			}
			s.cfg.Save()
		}
		s.computeNotes(p.Dir)
		return "ok", nil
	})
	api("/api/project/comment", func(r *http.Request) (any, error) {
		b := body(r)
		return locked(func() (any, error) {
			p, err := proj(str(b, "path"))
			if err != nil {
				return nil, err
			}
			return p.AddComment(str(b, "kind"), str(b, "text"))
		})
	})
	api("/api/project/comment/update", func(r *http.Request) (any, error) {
		b := body(r)
		return locked(func() (any, error) {
			p, err := proj(str(b, "path"))
			if err != nil {
				return nil, err
			}
			var done *bool
			if v, ok := b["done"].(bool); ok {
				done = &v
			}
			del, _ := b["delete"].(bool)
			return "ok", p.UpdateComment(str(b, "id"), done, del)
		})
	})
	api("/api/project/status", func(r *http.Request) (any, error) {
		b := body(r)
		return locked(func() (any, error) {
			p, err := proj(str(b, "path"))
			if err != nil {
				return nil, err
			}
			return "ok", p.SetStatus(str(b, "stage"), str(b, "text"))
		})
	})

	// ----- preview track -----
	api("/api/project/preview", func(r *http.Request) (any, error) {
		p, err := proj(r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		return p.Preview()
	})
	jobAPI := func(path, title string, fn func(b map[string]any, j *Job) (any, error)) {
		api(path, func(r *http.Request) (any, error) {
			b := body(r)
			p, _ := b["path"].(string)
			return s.startJob(title, p, func(j *Job) (any, error) { return fn(b, j) }), nil
		})
	}
	jobAPI("/api/project/preview/upload", "Загрузка превью", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		return p.UploadPreview(str(b, "file"))
	})
	// drag & drop: the page sends the file body directly
	api("/api/project/preview/upload-raw", func(r *http.Request) (any, error) {
		name := filepath.Base(r.URL.Query().Get("name"))
		if !previewExts[strings.ToLower(filepath.Ext(name))] {
			return nil, errors.New("можно только mp3, flac или wav")
		}
		dir := filepath.Join(os.TempDir(), "uzbek-upload-"+time.Now().Format("150405.000"))
		os.MkdirAll(dir, 0o755)
		tmp := filepath.Join(dir, name)
		f, err := os.Create(tmp)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(f, r.Body)
		f.Close()
		if err != nil {
			return nil, err
		}
		path := r.URL.Query().Get("path")
		return s.startJob("Загрузка превью", path, func(j *Job) (any, error) {
			defer os.RemoveAll(dir)
			p, err := s.project(path, j)
			if err != nil {
				return nil, err
			}
			return p.UploadPreview(tmp)
		}), nil
	})
	jobAPI("/api/project/preview/fetch", "Скачивание превью", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		return p.PreviewFile(j)
	})
	api("/api/project/preview/delete", func(r *http.Request) (any, error) {
		p, err := proj(str(body(r), "path"))
		if err != nil {
			return nil, err
		}
		return "ok", p.DeletePreview()
	})

}
