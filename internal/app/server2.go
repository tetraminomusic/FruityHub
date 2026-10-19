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
	Unread   int             `json:"unread"`   // new comments from others
	Incoming []PluginRequest `json:"incoming"` // others ask me for a plugin
	Ready    []PluginRequest `json:"ready"`    // my plugin requests that were approved
	Behind   int             `json:"behind"`
	Checked  string          `json:"checked"`
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
	for _, r := range p.Requests() {
		if r.To == me && r.Status == ReqPending {
			n.Incoming = append(n.Incoming, r)
		}
		if r.From == me && r.Status == ReqApproved {
			n.Ready = append(n.Ready, r)
		}
	}
	_, n.Behind = g.AheadBehind()
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
			if n := s.notes[lp.Path]; n != nil {
				behindBefore = n.Behind
			}
			if g.IsRepo() && g.Remote() != "" && s.mu.TryLock() {
				g.Fetch()
				s.mu.Unlock()
			}
			n := s.computeNotes(lp.Path)
			if behindBefore >= 0 && n.Behind > behindBefore {
				who, _ := g.RunQuiet("log", "-1", "--format=%an: %s", "origin/"+g.Branch())
				LogActivity(lp.Path, "info", "На сервере новое — "+who)
			}
			s.autoApprove(lp.Path, n)
		}
		if s.cfg.Token != "" {
			if inv, err := PendingInvitations(s.cfg); err == nil {
				s.notesMu.Lock()
				s.invites = inv
				s.notesMu.Unlock()
			}
		}
		time.Sleep(every)
	}
}

// autoApprove sends plugins to trusted friends without asking (only when the
// plugin can be sent as is; installers always need a human).
func (s *Server) autoApprove(path string, n *ProjectNotes) {
	for _, r := range n.Incoming {
		trusted := false
		for _, t := range s.cfg.TrustedRequesters {
			trusted = trusted || strings.EqualFold(t, r.From)
		}
		if !trusted {
			continue
		}
		p := &Project{Dir: path, Cfg: s.cfg}
		in, err := p.Inspect(r.ID)
		if err != nil || !in.Compatible {
			continue
		}
		id, from, plugin := r.ID, r.From, r.Plugin
		s.startJob("Отправка плагина "+plugin+" для "+from+" (без вопросов)", path, func(j *Job) (any, error) {
			p := &Project{Dir: path, Cfg: s.cfg, Job: j}
			err := p.ApproveRequest(id, "", false)
			s.computeNotes(path)
			return "ok", err
		})
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
		defer s.notesMu.Unlock()
		return map[string]any{"projects": s.notes, "invites": s.invites}, nil
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
		return map[string]any{"comments": cs, "status": st, "requests": p.Requests(), "me": s.cfg.MachineID()}, nil
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

	// ----- plugin transfers -----
	api("/api/plugin/request", func(r *http.Request) (any, error) {
		b := body(r)
		return locked(func() (any, error) {
			p, err := proj(str(b, "path"))
			if err != nil {
				return nil, err
			}
			req, err := p.RequestPlugin(str(b, "name"), str(b, "format"), str(b, "vendor"), str(b, "to"))
			s.computeNotes(p.Dir)
			return req, err
		})
	})
	api("/api/plugin/decline", func(r *http.Request) (any, error) {
		b := body(r)
		return locked(func() (any, error) {
			p, err := proj(str(b, "path"))
			if err != nil {
				return nil, err
			}
			err = p.DeclineRequest(str(b, "id"), str(b, "reason"))
			s.computeNotes(p.Dir)
			return "ok", err
		})
	})
	api("/api/plugin/cancel", func(r *http.Request) (any, error) {
		b := body(r)
		return locked(func() (any, error) {
			p, err := proj(str(b, "path"))
			if err != nil {
				return nil, err
			}
			err = p.CancelRequest(str(b, "id"))
			s.computeNotes(p.Dir)
			return "ok", err
		})
	})
	jobAPI("/api/plugin/approve", "Отправка плагина", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		trust, _ := b["trust"].(bool)
		err = p.ApproveRequest(str(b, "id"), str(b, "file"), trust)
		s.computeNotes(p.Dir)
		return "ok", err
	})
	jobAPI("/api/plugin/install", "Установка плагина", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		dst, err := p.InstallRequest(str(b, "id"))
		s.computeNotes(p.Dir)
		return dst, err
	})
}
