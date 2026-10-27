package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Полный откат проекта к версии из «Истории».
//
// История не переписывается: текущее состояние сначала коммитится (если есть несохранённые правки),
// потом файлы проекта становятся такими, как в выбранном коммите, и это записывается новым коммитом
// «Откат к версии …». Его можно отправить другу как обычные правки, а сам откат — отменить
// откатом к коммиту, который был перед ним.
//
// Не трогаются: служебная папка .flsync/ (настройки проекта, каталог плагинов), .gitattributes и .gitignore
// (что идёт через LFS и что не отправляется — текущие правила), и файлы, убранные «только у меня»
// (их на этом компьютере нет намеренно).

type RollbackResult struct {
	To       string `json:"to"`       // короткий хэш версии
	Changed  int    `json:"changed"`  // файлов стало как в той версии
	Removed  int    `json:"removed"`  // файлов, которых тогда не было, удалено
	Saved    bool   `json:"saved"`    // несохранённые правки сначала закоммичены
	Moved    int    `json:"moved"`    // не отправлявшиеся файлы, которые мешали, — в Backup
	BackupTo string `json:"backupTo"` // куда
	Before   string `json:"before"`   // коммит до отката (к нему можно вернуться)
	Nothing  bool   `json:"nothing"`  // проект и так в этой версии
}

func rollbackKeep(rel string) bool {
	return strings.HasPrefix(rel, ".flsync/") || rel == ".gitattributes" || rel == ".gitignore"
}

func (p *Project) RollbackTo(hash string) (*RollbackResult, error) {
	g := p.git()
	if hash == "" || strings.HasPrefix(hash, "-") {
		return nil, errors.New("не указана версия")
	}
	full, err := g.RunQuiet("rev-parse", "--verify", "--quiet", hash+"^{commit}")
	if err != nil || full == "" {
		return nil, errors.New("такой версии в истории проекта нет")
	}
	full = strings.TrimSpace(full)
	res := &RollbackResult{To: full[:7]}
	when, _ := g.RunQuiet("log", "-1", "--format=%ad", "--date=format:%d.%m %H:%M", full)
	subj, _ := g.RunQuiet("log", "-1", "--format=%s", full)

	// что было в истории до сохранения: новые файлы (ещё ни разу не записанные, например только что достанная
	// из истории копия .flp) откат не удаляет — они остаются как есть
	lsTree := func() map[string]bool {
		m := map[string]bool{}
		if out, err := g.RunQuiet("ls-tree", "-r", "-z", "--name-only", "HEAD"); err == nil {
			for _, f := range strings.Split(out, "\x00") {
				if f != "" {
					m[f] = true
				}
			}
		}
		return m
	}
	before := lsTree()

	// 1. текущее состояние — в историю, чтобы откат можно было отменить
	if c, err := p.commitAll(fmt.Sprintf("%s: состояние перед откатом к версии %s", p.Cfg.MachineID(), res.To)); err != nil {
		return nil, err
	} else if c {
		res.Saved = true
		p.Job.Logf("Твои несохранённые правки сначала записаны в историю (локально)")
	}
	head, _ := g.RunQuiet("rev-parse", "HEAD")
	res.Before = strings.TrimSpace(head)
	if len(res.Before) > 7 {
		res.Before = res.Before[:7]
	}

	hidden := map[string]bool{}
	for _, h := range p.HiddenLocal() {
		hidden[h] = true
	}
	tracked := lsTree()

	// 2. чем HEAD отличается от той версии
	out, err := g.RunQuiet("diff", "--name-status", "--no-renames", "-z", "HEAD", full)
	if err != nil {
		return nil, err
	}
	var restore, remove []string
	parts := strings.Split(out, "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		st, rel := strings.TrimSpace(parts[i]), parts[i+1]
		if rel == "" || rollbackKeep(rel) || hidden[rel] {
			continue
		}
		if strings.HasPrefix(st, "D") { // в той версии файла не было
			if before[rel] {
				remove = append(remove, rel)
			}
		} else {
			restore = append(restore, rel)
		}
	}
	if len(restore)+len(remove) == 0 {
		res.Nothing = true
		p.Job.Logf("Проект и так совпадает с этой версией")
		return res, nil
	}

	// файлы, которых нет в истории (не отправлялись), но лежат там, куда встанет старая версия, — не затираем, а убираем в Backup
	stamp := time.Now().Format("2006-01-02 15-04")
	bak := filepath.Join("Backup", "FruityHub — до отката "+stamp)
	for _, rel := range restore {
		if tracked[rel] {
			continue
		}
		src := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if !fileExists(src) {
			continue
		}
		dst := filepath.Join(p.Dir, bak, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.Rename(src, dst); err != nil {
			if err := copyFile(src, dst); err != nil {
				return nil, fmt.Errorf("не смог убрать %s в Backup (закрой его в программах): %w", rel, err)
			}
		}
		res.Moved++
	}
	if res.Moved > 0 {
		res.BackupTo = filepath.ToSlash(bak)
		p.Job.Logf("Не отправлявшиеся файлы, которые мешали (%d), — в %s", res.Moved, res.BackupTo)
	}

	// 3. аудио той версии — сначала с компьютера участника (быстрее), остальное git докачает с GitHub
	p.peerFetchLFS(full)
	p.Job.Logf("Возвращаю файлы версии %s (%s): %d, убираю появившиеся позже: %d", res.To, strings.TrimSpace(when), len(restore), len(remove))
	for len(remove) > 0 {
		n := min(len(remove), 200)
		if _, err := g.RunQuiet(append([]string{"rm", "-q", "-r", "--ignore-unmatch", "--"}, remove[:n]...)...); err != nil {
			return nil, fmt.Errorf("не смог удалить файлы (закрой проект в FL Studio): %w", err)
		}
		res.Removed += n
		remove = remove[n:]
	}
	total := len(restore)
	for done := 0; len(restore) > 0; {
		if p.Job != nil && p.Job.Stopped() {
			return nil, errStopped
		}
		n := min(len(restore), 100)
		if _, err := g.Run(append([]string{"checkout", full, "--"}, restore[:n]...)...); err != nil {
			return nil, fmt.Errorf("не смог вернуть файлы (закрой проект в FL Studio): %w", err)
		}
		done += n
		res.Changed += n
		restore = restore[n:]
		if p.Job != nil && total > 100 {
			p.Job.SetProgress("Возвращаю файлы", done*100/total)
		}
	}

	// 4. откат — новым коммитом
	msg := fmt.Sprintf("%s: откат к версии %s от %s", p.Cfg.MachineID(), res.To, strings.TrimSpace(when))
	if s := strings.TrimSpace(subj); s != "" {
		if r := []rune(s); len(r) > 60 {
			s = string(r[:60]) + "…"
		}
		msg += " («" + s + "»)"
	}
	if _, err := g.RunQuiet("diff", "--cached", "--quiet"); err != nil {
		if _, err := g.Run("commit", "--no-verify", "-q", "-m", msg); err != nil {
			return nil, err
		}
	}
	p.markIntentionalRollback()
	LogActivity(p.Dir, "ok", fmt.Sprintf("Откат к версии %s от %s (до отката был %s)", res.To, strings.TrimSpace(when), res.Before))
	p.Job.Logf("✓ Проект как в версии %s. Отправь, чтобы у друга стало так же; вернуть как было — откат к %s", res.To, res.Before)
	sizeCache.Delete(p.Dir)
	return res, nil
}

// после отката аудио, которого не было в той версии, удалено намеренно — проверка «пропавших сэмплов» не должна его возвращать
func (p *Project) markIntentionalRollback() {
	g := p.git()
	out, err := g.RunQuiet("diff", "--name-only", "--diff-filter=D", "-z", "HEAD~1", "HEAD")
	if err != nil {
		return
	}
	var l []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			l = append(l, f)
		}
	}
	if len(l) > 0 {
		markIntentional(p.Dir, l)
	}
}

