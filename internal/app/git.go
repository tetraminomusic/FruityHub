package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Job is a long-running operation shown to the user as simple steps and a
// progress bar — never as raw git output (that is kept only for "details").
type Job struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Project  string    `json:"project,omitempty"`
	Done     bool      `json:"done"`
	Err      string    `json:"error,omitempty"`
	Details  string    `json:"details,omitempty"` // technical text, hidden by default
	Steps    []string  `json:"steps"`
	Progress *Progress `json:"progress,omitempty"`
	Res      any       `json:"result,omitempty"`
	Started  int64     `json:"started"`
	raw      []string
	mu       sync.Mutex
}

type Progress struct {
	Label string `json:"label"`
	Pct   int    `json:"pct"` // -1 = unknown
}

// Logf adds a human-readable step.
func (j *Job) Logf(format string, a ...any) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Steps = append(j.Steps, fmt.Sprintf(format, a...))
	if len(j.Steps) > 300 {
		j.Steps = j.Steps[len(j.Steps)-300:]
	}
	j.Progress = nil
}

// SetProgress shows a progress bar (pct -1 = indeterminate).
func (j *Job) SetProgress(label string, pct int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.Progress = &Progress{Label: label, Pct: pct}
	j.mu.Unlock()
}

func (j *Job) addRaw(line string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.raw = append(j.raw, line)
	if len(j.raw) > 200 {
		j.raw = j.raw[len(j.raw)-200:]
	}
	j.mu.Unlock()
}

// legacy helper used by downloads: plain text progress
func (j *Job) progress(line string) { j.SetProgress(line, -1) }

var pctRe = regexp.MustCompile(`(\d{1,3})%`)

// gitProgressLabels turns git's own progress lines into words a musician understands.
var gitProgressLabels = []struct{ prefix, label string }{
	{"Uploading LFS objects", "Загрузка аудио на сервер"},
	{"Downloading LFS objects", "Скачивание аудио"},
	{"Filtering content", "Скачивание аудио"},
	{"Receiving objects", "Скачивание изменений"},
	{"Writing objects", "Отправка изменений"},
	{"Updating files", "Обновление файлов"},
	{"Checking out files", "Обновление файлов"},
	{"Resolving deltas", "Подготовка"},
	{"Compressing objects", "Подготовка"},
	{"Counting objects", "Подготовка"},
	{"Enumerating objects", "Подготовка"},
}

func (j *Job) gitLine(line string) {
	j.addRaw(line)
	if strings.HasPrefix(line, "remote: ") {
		line = strings.TrimPrefix(line, "remote: ")
	}
	for _, g := range gitProgressLabels {
		if strings.HasPrefix(line, g.prefix) {
			pct := -1
			if m := pctRe.FindStringSubmatch(line); m != nil {
				fmt.Sscanf(m[1], "%d", &pct)
			}
			label := g.label
			if i := strings.Index(line, "|"); i > 0 && strings.Contains(line[i:], "/s") {
				label += " · " + strings.TrimSpace(line[i+1:]) // transfer speed
			}
			j.SetProgress(label, pct)
			return
		}
	}
}

type jobWriter struct {
	j   *Job
	buf []byte
	all *bytes.Buffer
}

func (w *jobWriter) Write(p []byte) (int, error) {
	w.all.Write(p)
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
		if line != "" {
			w.j.gitLine(line)
		}
	}
	return len(p), nil
}

// Git runs git commands for one repo with the app's environment.
type Git struct {
	Dir string
	Cfg *Config
	Job *Job
	Env []string // extra environment (e.g. GIT_INDEX_FILE)
	URL string   // remote URL when there is no repo yet (clone)
}

func selfExe() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

func gitEnv(cfg *Config) []string { return gitEnvToken(cfg, "") }

func gitEnvToken(cfg *Config, token string) []string {
	env := os.Environ()
	path := os.Getenv("PATH")
	var extra []string
	if d := toolsBinDirs(); len(d) > 0 {
		extra = append(extra, d...)
	}
	if len(extra) > 0 {
		path = strings.Join(extra, string(os.PathListSeparator)) + string(os.PathListSeparator) + path
	}
	env = setEnv(env, "PATH", path)
	env = setEnv(env, "GIT_TERMINAL_PROMPT", "0")
	if token != "" {
		env = setEnv(env, "GIT_ASKPASS", selfExe())
		env = setEnv(env, "FLSYNC_ASKPASS", "1")
		env = setEnv(env, "FLSYNC_TOKEN", token)
	}
	return env
}

