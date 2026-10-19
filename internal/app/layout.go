package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"flsync/internal/flp"
)

// ---------- Audio/ folder ----------

// OrganizeAudio moves audio files lying in the project root into Audio/,
// so the root keeps only .flp, PLUGINS.txt and service files.
func (p *Project) OrganizeAudio() ([]string, error) {
	ents, err := os.ReadDir(p.Dir)
	if err != nil {
		return nil, err
	}
	var moved []string
	for _, e := range ents {
		if e.IsDir() || !flp.IsAudio(e.Name()) {
			continue
		}
		src := filepath.Join(p.Dir, e.Name())
		dst, same, err := uniqueTarget(filepath.Join(p.Dir, audioDir), e.Name(), src)
		if err != nil {
			return moved, err
		}
		if same { // identical copy already in Audio/
			if err := os.Remove(src); err != nil {
				return moved, err
			}
		} else if err := os.Rename(src, dst); err != nil {
			return moved, fmt.Errorf("не получилось перенести %s (файл открыт в FL?): %w", e.Name(), err)
		}
		moved = append(moved, e.Name())
	}
	return moved, nil
}

// uniqueTarget returns where to put name inside dir. same=true when an
// identical file is already there.
func uniqueTarget(dir, name, src string) (string, bool, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	sum := ""
	for i := 1; ; i++ {
		cand := name
		if i > 1 {
			cand = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		dst := filepath.Join(dir, cand)
		if !exists(dst) {
			return dst, false, nil
		}
		if src == "" {
			continue
		}
		if sum == "" {
			sum = fileSHA1(src)
		}
		if fileSHA1(dst) == sum {
			return dst, true, nil
		}
	}
}

// ---------- several .flp in one folder ----------

// isCopyName: conflict copies and restored versions made by this app.
func isCopyName(rel string) bool {
	return strings.Contains(rel, "(конфликт") || strings.Contains(rel, "(версия")
}

// ExtraFLPs returns the .flp files that look like separate projects living in
// this folder (not conflict/version copies, not marked as "just a version").
func (p *Project) ExtraFLPs() []string {
	var keep []string
	if lp := p.Cfg.Project(p.Dir); lp != nil {
		keep = lp.KeepFLPs
	}
	var out []string
	for _, rel := range p.FLPs() {
		if isCopyName(rel) {
			continue
		}
		skip := false
		for _, k := range keep {
			if k == rel {
				skip = true
			}
		}
		if !skip {
			out = append(out, rel)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// MainFLPGuess picks the project's "main" .flp: same name as the folder,
// otherwise the most recently saved one.
func (p *Project) MainFLPGuess(list []string) string {
	folder := strings.ToLower(filepath.Base(p.Dir))
	best, bestT := "", time.Time{}
	for _, rel := range list {
		if strings.ToLower(strings.TrimSuffix(flp.BaseName(rel), filepath.Ext(rel))) == folder {
			return rel
		}
		if st, err := os.Stat(filepath.Join(p.Dir, filepath.FromSlash(rel))); err == nil && st.ModTime().After(bestT) {
			best, bestT = rel, st.ModTime()
		}
	}
	return best
}

type SplitResult struct {
	NewProjects []string `json:"newProjects"`
	Moved       int      `json:"moved"`
	Copied      int      `json:"copied"`
}

// Split moves each of `extract` into its own sibling folder together with the
// audio only it uses (audio shared with other .flp is copied). `keep` are
// remembered as "versions" and no longer reported.
func (p *Project) Split(extract, keep []string, group string) (*SplitResult, error) {
	res := &SplitResult{}
	if lp := p.Cfg.Project(p.Dir); lp != nil && len(keep) > 0 {
		lp.KeepFLPs = append(lp.KeepFLPs, keep...)
		p.Cfg.Save()
	}
	if len(extract) == 0 {
		return res, nil
	}
	// who uses which audio (by file name, the same rule as everywhere else)
	users := map[string]map[string]bool{} // lower basename -> set of flp rel
	for _, rel := range p.FLPs() {
		f, err := flp.ParseFile(filepath.Join(p.Dir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		info := f.Info()
		for _, sp := range append(info.Samples, info.Embedded...) {
			k := strings.ToLower(flp.BaseName(sp))
			if users[k] == nil {
				users[k] = map[string]bool{}
			}
			users[k][rel] = true
		}
	}
	local := map[string]string{}
	p.walk(func(rel, abs string, _ fs.FileInfo) {
		if flp.IsAudio(rel) {
			k := strings.ToLower(flp.BaseName(rel))
			if prev, ok := local[k]; !ok || audioRank(rel) < audioRank(prev) {
				local[k] = rel
			}
		}
	})
	parent := filepath.Dir(p.Dir)
	for _, rel := range extract {
		src := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if !exists(src) || !strings.EqualFold(filepath.Ext(rel), ".flp") {
			return res, fmt.Errorf("нет файла %s", rel)
		}
		name := strings.TrimSuffix(flp.BaseName(rel), filepath.Ext(rel))
		dir := filepath.Join(parent, safeName(name))
		for i := 2; exists(dir); i++ {
			dir = filepath.Join(parent, fmt.Sprintf("%s (%d)", safeName(name), i))
		}
		if err := os.MkdirAll(filepath.Join(dir, audioDir), 0o755); err != nil {
			return res, err
		}
		if err := os.Rename(src, filepath.Join(dir, flp.BaseName(rel))); err != nil {
			return res, fmt.Errorf("не получилось перенести %s (закрой его в FL): %w", rel, err)
		}
		for k, set := range users {
			if !set[rel] || local[k] == "" {
				continue
			}
			from := filepath.Join(p.Dir, filepath.FromSlash(local[k]))
			to := filepath.Join(dir, audioDir, flp.BaseName(local[k]))
			if exists(to) {
				continue
			}
			if len(set) == 1 { // only this project uses it
				if err := os.Rename(from, to); err != nil {
					return res, err
				}
				res.Moved++
			} else {
				if err := copyFile(from, to); err != nil {
					return res, err
				}
				res.Copied++
			}
		}
		np := &Project{Dir: dir, Cfg: p.Cfg, Job: p.Job}
		np.relink(true)
		if p.Cfg.Project(dir) == nil {
			p.Cfg.Projects = append(p.Cfg.Projects, LocalProject{Path: dir, RelinkOnReceive: true, Group: group})
		}
		res.NewProjects = append(res.NewProjects, dir)
		p.Job.Logf("«%s» теперь отдельный проект: %s", name, dir)
	}
	sort.Slice(p.Cfg.Projects, func(i, j int) bool {
		return strings.ToLower(filepath.Base(p.Cfg.Projects[i].Path)) < strings.ToLower(filepath.Base(p.Cfg.Projects[j].Path))
	})
	p.Cfg.Save()
	p.Relink() // audio that moved away from this project is no longer here
	return res, nil
}

// ---------- deleting unused audio ----------

// TrashUnused moves the given unused audio files to the system trash.
func (p *Project) TrashUnused(rels []string) (int, error) {
	a := p.Analyze()
	unused := map[string]bool{}
	for _, f := range a.Audio {
		if !f.Used {
			unused[f.Rel] = true
		}
	}
	n := 0
	for _, rel := range rels {
		if !unused[rel] {
			return n, fmt.Errorf("«%s» используется в проекте или не найден — не удаляю", rel)
		}
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if !isInside(p.Dir, abs) {
			return n, errors.New("файл вне проекта")
		}
		if err := moveToTrash(abs); err != nil {
			return n, fmt.Errorf("не получилось удалить %s: %w", rel, err)
		}
		n++
	}
	return n, nil
}

// ---------- adopting an existing git repo ----------

type AdoptInfo struct {
	Remote       string    `json:"remote"`
	Branch       string    `json:"branch"`
	Commits      int       `json:"commits"`
	LastCommit   string    `json:"lastCommit"`
	LastAuthor   string    `json:"lastAuthor"`
	HasFlsync    bool      `json:"hasFlsync"`
	Dirty        int       `json:"dirty"`
	NonLFSAudio  []string  `json:"nonLfsAudio"`
	Participants []HubUser `json:"participants"`
	OnServer     bool      `json:"onServer"` // the remote is a project of our server
	PartErr      string    `json:"participantsError,omitempty"`
	Behind       int       `json:"behind"`
	FetchErr     string    `json:"fetchError,omitempty"`
	GitHub       bool      `json:"github"`
}

func (p *Project) IsAdopted() bool {
	v, _ := p.git().RunQuiet("config", "--get", "uzbek.adopted")
	return v == "true"
}

func (p *Project) AdoptInfo() *AdoptInfo {
	g := p.git()
	info := &AdoptInfo{Remote: g.Remote(), Branch: g.Branch(), HasFlsync: exists(filepath.Join(p.Dir, ".flsync"))}
	if out, err := g.RunQuiet("rev-list", "--count", "HEAD"); err == nil {
		fmt.Sscanf(out, "%d", &info.Commits)
		info.LastCommit, _ = g.RunQuiet("log", "-1", "--format=%s (%ar)")
		info.LastAuthor, _ = g.RunQuiet("log", "-1", "--format=%an")
	}
	if st, err := g.Status(); err == nil {
		info.Dirty = len(st)
	}
	lfs := map[string]bool{}
	if out, err := g.RunQuiet("lfs", "ls-files", "-n"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			lfs[strings.TrimSpace(l)] = true
		}
	}
	for path := range g.Tracked() {
		if flp.IsAudio(path) && !lfs[path] {
			info.NonLFSAudio = append(info.NonLFSAudio, path)
		}
	}
	sort.Strings(info.NonLFSAudio)
	if info.Remote != "" {
		if err := g.Fetch(); err != nil {
			info.FetchErr = err.Error()
		}
		_, info.Behind = g.AheadBehind()
		_, _, info.GitHub = repoSlug(info.Remote)
		if hp := p.Cfg.Hub().ProjectForRemote(info.Remote); hp != nil {
			info.OnServer, info.Participants = true, hp.Members
		}
	}
	return info
}

// Adopt connects an existing repository "as is": history and remote stay,
// only the app's local setup (LFS filters, service files) is added.
func (p *Project) Adopt() error {
	g := p.git()
	if _, err := g.Run("lfs", "install", "--local", "--skip-repo"); err != nil {
		return err
	}
	g.RunQuiet("config", "uzbek.adopted", "true")
	p.Job.Logf("Репозиторий подключён, история и участники сохранены")
	if _, err := p.Prepare(); err != nil {
		return err
	}
	if g.Remote() != "" {
		if _, behind := g.AheadBehind(); behind > 0 {
			p.Job.Logf("На сервере есть новые изменения (%d) — нажми «Получить»", behind)
		}
	}
	return nil
}
