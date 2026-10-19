package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"flsync/internal/flp"
)

const (
	ignoreBegin = "# >>> Программа Узбека: неиспользуемые аудио (генерируется автоматически, не редактируй)"
	ignoreEnd   = "# <<< Программа Узбека"
	audioDir    = "Audio"   // all audio of a project lives here
	samplesDir  = "Samples" // legacy folder of v0.1
)

var baseIgnore = []string{
	"# Программа Узбека — служебное",
	".flsync-local/",
	"Backup/",
	"*autosave*.flp",
	"*overwritten*.flp",
	"*.flsync-tmp",
	".DS_Store",
	"._*",
	"Thumbs.db",
	"desktop.ini",
}

// Project wraps one project folder.
type Project struct {
	Dir string
	Cfg *Config
	Job *Job
}

func (p *Project) git() *Git { return &Git{Dir: p.Dir, Cfg: p.Cfg, Job: p.Job} }

func skipDir(name string) bool {
	l := strings.ToLower(name)
	return l == ".git" || l == ".flsync-local" || l == "backup" || l == "node_modules"
}

// walk lists files relative to the project (slash separated).
func (p *Project) walk(fn func(rel, abs string, info fs.FileInfo)) {
	filepath.WalkDir(p.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != p.Dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(p.Dir, path)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fn(filepath.ToSlash(rel), path, info)
		return nil
	})
}

// FLPs returns project files (excluding autosaves / conflict copies are kept).
func (p *Project) FLPs() []string {
	var r []string
	p.walk(func(rel, abs string, _ fs.FileInfo) {
		l := strings.ToLower(rel)
		if strings.HasSuffix(l, ".flp") && !strings.Contains(l, "autosave") && !strings.Contains(l, "overwritten") {
			r = append(r, rel)
		}
	})
	sort.Strings(r)
	return r
}

// ---------- analysis ----------

type SampleRef struct {
	Name     string   `json:"name"`
	Original string   `json:"original,omitempty"`
	Status   string   `json:"status,omitempty"` // project, external, factory, missing
	Local    string   `json:"local,omitempty"`
	FLPs     []string `json:"flps"`
	Embedded bool     `json:"embedded"`
}

type AudioFile struct {
	Rel      string `json:"rel"`
	Size     int64  `json:"size"`
	Used     bool   `json:"used"`
	Tracked  bool   `json:"tracked"`
	Included bool   `json:"included"` // forced by alwaysInclude
	Ignored  bool   `json:"ignored"`  // will not be sent
}

type FLPInfo struct {
	Rel     string       `json:"rel"`
	Size    int64        `json:"size"`
	ModTime int64        `json:"modTime"`
	Error   string       `json:"error,omitempty"`
	Info    flp.Info     `json:"info"`
	Plugins []flp.Plugin `json:"plugins"`
}

type Analysis struct {
	FLPs    []FLPInfo      `json:"flps"`
	Samples []SampleRef    `json:"samples"`
	Audio   []AudioFile    `json:"audio"`
	Plugins []flp.Plugin   `json:"plugins"`
	Shared  SharedSettings `json:"shared"`
}

func flUserData() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Documents", "Image-Line", "FL Studio")
}