// token for this repo's remote (server projects get a short-lived key).
func (g *Git) token() string {
	if g.Cfg == nil {
		return ""
	}
	remote := g.URL
	if remote == "" {
		key := filepath.Clean(g.Dir)
		if e, ok := remoteCache.Load(key); ok && time.Since(e.(remoteEntry).at) < 30*time.Second {
			remote = e.(remoteEntry).url
		} else {
			cmd := exec.Command(gitBinary(), "config", "--get", "remote.origin.url")
			cmd.Dir = g.Dir
			cmd.Env = gitEnv(nil)
			hideWindow(cmd)
			out, _ := cmd.Output()
			remote = strings.TrimSpace(string(out))
			remoteCache.Store(key, remoteEntry{remote, time.Now()})
		}
	}
	return repoToken(g.Cfg, remote)
}

func setEnv(env []string, k, v string) []string {
	pref := k + "="
	out := env[:0]
	for _, e := range env {
		if runtime.GOOS == "windows" {
			if strings.HasPrefix(strings.ToUpper(e), strings.ToUpper(pref)) {
				continue
			}
		} else if strings.HasPrefix(e, pref) {
			continue
		}
		out = append(out, e)
	}
	if v == "" {
		return out
	}
	return append(out, pref+v)
}

func (g *Git) baseArgs() []string {
	a := []string{"-c", "core.quotepath=false", "-c", "core.autocrlf=false", "-c", "color.ui=false"}
	if g.Cfg != nil {
		name := strings.TrimSpace(g.Cfg.UserName)
		if name == "" {
			name = g.Cfg.MachineID()
		}
		a = append(a, "-c", "user.name="+name, "-c", "user.email="+strings.ReplaceAll(strings.ToLower(g.Cfg.MachineID()), " ", ".")+"@flsync.local")
	}
	return a
}

// needsAuth: commands that may talk to the server (LFS downloads happen on
// checkout/merge/reset too).
func needsAuth(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		switch a {
		case "fetch", "push", "pull", "clone", "ls-remote", "lfs", "checkout", "merge", "reset", "switch", "restore", "cherry-pick", "rebase", "stash", "show", "cat-file":
			return true
		}
		return false
	}
	return false
}

// Run executes git and returns trimmed stdout.
func (g *Git) Run(args ...string) (string, error) {
	return g.run(nil, true, args...)
}

// RunQuiet executes git without echoing the command into the job log.
func (g *Git) RunQuiet(args ...string) (string, error) {
	return g.run(nil, false, args...)
}

func (g *Git) run(stdin io.Reader, echo bool, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "remote" {
		defer forgetRemote(g.Dir)
	}
	full := g.baseArgs()
	tok := ""
	if needsAuth(args) {
		tok = g.token()
	}
	if tok != "" {
		full = append(full, "-c", "credential.helper=")
	}
	full = append(full, args...)
	cmd := exec.Command(gitBinary(), full...)
	cmd.Dir = g.Dir
	cmd.Env = append(gitEnvToken(g.Cfg, tok), g.Env...)
	cmd.Stdin = stdin
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	if g.Job != nil && echo {
		g.Job.addRaw("$ git " + strings.Join(args, " "))
		cmd.Stderr = &jobWriter{j: g.Job, all: &errb}
	} else {
		cmd.Stderr = &errb
	}
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return strings.TrimSpace(out.String()), &GitError{Args: args, Msg: msg, Err: err}
	}
	return strings.TrimRight(out.String(), "\r\n"), nil
}

type GitError struct {
	Args []string
	Msg  string
	Err  error
}

// Error is the human-readable message; Details() has the technical one.
func (e *GitError) Error() string { return humanGitError(e.Msg) }

func (e *GitError) Details() string {
	return "git " + strings.Join(e.Args, " ") + "\n" + e.Msg
}