// quickFetch — свежие данные с GitHub, если сейчас не идёт отправка или получение; медленную сеть не ждём
// дольше wait (fetch доделается сам).
func (s *Server) quickFetch(p *Project, wait time.Duration) bool {
	g := p.git()
	if !g.IsRepo() || g.Remote() == "" || !s.mu.TryLock() {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- g.Fetch(); s.mu.Unlock() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(wait):
		return false
	}
}

func (s *Server) routes7(api func(string, func(r *http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }

	// «Обновить»: свежие данные с GitHub (если сейчас не идёт отправка/получение), размер заново
	api("/api/project/refresh", func(r *http.Request) (any, error) {
		p, err := s.project(str(body(r), "path"), nil)
		if err != nil {
			return nil, err
		}
		sizeCache.Delete(p.Dir)
		fetched := s.quickFetch(p, 20*time.Second)
		return map[string]any{"fetched": fetched}, nil
	})

	api("/api/project/rollback", func(r *http.Request) (any, error) {
		b := body(r)
		path := str(b, "path")
		j := s.startJob("Откат проекта к версии", path, func(j *Job) (any, error) {
			p, err := s.project(path, j)
			if err != nil {
				return nil, err
			}
			return p.RollbackTo(str(b, "hash"))
		})
		return map[string]string{"id": j.ID, "job": j.ID, "title": j.Title}, nil
	})

	// запустить .flp из папки проекта (например, достанную из истории копию)
	api("/api/project/launch-file", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		rel := filepath.ToSlash(str(b, "rel"))
		if rel == "" || strings.Contains(rel, "..") || filepath.IsAbs(rel) || !strings.EqualFold(filepath.Ext(rel), ".flp") {
			return nil, errors.New("это не файл проекта")
		}
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if !fileExists(abs) {
			return nil, errors.New("файла уже нет — достань копию заново")
		}
		return "ok", OpenFile(abs)
	})
}
