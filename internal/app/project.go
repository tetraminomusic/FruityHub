package app

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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
	// Resolve — что делать, если проект здесь и на GitHub начаты отдельно (несвязанные истории):
	// "" — спросить; "theirs" — взять версию с GitHub; "mine" — оставить свою; "both" — объединить.
	Resolve string
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
		if strings.HasSuffix(l, ".flp") && !strings.Contains(l, "autosave") && !strings.Contains(l, "overwritten") && !strings.HasPrefix(l, ".flsync/") {
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
	return filepath.Join(documentsDir(), "Image-Line", "FL Studio")
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
		info, err := parseFLPCached(abs) // кэш: .flp разбирается заново, только если изменился
		if err != nil {
			fi.Error = err.Error()
			a.FLPs = append(a.FLPs, fi)
			continue
		}
		fi.Info = info
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
		case r.Embedded && findSampleByName(r.Name) != "": // плагин хранит только имя — сэмпл лежит в его библиотеке
			r.Status, r.Local = "external", findSampleByName(r.Name)
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
	// список проекта + общий список из Настроек → GitHub (новое расширение там сразу действует во всех проектах)
	exts := append([]string{}, s.LFSExtensions...)
	if g := globalLFS.Load(); g != nil {
		exts = append(exts, *g...)
	}
	seenExt := map[string]bool{}
	for _, e := range exts {
		e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
		if e != "" && !seenExt[e] {
			seenExt[e] = true
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
	// папка Backup (автосохранения FL Studio): не отправляем, если в общих правилах не включено обратное
	isBackupLine := func(l string) bool {
		l = strings.ToLower(strings.TrimSpace(l))
		return l == "backup/" || l == "/backup/" || l == "/[bb]ackup/" || l == "[bb]ackup/"
	}
	if a.Shared.SendBackup {
		kept := user[:0]
		for _, l := range user {
			if !isBackupLine(l) {
				kept = append(kept, l)
			}
		}
		user = kept
	} else {
		hasB := false
		for _, l := range user {
			if isBackupLine(l) {
				hasB = true
			}
		}
		if !hasB {
			user = append(user, "/[Bb]ackup/")
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
	if p.Cfg == nil || !p.Cfg.Meta.NoReadme {
		p.WriteReadme(a)
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
	// адрес репозитория — до всего остального: если дальше что-то упадёт, повтор продолжит с этим же репозиторием
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
	if _, err := g.Run("lfs", "install", "--local", "--skip-repo"); err != nil {
		return err
	}
	g.RunQuiet("config", "uzbek.adopted", "true")
	_, err := p.Prepare()
	return err
}

func Clone(cfg *Config, job *Job, url, parent string) (string, error) {
	return CloneNamed(cfg, job, url, parent, "")
}

// CloneNamed downloads a project into parent/<name> (name defaults to the repo name).
func CloneNamed(cfg *Config, job *Job, url, parent, name string) (string, error) {
	name = safeName(strings.TrimSpace(name))
	if name == "" {
		name = strings.TrimSuffix(flp.BaseName(strings.TrimSuffix(url, "/")), ".git")
	}
	if parent == "" {
		return "", errors.New("не выбрана папка, куда скачать проект")
	}
	if name == "" {
		return "", errors.New("не понял имя репозитория из ссылки")
	}
	dir := filepath.Join(parent, name)
	if exists(dir) {
		ents, _ := os.ReadDir(dir)
		if len(ents) > 0 {
			// та же папка уже есть: это может быть этот же проект (скачан раньше) или что-то чужое
			same := normRemote((&Git{Dir: dir, Cfg: cfg}).Remote()) == normRemote(url)
			msg := fmt.Sprintf("папка %s уже существует и не пустая", dir)
			if same {
				msg = fmt.Sprintf("этот проект уже скачан в %s", dir)
			}
			return "", &FixError{Msg: msg, Fix: &JobFix{Kind: "exists", Path: dir, Same: same}}
		}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	// «Отменить»: недокачанная папка проекта удаляется целиком (её создало это скачивание)
	job.SetCancellable(true)
	defer job.SetCancellable(false)
	defer func() {
		if job.Stopped() {
			os.RemoveAll(dir)
			job.Logf("Скачивание отменено — недокачанная папка удалена")
		}
	}()
	g := &Git{Dir: parent, Cfg: cfg, Job: job, URL: url, Env: []string{"GIT_LFS_SKIP_SMUDGE=1"}}
	job.Logf("Шаг 1 из 2: скачиваю проект (.flp и история)…")
	job.SetProgress("Скачивание проекта · подключаюсь к GitHub", -1)
	_, err := g.Run("clone", "--progress", url, dir)
	if err != nil {
		return "", err
	}
	if job.Stopped() {
		return "", errStopped
	}
	g.Dir, g.Env = dir, nil
	if _, err := g.Run("lfs", "install", "--local", "--skip-repo"); err != nil {
		return dir, err
	}
	g.RunQuiet("config", "uzbek.adopted", "true")
	g.RunQuiet("config", "lfs.concurrenttransfers", "8") // аудио с GitHub — в 8 потоков, а не в 3
	p := &Project{Dir: dir, Cfg: cfg, Job: job}
	objs := lfsObjects(g, "")
	var total int64
	for _, o := range objs {
		total += o.Size
	}
	if len(objs) > 0 {
		job.Logf("Шаг 2 из 2: аудио — %d файлов%s", len(objs), map[bool]string{true: ", " + fmtBytes(total), false: ""}[total > 0])
		p.peerFetchLFS("")
		job.setLFS(total, len(objs))
		job.SetProgress("Скачивание аудио с GitHub", -1)
		_, err := g.Run("lfs", "pull")
		job.setLFS(0, 0)
		if err != nil {
			if job.Stopped() {
				return "", errStopped
			}
			return dir, err
		}
	}
	if job.Stopped() {
		return "", errStopped
	}
	if n, _ := p.Relink(); n > 0 {
		job.Logf("Пути к сэмплам направлены в папку проекта: %d", n)
	}
	p.placePluginSamples()
	return dir, nil
}

// ---------- sync ----------

func isGenerated(path string) bool {
	return path == "PLUGINS.txt" || path == ".flsync/project.json" || path == ".flsync/lock.json" ||
		strings.HasPrefix(path, ".flsync/machines/") || path == ".gitignore" || path == ".gitattributes"
}

// conflictDir — куда кладутся копии .flp после конфликта (чтобы не мешались рядом с проектом).
const conflictDir = ".flsync/conflicts/"

func conflictName(path, who string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	name := fmt.Sprintf("%s (конфликт — %s %s)%s", base, who, nowStamp(), ext)
	if strings.EqualFold(ext, ".flp") {
		return conflictDir + name
	}
	return name
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
		case isGenerated(path) || path == "README.md" && p.readmeIsGenerated(): // README от FruityHub соберётся заново
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
			os.MkdirAll(filepath.Dir(filepath.Join(p.Dir, filepath.FromSlash(cp))), 0o755)
			if err := copyFile(abs, filepath.Join(p.Dir, filepath.FromSlash(cp))); err != nil {
				return copies, err
			}
			if _, err := g.Run("checkout", "--theirs", "--", path); err != nil {
				return copies, err
			}
			g.Run("add", "--", path, cp)
			copies = append(copies, cp)
			p.Job.Logf("Внимание: Конфликт в %s: версия друга сохранена под исходным именем, твоя — как «%s»", path, cp)
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

// readmeIsGenerated — README.md в конфликте собран FruityHub (у обеих сторон), а не написан руками.
func (p *Project) readmeIsGenerated() bool {
	g := p.git()
	for _, st := range []string{":2:README.md", ":3:README.md"} {
		if b, err := g.runBytes("show", st); err == nil && !bytes.Contains(b, []byte(readmeMark)) {
			return false
		}
	}
	return true
}

// commitAll stages everything and commits if there are changes.
func (p *Project) commitAll(msg string) (bool, error) {
	g := p.git()
	// re-evaluate unused files right before staging (a merge may have changed things)
	if _, err := p.updateIgnore(p.Analyze()); err != nil {
		return false, err
	}
	if !LoadShared(p.Dir).SendBackup {
		p.untrackBackup()
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
	// новое аудио — сначала с компьютера участника (быстрее), остальное git докачает с GitHub при слиянии
	p.peerFetchLFS("origin/" + b)
	if !g.HasCommits() {
		_, err := g.Run("reset", "--mixed", "origin/"+b)
		if err == nil {
			_, err = g.Run("checkout", "--", ".")
		}
		return nil, err
	}
	_, err = g.Run("merge", "--no-edit", "--no-verify", "origin/"+b)
	if err != nil && strings.Contains(strings.ToLower(errDetailsOf(err)), "unrelated histories") {
		// проект на этом компьютере начали отдельно (своя папка с теми же файлами), а на GitHub — уже с другого
		switch p.Resolve {
		case "theirs":
			return nil, p.takeTheirs(b)
		case "mine":
			p.Job.Logf("Оставляю свою версию — она заменит ту, что на GitHub, при отправке (история с GitHub сохранится)")
			_, err = g.Run("merge", "--no-edit", "--no-verify", "--allow-unrelated-histories", "-s", "ours", "origin/"+b)
			return nil, err
		case "both":
			// одинаковые файлы сольются сами, разные .flp — как при обычном конфликте (с GitHub под исходным именем, своя — копией)
			p.Job.Logf("Объединяю обе версии (ничего не потеряется)")
			_, err = g.Run("merge", "--no-edit", "--no-verify", "--allow-unrelated-histories", "origin/"+b)
		default:
			return nil, errNeedChoice
		}
	}
	if err != nil {
		if len(g.Conflicts()) == 0 {
			return nil, err
		}
		return p.resolveConflicts()
	}
	return nil, nil
}

// conflictOrig — исходное имя файла по имени копии «X (конфликт — кто время).flp».
func conflictOrig(cp string) (string, bool) {
	cp = strings.TrimPrefix(filepath.ToSlash(cp), conflictDir)
	ext := filepath.Ext(cp)
	base := strings.TrimSuffix(cp, ext)
	i := strings.LastIndex(base, " (конфликт — ")
	if i < 0 || !strings.HasSuffix(base, ")") {
		return "", false
	}
	return base[:i] + ext, true
}

// KeepConflictVersion: keep "theirs" — оставить версию с GitHub (копию со своей версией удалить),
// "mine" — оставить свою (копия встаёт на место оригинала). Уйдёт на GitHub при следующей отправке.
func (p *Project) KeepConflictVersion(cp, keep string) error {
	orig, ok := conflictOrig(cp)
	if !ok || strings.Contains(cp, "..") || filepath.IsAbs(cp) {
		return errors.New("это не копия конфликта")
	}
	src := filepath.Join(p.Dir, filepath.FromSlash(cp))
	dst := filepath.Join(p.Dir, filepath.FromSlash(orig))
	if !fileExists(src) {
		return errors.New("копии уже нет — видимо, ты уже выбрал")
	}
	switch keep {
	case "theirs":
	case "mine":
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("не смог заменить %s (закрой его в FL Studio): %w", orig, err)
		}
	default:
		return errors.New("неизвестный выбор")
	}
	return os.Remove(src)
}

// errNeedChoice: нужно решение человека — какую версию оставить.
var errNeedChoice = errors.New("проект на этом компьютере и на GitHub начаты отдельно — выбери, какую версию оставить")

// takeTheirs: берём версию с GitHub. Свои отличающиеся файлы — в Backup (FL Studio знает эту папку, на GitHub она не уходит),
// а вся своя история — в резервную ветку.
func (p *Project) takeTheirs(b string) error {
	g := p.git()
	stamp := time.Now().Format("2006-01-02 15-04")
	bak := filepath.Join(p.Dir, "Backup", "FruityHub — моя версия "+stamp)
	out, _ := g.RunQuiet("diff", "--name-only", "-z", "HEAD", "origin/"+b)
	n := 0
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		src := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if !fileExists(src) {
			continue
		}
		dst := filepath.Join(bak, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("не смог сохранить свою версию %s: %w", rel, err)
		}
		n++
	}
	branch := "fruityhub-моя-версия-" + time.Now().Format("20060102-150405")
	g.Run("branch", branch)
	if _, err := g.Run("reset", "--hard", "origin/"+b); err != nil {
		return err
	}
	if n > 0 {
		p.Job.Logf("Взял версию с GitHub. Твои отличавшиеся файлы (%d) сохранены в %s", n, bak)
	} else {
		p.Job.Logf("Взял версию с GitHub")
	}
	return nil
}

// errDetailsOf — полный текст ошибки git (не «человеческий»), чтобы узнавать конкретные случаи.
func errDetailsOf(err error) string {
	var ge *GitError
	if errors.As(err, &ge) {
		return ge.Msg
	}
	return err.Error()
}

type SyncResult struct {
	NeedChoice string   `json:"needChoice,omitempty"` // "unrelated" — спросить, какую версию оставить
	Conflicts  []string `json:"conflicts"`
	Relinked   int      `json:"relinked"`
	Committed  bool     `json:"committed"`
	Pushed     bool     `json:"pushed"`
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
	if errors.Is(err, errNeedChoice) {
		p.Job.Logf("Внимание: проект здесь и на GitHub начаты отдельно — выбери, какую версию оставить")
		res.NeedChoice = "unrelated"
		return res, nil
	}
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
	p.placePluginSamples()
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
	hubLock := p.hubID() != "" && p.Cfg.Hub().LocksKnown()
	if hubLock {
		os.Remove(p.lockFile()) // старый замок-файл больше не нужен: замки теперь на сервере
	} else if release {
		if l := p.ReadLock(); l != nil && l.User == p.Cfg.MachineID() {
			os.Remove(p.lockFile())
			msg += " · проект свободен"
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
		if errors.Is(err, errNeedChoice) {
			p.Job.Logf("Внимание: проект здесь и на GitHub начаты отдельно — выбери, какую версию оставить (твои изменения сохранены локально)")
			res.NeedChoice = "unrelated"
			return res, nil
		}
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
			if release && hubLock { // освобождаем только после отправки — чтобы друг взял уже новую версию
				if l := p.ReadLock(); l != nil && l.User == p.Cfg.MachineID() {
					if err := p.Cfg.Hub().ReleaseLock(p.hubID(), false); err != nil {
						p.Job.Logf("Не смог освободить проект: %s — нажми «Освободить»", err.Error())
					} else {
						p.Job.Logf("✓ Проект свободен")
					}
				}
			}
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

// Замок проекта хранится на сервере FruityHub (взять/освободить — мгновенно). Для проектов без сервера
// и со старым сервером — по-старому, файлом .flsync/lock.json в репозитории.

type hubIDEntry struct {
	id string
	at time.Time
}

var hubIDCache sync.Map // папка проекта → id на сервере

// hubID — id проекта на сервере FruityHub ("" — проект не на сервере).
func (p *Project) hubID() string {
	h := p.Cfg.Hub()
	if !h.Registered() {
		return ""
	}
	if v, ok := hubIDCache.Load(p.Dir); ok {
		e := v.(hubIDEntry)
		if e.id != "" || time.Since(e.at) < time.Minute {
			return e.id
		}
	}
	id := ""
	if hp := h.ProjectForRemote(p.git().Remote()); hp != nil {
		id = hp.ID
	}
	hubIDCache.Store(p.Dir, hubIDEntry{id, time.Now()})
	return id
}

// requestKey — чем помечаются запросы плагинов из этого проекта: id на сервере, иначе папка.
func (p *Project) requestKey() string {
	if id := p.hubID(); id != "" {
		return id
	}
	return "local:" + filepath.Base(p.Dir)
}

func (p *Project) lockFile() string { return filepath.Join(p.Dir, ".flsync", "lock.json") }

func (p *Project) ReadLock() *Lock {
	if id := p.hubID(); id != "" {
		if l, known := p.Cfg.Hub().ProjectLock(id); known {
			if l == nil {
				return nil
			}
			user := l.User
			if l.UserID == p.Cfg.UserID {
				user = p.Cfg.MachineID()
			}
			return &Lock{User: user, OS: l.OS, Since: l.Since, Note: l.Note}
		}
	}
	var l Lock
	if err := readJSON(p.lockFile(), &l); err != nil || l.User == "" {
		return nil
	}
	return &l
}

func (p *Project) TakeLock(force bool, note string) error {
	if id := p.hubID(); id != "" {
		_, err := p.Cfg.Hub().TakeLock(id, force, note)
		if err == nil {
			p.Job.Logf("✓ Проект закреплён за тобой — друзья увидят это через пару секунд")
			return p.getFreshIfBehind()
		}
		if !errors.Is(err, errNoHubLocks) {
			return err
		}
	}
	me := p.Cfg.MachineID()
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := p.Receive(); err != nil {
			return err
		}
		if l := p.ReadLock(); l != nil && l.User != me && !force {
			return fmt.Errorf("проект сейчас в работе у %s (с %s). Попроси его нажать «Отправить и освободить» или забери принудительно",
				l.User, time.Unix(l.Since, 0).Format("02.01 15:04"))
		}
		writeJSON(p.lockFile(), Lock{User: me, OS: OSName(), Since: time.Now().Unix(), Note: note})
		if _, err := p.commitAll("" + me + " взял проект в работу"); err != nil {
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

// getFreshIfBehind: берёшь проект в работу — сначала свежая версия, но полное «Получить» только если
// на GitHub действительно есть что-то новое (обычно нет — тогда это пара секунд).
func (p *Project) getFreshIfBehind() error {
	g := p.git()
	b := g.Branch()
	if b == "" || g.Remote() == "" {
		return nil
	}
	p.Job.Logf("Проверяю, нет ли свежих изменений от друзей…")
	if _, err := g.Run("fetch", "--quiet", "origin"); err != nil {
		p.Job.Logf("Не смог проверить GitHub (%s) — нажми «Получить» перед работой", firstLine(err.Error()))
		return nil
	}
	if !g.RemoteBranchExists(b) {
		return nil
	}
	out, err := g.RunQuiet("rev-list", "--count", "HEAD..origin/"+b)
	if err != nil || strings.TrimSpace(out) == "0" {
		p.Job.Logf("✓ У тебя последняя версия")
		return nil
	}
	p.Job.Logf("Есть свежие изменения (%s) — получаю", strings.TrimSpace(out))
	_, err = p.Receive()
	return err
}

func (p *Project) ReleaseLock() error {
	if id := p.hubID(); id != "" {
		err := p.Cfg.Hub().ReleaseLock(id, false)
		if err == nil {
			p.Job.Logf("✓ Проект свободен")
			return nil
		}
		if !errors.Is(err, errNoHubLocks) {
			return err
		}
	}
	l := p.ReadLock()
	if l == nil {
		return nil
	}
	os.Remove(p.lockFile())
	if _, err := p.commitAll("" + p.Cfg.MachineID() + " освободил проект"); err != nil {
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

// untrackBackup убирает из репозитория папку Backup в корне проекта, если её когда-то уже отправили
// (файлы на диске остаются, просто больше не синхронизируются).
func (p *Project) untrackBackup() {
	g := p.git()
	if !g.HasCommits() {
		return
	}
	out, err := g.RunQuiet("ls-files", "-z")
	if err != nil {
		return
	}
	var paths []string
	for _, f := range strings.Split(out, "\x00") {
		if i := strings.Index(f, "/"); i > 0 && strings.EqualFold(f[:i], "backup") {
			paths = append(paths, f)
		}
	}
	for len(paths) > 0 {
		n := len(paths)
		if n > 200 {
			n = 200
		}
		g.RunQuiet(append([]string{"rm", "-r", "-q", "--cached", "--ignore-unmatch", "--"}, paths[:n]...)...)
		paths = paths[n:]
	}
}