func humanGitError(msg string) string {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "authentication failed"), strings.Contains(l, "could not read username"),
		strings.Contains(l, "terminal prompts disabled"), strings.Contains(l, "invalid username or password"):
		return "Нет доступа к репозиторию. Проверь GitHub-токен в настройках и что тебя добавили в проект."
	case strings.Contains(l, "repository not found"):
		return "Репозиторий не найден или у тебя нет к нему доступа."
	case strings.Contains(l, "could not resolve host"), strings.Contains(l, "failed to connect"), strings.Contains(l, "timed out"),
		strings.Contains(l, "connection reset"), strings.Contains(l, "unable to access"):
		return "Нет связи с сервером. Проверь интернет или VPN и попробуй ещё раз."
	case strings.Contains(l, "over its data quota"), strings.Contains(l, "exceeded"):
		return "Закончилась бесплатная квота GitHub на аудио (10 ГБ в месяц). Нужен свой сервер или оплата."
	case strings.Contains(l, "is not a git command") && strings.Contains(l, "lfs"):
		return "Не установлен git-lfs. Открой «Настройки» → «Установить автоматически»."
	case strings.Contains(l, "would be overwritten"):
		return "Твои несохранённые изменения мешают получить версию друга. Сначала нажми «Отправить» (или закрой проект в FL)."
	case strings.Contains(l, "no space left"):
		return "На диске закончилось место."
	case strings.Contains(l, "permission denied"), strings.Contains(l, "operation not permitted"):
		return "Нет прав на запись в папку проекта (возможно, файл открыт в FL Studio)."
	case strings.Contains(l, "index.lock"):
		return "Проект занят другой операцией. Подожди пару секунд и попробуй ещё раз."
	}
	first := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(msg, "fatal: "), "error: "), "\n", 2)[0])
	if len([]rune(first)) > 160 {
		first = string([]rune(first)[:160]) + "…"
	}
	if first == "" {
		first = "неизвестная ошибка"
	}
	return "Git не смог выполнить операцию: " + first
}

func (g *Git) IsRepo() bool {
	_, err := os.Stat(filepath.Join(g.Dir, ".git"))
	return err == nil
}

func (g *Git) Branch() string {
	b, err := g.RunQuiet("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || b == "HEAD" {
		b, _ = g.RunQuiet("symbolic-ref", "--short", "HEAD")
	}
	if b == "" {
		b = "main"
	}
	return b
}

func (g *Git) HasCommits() bool {
	_, err := g.RunQuiet("rev-parse", "--verify", "HEAD")
	return err == nil
}

func (g *Git) Remote() string {
	r, _ := g.RunQuiet("remote", "get-url", "origin")
	return r
}

// StatusEntry is one changed file.
type StatusEntry struct {
	Path   string `json:"path"`
	Code   string `json:"code"`
	Kind   string `json:"kind"` // added, modified, deleted, untracked, conflict
	Size   int64  `json:"size"`
	IsFLP  bool   `json:"isFlp"`
	Hidden bool   `json:"hidden"` // service file
}

func (g *Git) Status() ([]StatusEntry, error) {
	out, err := g.RunQuiet("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var res []StatusEntry
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if len(p) < 4 {
			continue
		}
		code, path := p[:2], p[3:]
		if code[0] == 'R' || code[0] == 'C' {
			i++ // skip original name
		}
		e := StatusEntry{Path: path, Code: code}
		switch {
		case code == "??":
			e.Kind = "untracked"
		case strings.Contains(code, "U") || code == "AA" || code == "DD":
			e.Kind = "conflict"
		case strings.Contains(code, "D"):
			e.Kind = "deleted"
		case strings.Contains(code, "A"):
			e.Kind = "added"
		default:
			e.Kind = "modified"
		}
		if st, err := os.Stat(filepath.Join(g.Dir, path)); err == nil {
			e.Size = st.Size()
		}
		e.IsFLP = strings.EqualFold(filepath.Ext(path), ".flp")
		e.Hidden = strings.HasPrefix(path, ".flsync/") || path == ".gitignore" || path == ".gitattributes" || path == "PLUGINS.txt"
		res = append(res, e)
	}
	return res, nil
}

