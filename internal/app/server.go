package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flsync/internal/flp"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var Version = "dev"

var (
	stampMu  sync.Mutex
	stampVal string
)

func stampReset() {
	stampMu.Lock()
	stampVal = ""
	stampMu.Unlock()
}

// assetStamp — короткий отпечаток картинок интерфейса: меняется вместе с картинкой (в том числе своим фоном),
// и окно не держит старую из кэша.
func (s *Server) assetStamp() string {
	stampMu.Lock()
	defer stampMu.Unlock()
	if stampVal != "" {
		return stampVal
	}
	h := crc32.NewIEEE()
	h.Write([]byte(Version))
	if s.Assets != nil {
		for _, n := range []string{"bg.jpg", "bg.png", "bg.jpeg", "bg.webp", "icon.png"} {
			if b, err := fs.ReadFile(s.Assets, n); err == nil {
				h.Write(b)
			}
		}
	}
	if c := customBackground(); c != "" {
		if st, err := os.Stat(c); err == nil {
			fmt.Fprint(h, c, st.Size(), st.ModTime().UnixNano())
		}
	}
	stampVal = fmt.Sprintf("%08x", h.Sum32())
	return stampVal
}

type Server struct {
	Assets    fs.FS // background picture, icon (optional)
	cfg       *Config
	token     string
	index     []byte
	mu        sync.Mutex // serialises git operations
	jobsMu    sync.Mutex
	jobs      map[string]*Job
	running   int
	lastPing  time.Time
	quit      chan struct{}
	quitOnce  sync.Once
	Windowed  bool
	notesMu   sync.Mutex
	notes     map[string]*ProjectNotes
	busyReq   sync.Map // plugin request id -> "sending" | "installing" | "human"
	fetchSeen sync.Map // просьбы участников об аудио проектов, которые уже обработаны
	cloning   sync.Map // id проекта на сервере → id задачи, которая его скачивает
	fixPaths  sync.Map // папки из ошибки «уже существует», которые можно убрать кнопкой
	reqJobs   sync.Map // plugin request id -> *Job идущей передачи (для отмены)
	hubError  string
}

func NewServer(index []byte) *Server {
	b := make([]byte, 16)
	rand.Read(b)
	cfg := LoadConfig()
	directOff.Store(cfg.NoLAN)
	go cleanStaleTransfers()
	return &Server{cfg: cfg, token: hex.EncodeToString(b), index: index, jobs: map[string]*Job{},
		lastPing: time.Now(), quit: make(chan struct{}), notes: map[string]*ProjectNotes{}}
}

func (s *Server) Quit() <-chan struct{} { return s.quit }

// LiteUI — включён лёгкий режим окна (Настройки → Вид).
func (s *Server) LiteUI() bool { return s.cfg.LiteUI }

// Stop closes the app (safe to call many times).
func (s *Server) Stop() { s.quitOnce.Do(func() { close(s.quit) }) }

// IdleWatcher stops the app when the UI tab has been closed for a while.
func (s *Server) IdleWatcher(after time.Duration) {
	for range time.Tick(15 * time.Second) {
		s.jobsMu.Lock()
		idle := s.running == 0 && time.Since(s.lastPing) > after
		s.jobsMu.Unlock()
		if idle {
			s.Stop()
			return
		}
	}
}