// resolve expands FL placeholders. factory=true for FL factory content.
func resolveFLPath(s string) (path string, factory bool) {
	if strings.HasPrefix(s, "%") {
		i := strings.Index(s[1:], "%")
		if i > 0 {
			key := strings.ToLower(s[1 : i+1])
			rest := strings.TrimLeft(s[i+2:], `/\`)
			rest = strings.ReplaceAll(rest, `\`, string(os.PathSeparator))
			if strings.Contains(key, "factory") {
				return s, true
			}
			if strings.Contains(key, "userdata") {
				return filepath.Join(flUserData(), rest), false
			}
		}
	}
	return s, false
}

func isInside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func (p *Project) Analyze() *Analysis {
	a := &Analysis{Shared: LoadShared(p.Dir), FLPs: []FLPInfo{}, Samples: []SampleRef{}, Audio: []AudioFile{}, Plugins: []flp.Plugin{}}
	byName := map[string]*SampleRef{}
	var order []string
	for _, rel := range p.FLPs() {
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		fi := FLPInfo{Rel: rel}
		if st, err := os.Stat(abs); err == nil {
			fi.Size, fi.ModTime = st.Size(), st.ModTime().Unix()
		}
		f, err := flp.ParseFile(abs)
		if err != nil {
			fi.Error = err.Error()
			a.FLPs = append(a.FLPs, fi)
			continue
		}
		fi.Info = f.Info()
		fi.Plugins = flp.UniquePlugins(fi.Info.Plugins)
		a.Plugins = append(a.Plugins, fi.Info.Plugins...)
		add := func(s string, emb bool) {
			k := strings.ToLower(flp.BaseName(s))
			if k == "" {
				return
			}
			r := byName[k]
			if r == nil {
				r = &SampleRef{Name: flp.BaseName(s), Original: s, Embedded: emb}
				byName[k] = r
				order = append(order, k)
			}
			if !emb {
				r.Embedded = false
				if r.Original != s && !strings.ContainsAny(r.Original, `/\`) {
					r.Original = s
				}
			}
			for _, x := range r.FLPs {
				if x == rel {
					return
				}
			}
			r.FLPs = append(r.FLPs, rel)
		}
		for _, s := range fi.Info.Samples {
			add(s, false)
		}
		for _, s := range fi.Info.Embedded {
			add(s, true)
		}
		a.FLPs = append(a.FLPs, fi)
	}
	a.Plugins = flp.UniquePlugins(a.Plugins)
	if a.Plugins == nil {
		a.Plugins = []flp.Plugin{}
	}

	tracked := map[string]bool{}
	g := p.git()
	if g.IsRepo() {
		tracked = g.Tracked()
	}
	// index project audio by name
	local := map[string]string{}
	p.walk(func(rel, abs string, info fs.FileInfo) {
		if !flp.IsAudio(rel) {
			return
		}
		k := strings.ToLower(flp.BaseName(rel))
		if prev, ok := local[k]; !ok || audioRank(rel) < audioRank(prev) {
			local[k] = rel
		}
		af := AudioFile{Rel: rel, Size: info.Size(), Tracked: tracked[rel]}
		_, af.Used = byName[k]
		af.Included = matchesInclude(rel, a.Shared.AlwaysInclude)
		af.Ignored = !af.Used && !af.Included && !af.Tracked && a.Shared.IgnoreUnused
		a.Audio = append(a.Audio, af)
	})
	sort.Slice(a.Audio, func(i, j int) bool { return a.Audio[i].Rel < a.Audio[j].Rel })

	for _, k := range order {
		r := byName[k]
		path, factory := resolveFLPath(r.Original)
		switch {
		case local[k] != "":
			r.Status, r.Local = "project", local[k]
		case factory:
			r.Status = "factory"
		case filepath.IsAbs(path) && exists(path):
			r.Status, r.Local = "external", path
		default:
			r.Status = "missing"
		}
		a.Samples = append(a.Samples, *r)
	}
	return a
}

func matchesInclude(rel string, pats []string) bool {
	for _, p := range pats {
		p = strings.Trim(filepath.ToSlash(p), " ")
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(strings.ToLower(rel), strings.ToLower(p)) {
				return true
			}
		} else if strings.EqualFold(rel, p) {
			return true
		} else if ok, _ := filepath.Match(strings.ToLower(p), strings.ToLower(rel)); ok {
			return true
		}
	}
	return false
}

// ---------- preparing the repo ----------

func ciPattern(ext string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(ext) {
		u := strings.ToUpper(string(r))
		if u != string(r) {
			b.WriteString("[" + string(r) + u + "]")
		} else {
			b.WriteRune(r)
		}
	}
	return "*." + b.String()
}

func (p *Project) writeAttributes(s SharedSettings) error {
	var b strings.Builder
	b.WriteString("# Программа Узбека: большие бинарные файлы → Git LFS, проекты .flp не мерджатся\n")
	b.WriteString(ciPattern("flp") + " binary\n")
	for _, e := range s.LFSExtensions {
		e = strings.TrimPrefix(strings.TrimSpace(e), ".")
		if e != "" {
			b.WriteString(ciPattern(e) + " filter=lfs diff=lfs merge=lfs -text\n")
		}
	}
	path := filepath.Join(p.Dir, ".gitattributes")
	old, _ := os.ReadFile(path)
	if string(old) == b.String() {
		return nil
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// updateIgnore rewrites .gitignore keeping user lines, base rules and the
// generated block of unused audio files.
func (p *Project) updateIgnore(a *Analysis) (int, error) {
	path := filepath.Join(p.Dir, ".gitignore")
	old, _ := os.ReadFile(path)
	var user []string
	in := false
	have := map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(string(old), "\r\n", "\n"), "\n") {
		switch {
		case l == ignoreBegin:
			in = true
		case l == ignoreEnd:
			in = false
		case !in:
			user = append(user, l)
			have[strings.TrimSpace(l)] = true
		}
	}
	for len(user) > 0 && strings.TrimSpace(user[len(user)-1]) == "" {
		user = user[:len(user)-1]
	}
	for _, l := range baseIgnore {
		if !have[l] {
			user = append(user, l)
		}
	}
	out := strings.Join(user, "\n") + "\n"
	if string(old) != out {
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			return 0, err
		}
	}
	// Unused audio is machine-specific (each of you has different leftovers),
	// so it goes into .git/info/exclude, which is local and never shared.
	var block []string
	for _, f := range a.Audio {
		if f.Ignored {
			block = append(block, "/"+escapeIgnore(f.Rel))
		}
	}
	gitDir := filepath.Join(p.Dir, ".git")
	if !exists(gitDir) {
		return len(block), nil
	}
	ex := filepath.Join(gitDir, "info", "exclude")
	os.MkdirAll(filepath.Dir(ex), 0o755)
	oldEx, _ := os.ReadFile(ex)
	var keep []string
	in = false
	for _, l := range strings.Split(strings.ReplaceAll(string(oldEx), "\r\n", "\n"), "\n") {
		switch {
		case l == ignoreBegin:
			in = true
		case l == ignoreEnd:
			in = false
		case !in:
			keep = append(keep, l)
		}
	}
	for len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
		keep = keep[:len(keep)-1]
	}
	exOut := strings.Join(keep, "\n") + "\n"
	if len(block) > 0 {
		exOut += "\n" + ignoreBegin + "\n" + strings.Join(block, "\n") + "\n" + ignoreEnd + "\n"
	}
	if string(oldEx) != exOut {
		return len(block), os.WriteFile(ex, []byte(exOut), 0o644)
	}
	return len(block), nil
}

func escapeIgnore(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "!", `\!`, "#", `\#`)
	s = r.Replace(s)
	if strings.HasSuffix(s, " ") {
		s = strings.TrimRight(s, " ") + `\ `
	}
	return s
}

// audioRank: which copy of a same-named file wins (Audio/ first).
func audioRank(rel string) int {
	switch {
	case strings.HasPrefix(rel, audioDir+"/"):
		return 0
	case strings.HasPrefix(rel, samplesDir+"/"):
		return 1
	}
	return 2
}

// CollectSamples copies samples that live outside the project into Audio/.
func (p *Project) CollectSamples(a *Analysis) ([]string, error) {
	var copied []string
	for i, s := range a.Samples {
		if s.Status != "external" {
			continue
		}
		dstDir := filepath.Join(p.Dir, audioDir)
		os.MkdirAll(dstDir, 0o755)
		name := flp.BaseName(s.Local)
		dst := filepath.Join(dstDir, name)
		if exists(dst) {
			continue
		}
		if err := copyFile(s.Local, dst); err != nil {
			return copied, fmt.Errorf("копирование %s: %w", s.Local, err)
		}
		rel := audioDir + "/" + name
		copied = append(copied, rel)
		a.Samples[i].Status, a.Samples[i].Local = "project", rel
		st, _ := os.Stat(dst)
		af := AudioFile{Rel: rel, Used: true}
		if st != nil {
			af.Size = st.Size()
		}
		a.Audio = append(a.Audio, af)
	}
	return copied, nil
}

// Relink rewrites sample paths inside .flp files so that samples which are
// missing at their original location point to the copy inside the project.
// The original .flp is backed up to .flsync-local/backup.
func (p *Project) Relink() (int, error) { return p.relink(false) }

// relink; own=true also redirects paths that exist but lie outside this
// project when the project has its own copy (used after splitting).
func (p *Project) relink(own bool) (int, error) {
	local, localRel := map[string]string{}, map[string]string{}
	p.walk(func(rel, abs string, _ fs.FileInfo) {
		if flp.IsAudio(rel) {
			k := strings.ToLower(flp.BaseName(rel))
			if prev, ok := localRel[k]; !ok || audioRank(rel) < audioRank(prev) {
				local[k], localRel[k] = abs, rel
			}
		}
	})
	total := 0
	for _, rel := range p.FLPs() {
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		raw, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		f, err := flp.Parse(raw)
		if err != nil {
			continue
		}
		n := f.RewriteSamplePaths(func(old string) string {
			path, factory := resolveFLPath(old)
			if factory {
				return ""
			}
			if filepath.IsAbs(path) && exists(path) && (!own || isInside(p.Dir, path)) {
				return ""
			}
			if l, ok := local[strings.ToLower(flp.BaseName(old))]; ok {
				return l
			}
			return ""
		})
		if n == 0 {
			continue
		}
		bdir := filepath.Join(p.Dir, ".flsync-local", "backup")
		os.MkdirAll(bdir, 0o755)
		os.WriteFile(filepath.Join(bdir, strings.ReplaceAll(rel, "/", "_")+"."+time.Now().Format("20060102-150405")+".flp"), raw, 0o644)
		if err := os.WriteFile(abs, f.Bytes(), 0o644); err != nil {
			return total, err
		}
		p.Job.Logf("В %s пути к сэмплам направлены в папку проекта (%d)", flp.BaseName(rel), n)
		total += n
	}
	return total, nil
}

// Prepare: collect samples, refresh .gitignore/.gitattributes and the
// plugin manifest. Called before every commit.
func (p *Project) Prepare() (*Analysis, error) {
	s := LoadShared(p.Dir)
	if !exists(filepath.Join(p.Dir, ".flsync", "settings.json")) {
		writeJSON(filepath.Join(p.Dir, ".flsync", "settings.json"), s)
	}
	if err := p.writeAttributes(s); err != nil {
		return nil, err
	}
	a := p.Analyze()
	if s.CollectSamples {
		if c, err := p.CollectSamples(a); err != nil {
			return a, err
		} else if len(c) > 0 {
			p.Job.Logf("Сэмплы, лежавшие вне проекта, скопированы в %s/: %d", audioDir, len(c))
		}
	}
	if s.OrganizeAudio {
		moved, err := p.OrganizeAudio()
		if err != nil {
			return a, err
		}
		if len(moved) > 0 {
			p.Job.Logf("Аудио из корня проекта перенесено в %s/: %d", audioDir, len(moved))
			p.Relink()
			a = p.Analyze()
		}
	}
	n, err := p.updateIgnore(a)
	if err != nil {
		return a, err
	}
	if n > 0 {
		p.Job.Logf("Не отправляю неиспользуемое аудио: %d", n)
	}
	if err := p.WriteManifest(a); err != nil {
		return a, err
	}
	return a, nil
}

// ---------- init / clone ----------

func (p *Project) Init(remote string) error {
	g := p.git()
	if !g.IsRepo() {
		if _, err := g.Run("init"); err != nil {
			return err
		}
		g.RunQuiet("symbolic-ref", "HEAD", "refs/heads/main")
	}
	if _, err := g.Run("lfs", "install", "--local", "--skip-repo"); err != nil {
		return err
	}
	g.RunQuiet("config", "uzbek.adopted", "true")
	if remote != "" {
		if g.Remote() == "" {
			_, err := g.Run("remote", "add", "origin", remote)
			if err != nil {
				return err
			}
		} else if _, err := g.Run("remote", "set-url", "origin", remote); err != nil {
			return err
		}
	}
	_, err := p.Prepare()
	return err
}

func Clone(cfg *Config, job *Job, url, parent string) (string, error) {
	name := strings.TrimSuffix(flp.BaseName(strings.TrimSuffix(url, "/")), ".git")
	if name == "" {
		return "", errors.New("не понял имя репозитория из ссылки")
	}
	dir := filepath.Join(parent, name)
	if exists(dir) {
		ents, _ := os.ReadDir(dir)
		if len(ents) > 0 {
			return "", fmt.Errorf("папка %s уже существует и не пустая", dir)
		}
	}
	g := &Git{Dir: parent, Cfg: cfg, Job: job}
	os.Setenv("GIT_LFS_SKIP_SMUDGE", "1")
	job.Logf("Скачиваю проект…")
	_, err := g.Run("clone", "--progress", url, dir)
	os.Unsetenv("GIT_LFS_SKIP_SMUDGE")
	if err != nil {
		return "", err
	}
	g.Dir = dir
	if _, err := g.Run("lfs", "install", "--local", "--skip-repo"); err != nil {
		return dir, err
	}
	g.RunQuiet("config", "uzbek.adopted", "true")
	job.Logf("Проект скачан, докачиваю аудио…")
	if _, err := g.Run("lfs", "pull"); err != nil {
		return dir, err
	}
	p := &Project{Dir: dir, Cfg: cfg, Job: job}
	if n, _ := p.Relink(); n > 0 {
		job.Logf("Пути к сэмплам направлены в папку проекта: %d", n)
	}
	return dir, nil
}

// ---------- sync ----------

func isGenerated(path string) bool {
	return path == "PLUGINS.txt" || path == ".flsync/project.json" || path == ".flsync/lock.json" ||
		strings.HasPrefix(path, ".flsync/machines/") || path == ".gitignore" || path == ".gitattributes"
}

func conflictName(path, who string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	return fmt.Sprintf("%s (конфликт — %s %s)%s", base, who, nowStamp(), ext)
}

// resolveConflicts never loses work: the server version keeps the original
// name, the local version is saved next to it with "(конфликт — ...)".
func (p *Project) resolveConflicts() ([]string, error) {
	g := p.git()
	var copies []string
	for _, path := range g.Conflicts() {
		stages, _ := g.RunQuiet("ls-files", "-u", "--", path)
		hasOurs := strings.Contains(stages, " 2\t")
		hasTheirs := strings.Contains(stages, " 3\t")
		abs := filepath.Join(p.Dir, filepath.FromSlash(path))
		switch {
		case isGenerated(path):
			if hasTheirs {
				g.Run("checkout", "--theirs", "--", path)
				g.Run("add", "--", path)
			} else {
				g.Run("rm", "-q", "--cached", "--", path)
			}
		case hasOurs && hasTheirs:
			if _, err := g.Run("checkout", "--ours", "--", path); err != nil {
				return copies, err
			}
			cp := conflictName(path, p.Cfg.MachineID())
			if err := copyFile(abs, filepath.Join(p.Dir, filepath.FromSlash(cp))); err != nil {
				return copies, err
			}
			if _, err := g.Run("checkout", "--theirs", "--", path); err != nil {
				return copies, err
			}
			g.Run("add", "--", path, cp)
			copies = append(copies, cp)
			p.Job.Logf("⚠ Конфликт в %s: версия друга сохранена под исходным именем, твоя — как «%s»", path, cp)
		case hasOurs:
			g.Run("checkout", "--ours", "--", path)
			g.Run("add", "--", path)
		case hasTheirs:
			g.Run("checkout", "--theirs", "--", path)
			g.Run("add", "--", path)
		}
	}
	if rest := g.Conflicts(); len(rest) > 0 {
		return copies, fmt.Errorf("не удалось разрешить конфликты: %s", strings.Join(rest, ", "))
	}
	_, err := g.Run("commit", "--no-edit", "--no-verify")
	return copies, err
}

// commitAll stages everything and commits if there are changes.
func (p *Project) commitAll(msg string) (bool, error) {
	g := p.git()
	// re-evaluate unused files right before staging (a merge may have changed things)
	if _, err := p.updateIgnore(p.Analyze()); err != nil {
		return false, err
	}
	if _, err := g.Run("add", "-A"); err != nil {
		return false, err
	}
	if _, err := g.RunQuiet("diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	_, err := g.Run("commit", "--no-verify", "-q", "-m", msg)
	return err == nil, err
}

// fetchMerge downloads remote changes and merges them in.
func (p *Project) fetchMerge() (conflicts []string, err error) {
	g := p.git()
	if g.Remote() == "" {
		return nil, errors.New("не указан адрес репозитория (remote). Укажи его в настройках проекта")
	}
	if _, err := g.Run("fetch", "--progress", "origin"); err != nil {
		return nil, err
	}
	b := g.Branch()
	if !g.RemoteBranchExists(b) {
		p.Job.Logf("На сервере ещё нет ветки %s — получать нечего", b)
		return nil, nil
	}
	if !g.HasCommits() {
		_, err := g.Run("reset", "--mixed", "origin/"+b)
		if err == nil {
			_, err = g.Run("checkout", "--", ".")
		}
		return nil, err
	}
	if _, err := g.Run("merge", "--no-edit", "--no-verify", "origin/"+b); err != nil {
		if len(g.Conflicts()) == 0 {
			return nil, err
		}
		return p.resolveConflicts()
	}
	return nil, nil
}

type SyncResult struct {
	Conflicts []string `json:"conflicts"`
	Relinked  int      `json:"relinked"`
	Committed bool     `json:"committed"`
	Pushed    bool     `json:"pushed"`
}

func (p *Project) Receive() (*SyncResult, error) {
	res := &SyncResult{}
	if _, err := p.Prepare(); err != nil {
		return res, err
	}
	if c, err := p.commitAll(fmt.Sprintf("Локальные изменения %s (перед получением)", p.Cfg.MachineID())); err != nil {
		return res, err
	} else if c {
		p.Job.Logf("Твои несохранённые изменения закоммичены локально (на сервер не отправлены)")
	}
	conf, err := p.fetchMerge()
	res.Conflicts = conf
	if err != nil {
		return res, err
	}
	g := p.git()
	if g.RemoteBranchExists(g.Branch()) {
		if _, err := g.Run("lfs", "pull"); err != nil {
			return res, err
		}
	}
	if lp := p.Cfg.Project(p.Dir); lp == nil || lp.RelinkOnReceive {
		n, err := p.Relink()
		res.Relinked = n
		if err != nil {
			return res, err
		}
	}
	p.Job.Logf("✓ Получено")
	return res, nil
}

func (p *Project) push() error {
	g := p.git()
	b := g.Branch()
	if _, err := g.Run("lfs", "push", "origin", b); err != nil {
		return err
	}
	_, err := g.Run("push", "--no-verify", "--progress", "-u", "origin", "HEAD:"+b)
	return err
}

func (p *Project) Send(msg string, release bool) (*SyncResult, error) {
	res := &SyncResult{}
	if _, err := p.Prepare(); err != nil {
		return res, err
	}
	if release {
		if l := p.ReadLock(); l != nil && l.User == p.Cfg.MachineID() {
			os.Remove(filepath.Join(p.Dir, ".flsync", "lock.json"))
			msg += " · 🔓 проект свободен"
		}
	}
	c, err := p.commitAll(msg)
	if err != nil {
		return res, err
	}
	res.Committed = c
	for attempt := 0; attempt < 3; attempt++ {
		conf, err := p.fetchMerge()
		res.Conflicts = append(res.Conflicts, conf...)
		if err != nil {
			return res, err
		}
		if len(conf) > 0 {
			// regenerate manifests after merge
			p.Prepare()
			p.commitAll("Обновление манифеста после слияния")
		}
		err = p.push()
		if err == nil {
			res.Pushed = true
			p.Job.Logf("✓ Отправлено")
			return res, nil
		}
		if !isPushRejected(err) {
			return res, err
		}
		p.Job.Logf("Пока ты собирал коммит, друг успел что-то отправить — сливаю и пробую снова…")
	}
	return res, errors.New("не получилось отправить после 3 попыток")
}

// ---------- lock ----------

type Lock struct {
	User  string `json:"user"`
	OS    string `json:"os"`
	Since int64  `json:"since"`
	Note  string `json:"note,omitempty"`
}

func (p *Project) ReadLock() *Lock {
	var l Lock
	if err := readJSON(filepath.Join(p.Dir, ".flsync", "lock.json"), &l); err != nil || l.User == "" {
		return nil
	}
	return &l
}

func (p *Project) TakeLock(force bool, note string) error {
	me := p.Cfg.MachineID()
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := p.Receive(); err != nil {
			return err
		}
		if l := p.ReadLock(); l != nil && l.User != me && !force {
			return fmt.Errorf("проект сейчас в работе у %s (с %s). Попроси его нажать «Отправить и освободить» или забери принудительно",
				l.User, time.Unix(l.Since, 0).Format("02.01 15:04"))
		}
		writeJSON(filepath.Join(p.Dir, ".flsync", "lock.json"), Lock{User: me, OS: OSName(), Since: time.Now().Unix(), Note: note})
		if _, err := p.commitAll("🔒 " + me + " взял проект в работу"); err != nil {
			return err
		}
		err := p.push()
		if err == nil {
			p.Job.Logf("✓ Проект закреплён за тобой")
			return nil
		}
		if !isPushRejected(err) {
			return err
		}
	}
	return errors.New("не удалось взять проект: сервер всё время меняется")
}

func (p *Project) ReleaseLock() error {
	l := p.ReadLock()
	if l == nil {
		return nil
	}
	os.Remove(filepath.Join(p.Dir, ".flsync", "lock.json"))
	if _, err := p.commitAll("🔓 " + p.Cfg.MachineID() + " освободил проект"); err != nil {
		return err
	}
	if _, err := p.fetchMerge(); err != nil {
		return err
	}
	return p.push()
}

// RestoreVersion saves an old version of a file next to the current one.
func (p *Project) RestoreVersion(hash, rel string) (string, error) {
	g := p.git()
	short := hash
	if len(short) > 7 {
		short = short[:7]
	}
	when, _ := g.RunQuiet("log", "-1", "--format=%ad", "--date=format:%Y-%m-%d %H-%M", hash)
	ext := filepath.Ext(rel)
	dst := fmt.Sprintf("%s (версия %s %s)%s", strings.TrimSuffix(rel, ext), when, short, ext)
	if err := g.ShowToFile(hash, rel, filepath.Join(p.Dir, filepath.FromSlash(dst))); err != nil {
		return "", err
	}
	return dst, nil
}

func defaultCommitMsg(cfg *Config) string {
	return fmt.Sprintf("%s: правки %s", cfg.MachineID(), time.Now().Format("02.01 15:04"))
}

var _ = runtime.GOOS