// Tracked returns the set of tracked paths (slash separated).
func (g *Git) Tracked() map[string]bool {
	m := map[string]bool{}
	out, err := g.RunQuiet("ls-files", "-z")
	if err != nil {
		return m
	}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			m[p] = true
		}
	}
	return m
}

func (g *Git) RemoteBranchExists(branch string) bool {
	_, err := g.RunQuiet("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

// AheadBehind compares local branch with origin (after the last fetch).
func (g *Git) AheadBehind() (ahead, behind int) {
	b := g.Branch()
	if !g.RemoteBranchExists(b) {
		return -1, 0
	}
	out, err := g.RunQuiet("rev-list", "--left-right", "--count", "HEAD...origin/"+b)
	if err != nil {
		return
	}
	fmt.Sscanf(out, "%d %d", &ahead, &behind)
	return
}

// Commit info for history.
type Commit struct {
	Hash    string   `json:"hash"`
	Short   string   `json:"short"`
	Author  string   `json:"author"`
	Time    int64    `json:"time"`
	Subject string   `json:"subject"`
	Files   []string `json:"files"`
}

func (g *Git) Log(n int) []Commit {
	if !g.HasCommits() {
		return nil
	}
	out, err := g.RunQuiet("log", fmt.Sprintf("-n%d", n), "--name-only", "--format=\x1e%H\x1f%h\x1f%an\x1f%at\x1f%s")
	if err != nil {
		return nil
	}
	var res []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		lines := strings.Split(rec, "\n")
		f := strings.Split(lines[0], "\x1f")
		if len(f) < 5 {
			continue
		}
		c := Commit{Hash: f[0], Short: f[1], Author: f[2], Subject: f[4]}
		fmt.Sscanf(f[3], "%d", &c.Time)
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				c.Files = append(c.Files, l)
			}
		}
		res = append(res, c)
	}
	return res
}

// ShowToFile writes the content of path at revision rev into dst
// (passing through git-lfs smudge when the blob is an LFS pointer).
func (g *Git) ShowToFile(rev, path, dst string) error {
	out, err := g.runBytes("show", rev+":"+path)
	if err != nil {
		return err
	}
	if bytes.HasPrefix(out, []byte("version https://git-lfs.github.com/spec/")) {
		cmd := exec.Command(gitBinary(), append(g.baseArgs(), "lfs", "smudge", "--", path)...)
		cmd.Dir = g.Dir
		cmd.Env = gitEnvToken(g.Cfg, g.token())
		cmd.Stdin = bytes.NewReader(out)
		hideWindow(cmd)
		sm, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("lfs smudge: %w", err)
		}
		out = sm
	}
	return os.WriteFile(dst, out, 0o644)
}

// RunInput executes git with stdin and returns raw stdout.
func (g *Git) RunInput(stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command(gitBinary(), append(g.baseArgs(), args...)...)
	cmd.Dir = g.Dir
	cmd.Env = append(gitEnv(g.Cfg), g.Env...)
	cmd.Stdin = bytes.NewReader(stdin)
	hideWindow(cmd)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return out, &GitError{Args: args, Msg: errb.String(), Err: err}
	}
	return out, nil
}

func (g *Git) runBytes(args ...string) ([]byte, error) {
	cmd := exec.Command(gitBinary(), append(g.baseArgs(), args...)...)
	cmd.Dir = g.Dir
	cmd.Env = append(gitEnv(g.Cfg), g.Env...)
	hideWindow(cmd)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return nil, &GitError{Args: args, Msg: errb.String(), Err: err}
	}
	return out, nil
}

// Conflicts lists unmerged paths.
func (g *Git) Conflicts() []string {
	out, _ := g.RunQuiet("diff", "--name-only", "--diff-filter=U", "-z")
	var r []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			r = append(r, p)
		}
	}
	return r
}

func isPushRejected(err error) bool {
	var ge *GitError
	if errors.As(err, &ge) {
		l := strings.ToLower(ge.Msg)
		return strings.Contains(l, "rejected") || strings.Contains(l, "fetch first") || strings.Contains(l, "non-fast-forward")
	}
	return false
}

func nowStamp() string {
	return time.Now().Format("2006-01-02 15-04")
}