func (s *Server) Handler(port int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		page := bytes.Replace(s.index, []byte("__FLSYNC_TOKEN__"), []byte(s.token), 1)
		if s.cfg.LiteUI { // лёгкий режим — сразу, чтобы фон даже не начал грузиться
			page = bytes.Replace(page, []byte("<body>"), []byte(`<body class="lite">`), 1)
		}
		// версия в адресе картинок: после обновления окно не держит старый фон из кэша
		w.Write(bytes.ReplaceAll(page, []byte("__V__"), []byte(s.assetStamp())))
	})
	// pictures for the interface: /assets/bg, /assets/icon (any extension in web/)
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if s.Assets == nil || strings.ContainsAny(name, "/\\.") {
			http.NotFound(w, r)
			return
		}
		if name == "bg" { // свой фон пользователя
			if c := customBackground(); c != "" {
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeFile(w, r, c)
				return
			}
		}
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			if b, err := fs.ReadFile(s.Assets, name+ext); err == nil {
				w.Header().Set("Content-Type", map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp"}[ext])
				w.Header().Set("Cache-Control", "no-cache")
				w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	})
	// словарь английского интерфейса
	mux.HandleFunc("/i18n/en.js", func(w http.ResponseWriter, r *http.Request) {
		if s.Assets == nil {
			http.NotFound(w, r)
			return
		}
		b, err := fs.ReadFile(s.Assets, "i18n_en.js")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	})
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "uzbek") })
	mux.HandleFunc("/focus", func(w http.ResponseWriter, r *http.Request) {
		if OnFocus != nil {
			OnFocus()
		}
	})
	// media files for the <audio> player (token in the query: <audio> can't send headers)
	mux.HandleFunc("/media/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != s.token {
			http.Error(w, "forbidden", 403)
			return
		}
		p := &Project{Dir: r.URL.Query().Get("path"), Cfg: s.cfg}
		info, err := p.Preview()
		if err != nil || info.Asset == nil || !info.Cached {
			http.NotFound(w, r)
			return
		}
		st, _ := p.storage()
		w.Header().Set("Content-Type", contentType(info.Asset.Name))
		http.ServeFile(w, r, st.previewCachePath(*info.Asset))
	})
	api := func(path string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-FLSync") != s.token {
				http.Error(w, "forbidden", 403)
				return
			}
			quiet := quietAPI[path]
			started := time.Now()
			res, err := fn(r)
			if !quiet || err != nil {
				q := ""
				if r.URL.RawQuery != "" {
					q = "?" + r.URL.RawQuery
					if u, err := url.QueryUnescape(r.URL.RawQuery); err == nil {
						q = "?" + u
					}
				}
				if err != nil {
					DevLog("ui", time.Since(started).Milliseconds(), true, "%s%s → %v", path, q, err)
				} else {
					DevLog("ui", time.Since(started).Milliseconds(), false, "%s%s", path, q)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(res)
		})
	}
	s.routes(api)
	s.routes2(api)
	s.routes3(api)
	s.routes4(api)
	s.routes5(api)
	s.routes6(api)
	s.routesRelocate(api)
	// DNS-rebinding protection: only accept our own host names
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Host
		if h != fmt.Sprintf("127.0.0.1:%d", port) && h != fmt.Sprintf("localhost:%d", port) {
			http.Error(w, "bad host", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(r.Body)
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// quietAPI — вызовы, которые интерфейс делает сам по таймеру; в журнал попадают только их ошибки.
var quietAPI = map[string]bool{"/api/notifications": true, "/api/admin/logs": true, "/api/jobs": true, "/api/job": true, "/api/dev/logs": true,
	"/api/state": true, "/api/project": true, "/api/projects/status": true, "/api/available": true, "/api/plugin/requests": true}

func (s *Server) startJob(title, project string, fn func(j *Job) (any, error)) *Job {
	return s.startJobOpt(title, project, true, fn)
}

// startJobOpt: lock=false for work that does not touch git (plugin transfers),
// so a big upload never blocks sending/receiving projects.
func (s *Server) startJobOpt(title, project string, lock bool, fn func(j *Job) (any, error)) *Job {
	b := make([]byte, 6)
	rand.Read(b)
	j := &Job{ID: hex.EncodeToString(b), Title: title, Project: project, Started: time.Now().Unix()}
	j.ctx, j.cancel = context.WithCancel(context.Background())
	DevLog("job", 0, false, "▶ %s%s", title, map[bool]string{true: " [" + filepath.Base(project) + "]", false: ""}[project != ""])
	s.jobsMu.Lock()
	s.jobs[j.ID] = j
	s.running++
	s.jobsMu.Unlock()
	go func() {
		if lock {
			s.mu.Lock()
			defer s.mu.Unlock()
		}
		defer func() {
			if rec := recover(); rec != nil {
				j.mu.Lock()
				j.Err = fmt.Sprint("внутренняя ошибка: ", rec)
				j.mu.Unlock()
			}
			j.mu.Lock()
			j.Done = true
			j.mu.Unlock()
			s.jobsMu.Lock()
			s.running--
			s.jobsMu.Unlock()
		}()
		res, err := fn(j)
		if err != nil && j.Stopped() {
			err = errStopped
		}
		j.mu.Lock()
		j.Res = res
		j.Cancelled = errors.Is(err, errStopped)
		if err != nil {
			j.Err = err.Error()
			var fe *FixError
			if errors.As(err, &fe) {
				j.Fix = fe.Fix
			}
			j.Details = errDetails(err)
			if j.Details == "" && len(j.raw) > 0 {
				j.Details = strings.Join(j.raw[max(0, len(j.raw)-15):], "\n")
			}
		}
		last := "" // the most telling line for the journal
		for _, st := range j.Steps {
			if !strings.HasPrefix(st, "✓") && !strings.HasSuffix(st, "…") {
				last = st
				break
			}
		}
		if last == "" && len(j.Steps) > 0 {
			last = j.Steps[len(j.Steps)-1]
		}
		j.mu.Unlock()
		DevLog("job", time.Since(time.Unix(j.Started, 0)).Milliseconds(), err != nil, "■ %s — %s", title, map[bool]string{true: "ошибка: " + fmt.Sprint(err), false: "готово"}[err != nil])
		if err != nil && !errors.Is(err, errCancelled) && !errors.Is(err, errStopped) {
			j.mu.Lock()
			det := j.Details
			j.mu.Unlock()
			if len(det) > 1500 {
				det = det[len(det)-1500:]
			}
			if det != "" {
				reportError("подробности «" + title + "»:\n" + maskSecrets(det))
			}
		}
		if err != nil {
			LogActivity(project, "error", title+": "+err.Error())
		} else {
			LogActivity(project, "ok", title+" — готово"+map[bool]string{true: ". " + strings.TrimPrefix(last, "✓ "), false: ""}[last != ""])
		}
	}()
	return j
}

type pathReq struct {
	Path  string `json:"path"`
	Group string `json:"group"`
}

func (s *Server) project(path string, j *Job) (*Project, error) {
	if path == "" {
		return nil, errors.New("не указан проект")
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("папка не найдена: %s", path)
	}
	return &Project{Dir: path, Cfg: s.cfg, Job: j}, nil
}

type ProjectSummary struct {
	ReqKey   string         `json:"reqKey,omitempty"` // метка запросов плагинов из этого проекта
	Path     string         `json:"path"`
	Name     string         `json:"name"`
	Exists   bool           `json:"exists"`
	IsRepo   bool           `json:"isRepo"`
	Remote   string         `json:"remote"`
	Branch   string         `json:"branch"`
	Lock     *Lock          `json:"lock"`
	Changes  []StatusEntry  `json:"changes"`
	Ahead    int            `json:"ahead"`
	Behind   int            `json:"behind"`
	Me       string         `json:"me"`
	Relink   bool           `json:"relinkOnReceive"`
	Shared   SharedSettings `json:"shared"`
	Machines []Machine      `json:"machines"`
	LastSync string         `json:"lastSync"`
	Artist   string         `json:"artist,omitempty"` // Author из Project info самого свежего .flp
	Modified int64          `json:"modified"`         // когда последний раз сохраняли .flp (unix), для сортировки
	Adopted  bool           `json:"adopted"`
	Extra    []string       `json:"extraFlps"`
	MainFLP  string         `json:"mainFlp"`
	Group    string         `json:"group"`
	Rootless int            `json:"rootAudio"` // audio files lying in the project root
	OnServer bool           `json:"onServer"`  // the remote is a project of our server
	FL       FLCompat       `json:"fl"`        // в какой FL сохранён и хватит ли установленной
	Folder   string         `json:"folder"`    // имя папки (название может быть своим)
}

func (s *Server) summary(path string) ProjectSummary {
	ps := ProjectSummary{Path: path, Name: filepath.Base(path), Me: s.cfg.MachineID()}
	if lp := s.cfg.Project(path); lp != nil {
		ps.Relink = lp.RelinkOnReceive
		ps.Group = lp.Group
		ps.Name = lp.DisplayName()
	}
	if !exists(path) {
		return ps
	}
	ps.Exists = true
	p := &Project{Dir: path, Cfg: s.cfg}
	g := p.git()
	ps.IsRepo = g.IsRepo()
	ps.Shared = LoadShared(path)
	if ps.IsRepo {
		ps.Adopted = p.IsAdopted()
		if !ps.Adopted && exists(filepath.Join(path, ".flsync")) {
			// a project already managed by this app (e.g. made by an older version) — no questions
			g.RunQuiet("lfs", "install", "--local", "--skip-repo")
			g.RunQuiet("config", "uzbek.adopted", "true")
			ps.Adopted = true
		}
		ps.Remote = g.Remote()
		ps.OnServer = s.cfg.Hub().ProjectForRemote(ps.Remote) != nil
		ps.Branch = g.Branch()
		ps.Changes, _ = g.Status()
		ps.Ahead, ps.Behind = g.AheadBehind()
		if fi, err := os.Stat(filepath.Join(path, ".git", "FETCH_HEAD")); err == nil {
			ps.LastSync = fi.ModTime().Format("02.01 15:04")
		}
	}
	ps.Folder = filepath.Base(path)
	ps.FL = p.FLCompat()
	ps.Extra = p.ExtraFLPs()
	if len(ps.Extra) > 0 {
		ps.MainFLP = p.MainFLPGuess(ps.Extra)
	}
	if ents, err := os.ReadDir(path); err == nil {
		for _, e := range ents {
			if !e.IsDir() && flp.IsAudio(e.Name()) {
				ps.Rootless++
			}
		}
	}
	ps.Lock = p.ReadLock()
	ps.ReqKey = p.requestKey()
	ps.Machines = p.Machines()
	for i := range ps.Machines {
		ps.Machines[i].Plugins = nil
	}
	return ps
}

func (s *Server) routes(api func(string, func(*http.Request) (any, error))) {
	api("/api/ping", func(r *http.Request) (any, error) {
		s.jobsMu.Lock()
		s.lastPing = time.Now()
		s.jobsMu.Unlock()
		return "ok", nil
	})
	api("/api/quit", func(r *http.Request) (any, error) {
		go func() { time.Sleep(300 * time.Millisecond); s.Stop() }()
		return "bye", nil
	})
	api("/api/state", func(r *http.Request) (any, error) {
		cfg := *s.cfg
		cfg.Token, cfg.DeviceToken, cfg.hub = "", "", nil
		hasAdmin := cfg.AdminKey != ""
		cfg.AdminKey = ""
		var list []ProjectSummary
		for _, p := range s.cfg.Projects {
			ps := ProjectSummary{Path: p.Path, Name: p.DisplayName(), Exists: exists(p.Path), Group: p.Group}
			pr := &Project{Dir: p.Path, Cfg: s.cfg}
			ps.Lock = pr.ReadLock()
			ps.IsRepo = pr.git().IsRepo()
			ps.Modified, ps.Artist = projectMeta(p.Path)
			list = append(list, ps)
		}
		sharing := s.cfg.Sharing
		if sharing == "" {
			sharing = "ask"
		}
		return map[string]any{"config": cfg, "registered": s.cfg.Hub().Registered(), "projects": list, "windowed": s.Windowed,
			"tools": CheckTools(), "os": OSName(), "me": s.cfg.MachineID(), "userId": s.cfg.UserID, "version": Version, "configDir": ConfigDir(),
			"groups": s.cfg.Groups, "server": s.cfg.ServerURL(), "serverDefault": DefaultServer, "sharing": sharing, "hasToken": s.cfg.Token != "", "githubLogin": s.cfg.GitHubLogin, "serverCodeBuiltIn": DefaultServerCode != "", "proxy": s.cfg.Proxy, "proxyHint": SystemProxyHint(),
			"hasAdminKey": hasAdmin, "customBg": customBackground() != "", "platform": Platform(), "defaultLfs": DefaultLFSExtensions()}, nil
	})
	api("/api/settings", func(r *http.Request) (any, error) {
		var req struct {
			Token           *string       `json:"token"`
			ExtraPluginDirs []string      `json:"extraPluginDirs"`
			Proxy           *string       `json:"proxy"`
			SortMode        *string       `json:"sortMode"`
			AutoUpdate      *bool         `json:"autoUpdate"`
			ErrorReports    *bool         `json:"errorReports"`
			AutoFetch       *bool         `json:"autoFetchPlugins"`
			SeenVersion     *string       `json:"seenVersion"`
			GuideSeen       *bool         `json:"guideSeen"`
			HideHelp        *bool         `json:"hideHelp"`
			PeerFetch       *bool         `json:"peerFetch"`
			LAN             *bool         `json:"lan"`
			ToastSec        *int          `json:"toastSec"`
			LFS             []string      `json:"lfsExtensions"`
			LiteUI          *bool         `json:"liteUi"`
			Lang            *string       `json:"lang"`
			Meta            *MetaSettings `json:"meta"`
			Lite            *struct {
				Plugin string `json:"plugin"`
				On     bool   `json:"on"`
			} `json:"lite"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Token != nil {
			s.cfg.Token = strings.TrimSpace(*req.Token)
		}
		if req.ExtraPluginDirs != nil {
			s.cfg.ExtraPluginDirs = req.ExtraPluginDirs
		}
		if req.SortMode != nil {
			s.cfg.SortMode = *req.SortMode
		}
		if req.AutoUpdate != nil {
			s.cfg.AutoUpdate = *req.AutoUpdate
		}
		if req.ErrorReports != nil {
			s.cfg.NoErrorReports = !*req.ErrorReports
		}
		if req.AutoFetch != nil {
			s.cfg.AutoFetchPlugins = *req.AutoFetch
		}
		if req.SeenVersion != nil {
			s.cfg.SeenVersion = *req.SeenVersion
		}
		if req.PeerFetch != nil {
			s.cfg.NoPeerFetch = !*req.PeerFetch
		}
		if req.LAN != nil {
			s.cfg.NoLAN = !*req.LAN
			directOff.Store(s.cfg.NoLAN)
		}
		if req.HideHelp != nil {
			s.cfg.HideHelp = *req.HideHelp
		}
		if req.ToastSec != nil {
			v := max(0, min(*req.ToastSec, 600))
			s.cfg.ToastSec = &v
		}
		if req.GuideSeen != nil {
			s.cfg.GuideSeen = *req.GuideSeen
		}
		if req.LFS != nil {
			var l []string
			for _, e := range req.LFS {
				if e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), ".")); e != "" {
					l = append(l, e)
				}
			}
			if len(l) == 0 {
				l = DefaultLFSExtensions()
			}
			s.cfg.LFSExtensions = l
			SetGlobalLFS(l)
		}
		if req.LiteUI != nil {
			s.cfg.LiteUI = *req.LiteUI
		}
		if req.Lang != nil {
			s.cfg.Lang = *req.Lang
		}
		if req.Meta != nil {
			s.cfg.Meta = *req.Meta
		}
		if req.Lite != nil && strings.TrimSpace(req.Lite.Plugin) != "" {
			s.cfg.SetLite(req.Lite.Plugin, req.Lite.On)
		}
		if req.Proxy != nil {
			s.cfg.Proxy = strings.TrimSpace(*req.Proxy)
			SetUserProxy(s.cfg.Proxy)
		}
		return "ok", s.cfg.Save()
	})
	api("/api/github/test", func(r *http.Request) (any, error) {
		var req struct {
			Proxy *string `json:"proxy"`
		}
		decode(r, &req)
		if req.Proxy != nil {
			SetUserProxy(*req.Proxy)
			defer SetUserProxy(s.cfg.Proxy)
		}
		u, src := GitHubProxy()
		via := "напрямую"
		if u != nil {
			via = "через " + u.Host + " (" + src + ")"
		}
		t0 := time.Now()
		cl := &http.Client{Timeout: 25 * time.Second}
		resp, err := cl.Get("https://api.github.com")
		if err != nil {
			return map[string]any{"ok": false, "via": via, "error": err.Error()}, nil
		}
		resp.Body.Close()
		return map[string]any{"ok": true, "via": via, "ms": time.Since(t0).Milliseconds()}, nil
	})
	api("/api/pickfolder", func(r *http.Request) (any, error) {
		p, err := PickFolder()
		if err != nil && p == "" {
			return map[string]string{"path": ""}, nil
		}
		return map[string]string{"path": p}, nil
	})
	api("/api/projects/add", func(r *http.Request) (any, error) {
		var req pathReq
		decode(r, &req)
		p := strings.TrimSpace(req.Path)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("папка не найдена: %s", p)
		}
		if s.cfg.Project(p) == nil {
			s.cfg.Projects = append(s.cfg.Projects, LocalProject{Path: p, RelinkOnReceive: true, Group: req.Group})
			pr := &Project{Dir: p, Cfg: s.cfg}
			// название проекта сразу попадает в Project info .flp, если там пусто
			if t, err := pr.FLTitle(); err == nil && strings.TrimSpace(t) == "" {
				pr.SetFLTitle(filepath.Base(p))
			}
			if !pr.git().IsRepo() && LoadShared(p).OrganizeAudio {
				if moved, err := pr.OrganizeAudio(); err == nil && len(moved) > 0 {
					pr.Relink()
					LogActivity(p, "info", fmt.Sprintf("Аудио из корня проекта перенесено в %s/: %d файл(ов)", audioDir, len(moved)))
				}
			}
			sort.Slice(s.cfg.Projects, func(i, j int) bool {
				return strings.ToLower(filepath.Base(s.cfg.Projects[i].Path)) < strings.ToLower(filepath.Base(s.cfg.Projects[j].Path))
			})
		}
		return "ok", s.cfg.Save()
	})
	api("/api/projects/remove", func(r *http.Request) (any, error) {
		var req pathReq
		decode(r, &req)
		var out []LocalProject
		for _, p := range s.cfg.Projects {
			if !samePath(p.Path, req.Path) {
				out = append(out, p)
			}
		}
		s.cfg.Projects = out
		return "ok", s.cfg.Save()
	})
	api("/api/project", func(r *http.Request) (any, error) {
		return s.summary(r.URL.Query().Get("path")), nil
	})
	api("/api/project/analysis", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		a := p.Analyze()
		machines := p.Machines()
		// always include my *current* inventory in the comparison
		me := p.myMachine(a)
		found := false
		for i := range machines {
			if machines[i].User == me.User {
				machines[i] = me
				found = true
			}
		}
		if !found {
			machines = append(machines, me)
		}
		return map[string]any{"analysis": a, "plugins": p.PluginReport(a, machines), "machines": len(machines)}, nil
	})
	api("/api/project/history", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		return p.git().Log(150), nil
	})
	api("/api/project/settings", func(r *http.Request) (any, error) {
		var req struct {
			Path   string          `json:"path"`
			Shared *SharedSettings `json:"shared"`
			Relink *bool           `json:"relinkOnReceive"`
			Remote *string         `json:"remote"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := s.project(req.Path, nil)
		if err != nil {
			return nil, err
		}
		if req.Shared != nil {
			if err := writeJSON(filepath.Join(p.Dir, ".flsync", "settings.json"), req.Shared); err != nil {
				return nil, err
			}
			p.writeAttributes(*req.Shared)
			p.updateIgnore(p.Analyze())
		}
		if req.Relink != nil {
			if lp := s.cfg.Project(p.Dir); lp != nil {
				lp.RelinkOnReceive = *req.Relink
				s.cfg.Save()
			}
		}
		if req.Remote != nil && p.git().IsRepo() {
			forgetRemote(p.Dir)
			g := p.git()
			if g.Remote() == "" {
				_, err = g.Run("remote", "add", "origin", strings.TrimSpace(*req.Remote))
			} else {
				_, err = g.Run("remote", "set-url", "origin", strings.TrimSpace(*req.Remote))
			}
			if err != nil {
				return nil, err
			}
		}
		return "ok", nil
	})
	api("/api/project/include", func(r *http.Request) (any, error) {
		var req struct {
			Path    string `json:"path"`
			File    string `json:"file"`
			Include bool   `json:"include"`
		}
		decode(r, &req)
		p, err := s.project(req.Path, nil)
		if err != nil {
			return nil, err
		}
		sh := LoadShared(p.Dir)
		var list []string
		for _, x := range sh.AlwaysInclude {
			if x != req.File {
				list = append(list, x)
			}
		}
		if req.Include {
			list = append(list, req.File)
		}
		sh.AlwaysInclude = list
		writeJSON(filepath.Join(p.Dir, ".flsync", "settings.json"), sh)
		_, err = p.updateIgnore(p.Analyze())
		return "ok", err
	})
	api("/api/project/catalog", func(r *http.Request) (any, error) {
		var req struct{ Path, Name, URL, Note string }
		decode(r, &req)
		p, err := s.project(req.Path, nil)
		if err != nil {
			return nil, err
		}
		return "ok", p.SetCatalog(req.Name, strings.TrimSpace(req.URL), strings.TrimSpace(req.Note))
	})
	api("/api/project/open", func(r *http.Request) (any, error) {
		var req pathReq
		decode(r, &req)
		return "ok", OpenFolder(req.Path)
	})
	api("/api/rescan", func(r *http.Request) (any, error) {
		return len(ScanInstalled(s.cfg, true)), nil
	})

	// ----- jobs -----
	jobAPI := func(path, title string, fn func(req map[string]any, j *Job) (any, error)) {
		api(path, func(r *http.Request) (any, error) {
			req := map[string]any{}
			if err := decode(r, &req); err != nil {
				return nil, err
			}
			p, _ := req["path"].(string)
			return s.startJob(title, p, func(j *Job) (any, error) { return fn(req, j) }), nil
		})
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	boolean := func(m map[string]any, k string) bool { v, _ := m[k].(bool); return v }

	jobAPI("/api/project/init", "Подключение проекта", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		if err := p.Init(strings.TrimSpace(str(req, "remote"))); err != nil {
			return nil, err
		}
		if _, err := p.commitAll("Начало проекта (" + s.cfg.MachineID() + ")"); err != nil {
			return nil, err
		}
		if p.git().Remote() != "" && boolean(req, "push") {
			return p.Send("Первая отправка", false)
		}
		return "ok", nil
	})
	jobAPI("/api/project/clone", "Скачивание проекта", func(req map[string]any, j *Job) (any, error) {
		dir, err := Clone(s.cfg, j, strings.TrimSpace(str(req, "url")), str(req, "parent"))
		if dir != "" && s.cfg.Project(dir) == nil {
			s.cfg.Projects = append(s.cfg.Projects, LocalProject{Path: dir, RelinkOnReceive: true})
			s.cfg.Save()
		}
		return map[string]string{"path": dir}, err
	})
	jobAPI("/api/project/receive", "Получение", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		p.Resolve = str(req, "resolve")
		return p.Receive()
	})
	jobAPI("/api/project/send", "Отправка", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		msg := strings.TrimSpace(str(req, "message"))
		if msg == "" {
			msg = defaultCommitMsg(s.cfg)
		}
		p.Resolve = str(req, "resolve")
		return p.Send(msg, boolean(req, "release"))
	})
	// после конфликта: оставить одну из двух версий файла (копия «(конфликт — …)» рядом с оригиналом)
	api("/api/project/conflict-keep", func(r *http.Request) (any, error) {
		var req struct{ Path, Copy, Keep string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := s.project(req.Path, nil)
		if err != nil {
			return nil, err
		}
		return "ok", p.KeepConflictVersion(req.Copy, req.Keep)
	})
	jobAPI("/api/project/lock", "Взять в работу", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		return "ok", p.TakeLock(boolean(req, "force"), str(req, "note"))
	})
	jobAPI("/api/project/unlock", "Освободить", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		return "ok", p.ReleaseLock()
	})
	jobAPI("/api/project/prepare", "Подготовка", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		_, err = p.Prepare()
		return "ok", err
	})
	jobAPI("/api/project/relink", "Перелинковка сэмплов", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		n, err := p.Relink()
		if n == 0 && err == nil {
			j.Logf("Все пути в порядке — менять нечего")
		}
		return n, err
	})
	jobAPI("/api/project/restore", "Восстановление версии", func(req map[string]any, j *Job) (any, error) {
		p, err := s.project(str(req, "path"), j)
		if err != nil {
			return nil, err
		}
		dst, err := p.RestoreVersion(str(req, "hash"), str(req, "file"))
		if err == nil {
			j.Logf("Сохранено как %s", dst)
		}
		return dst, err
	})
	jobAPI("/api/tools/install", "Установка компонентов", func(req map[string]any, j *Job) (any, error) {
		return "ok", InstallTools(j)
	})
	// jobs started in the background (automatic plugin transfers) — the UI shows them too
	api("/api/jobs", func(r *http.Request) (any, error) {
		s.jobsMu.Lock()
		defer s.jobsMu.Unlock()
		out := []map[string]any{}
		for _, j := range s.jobs {
			j.mu.Lock()
			if time.Since(time.Unix(j.Started, 0)) < 10*time.Minute {
				out = append(out, map[string]any{"id": j.ID, "title": j.Title, "done": j.Done, "started": j.Started})
			}
			j.mu.Unlock()
		}
		return out, nil
	})
	api("/api/job", func(r *http.Request) (any, error) {
		s.jobsMu.Lock()
		j := s.jobs[r.URL.Query().Get("id")]
		s.jobsMu.Unlock()
		if j == nil {
			return nil, errors.New("задача не найдена")
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		cp := Job{ID: j.ID, Title: j.Title, Project: j.Project, Done: j.Done, Err: j.Err, Details: j.Details, Res: j.Res, Fix: j.Fix,
			Steps: append([]string{}, j.Steps...), Progress: j.Progress, Started: j.Started, Cancellable: j.Cancellable && !j.Done, Cancelled: j.Cancelled}
		return &cp, nil
	})
	// «Отменить» у задачи (скачивание проекта)
	api("/api/job/cancel", func(r *http.Request) (any, error) {
		s.jobsMu.Lock()
		var b struct {
			ID string `json:"id"`
		}
		decode(r, &b)
		j := s.jobs[b.ID]
		s.jobsMu.Unlock()
		if j == nil {
			return nil, errors.New("задача не найдена")
		}
		j.mu.Lock()
		ok := j.Cancellable && !j.Done
		j.mu.Unlock()
		if !ok {
			return nil, errors.New("эту задачу уже нельзя отменить")
		}
		j.Cancel()
		return "ok", nil
	})
}

// Listen picks the preferred port, falling back to a random one.
func Listen(pref int) (net.Listener, int, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", pref))
	if err != nil {
		l, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, 0, err
		}
	}
	return l, l.Addr().(*net.TCPAddr).Port, nil
}
