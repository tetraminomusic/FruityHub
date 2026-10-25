package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---------- удалить неиспользуемое аудио только у себя ----------
//
// Файл, который уже лежит на GitHub (неудачный дубль, старый рендер), можно убрать с этого компьютера,
// не удаляя его у друзей и на GitHub: файл уходит в Корзину, git перестаёт его замечать (skip-worktree),
// git-lfs больше его не скачивает (lfs.fetchexclude), а вторая копия в .git/lfs удаляется — место освобождается
// целиком. «Вернуть» скачивает его обратно.

// TrashLocal убирает файлы только с этого компьютера. Возвращает, сколько убрано и сколько места освободилось.
func (p *Project) TrashLocal(rels []string) (int, int64, error) {
	a := p.Analyze()
	unused := map[string]AudioFile{}
	for _, f := range a.Audio {
		if !f.Used {
			unused[f.Rel] = f
		}
	}
	g := p.git()
	oids := lfsOidsByPath(g)
	n, freed := 0, int64(0)
	var hide []string
	for _, rel := range rels {
		f, ok := unused[rel]
		if !ok {
			return n, freed, fmt.Errorf("«%s» используется в проекте или не найден — не удаляю", rel)
		}
		if f.Included {
			return n, freed, fmt.Errorf("«%s» отмечен «всегда отправлять» (например, сэмпл синта) — не удаляю", rel)
		}
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if !isInside(p.Dir, abs) {
			return n, freed, errors.New("файл вне проекта")
		}
		if f.Tracked {
			if _, err := g.RunQuiet("update-index", "--skip-worktree", "--", rel); err != nil {
				return n, freed, fmt.Errorf("не получилось скрыть %s от git: %w", rel, err)
			}
			hide = append(hide, rel)
		}
		if err := moveToTrash(abs); err != nil {
			if f.Tracked {
				g.RunQuiet("update-index", "--no-skip-worktree", "--", rel)
			}
			return n, freed, fmt.Errorf("не получилось удалить %s: %w", rel, err)
		}
		freed += f.Size
		if oid := oids[rel]; f.Tracked && validOid(oid) && !oidUsedElsewhere(oids, oid, rel) {
			obj := lfsObjPath(lfsObjectsDir(g), oid)
			if st, err := os.Stat(obj); err == nil && os.Remove(obj) == nil {
				freed += st.Size()
			}
		}
		n++
	}
	if len(hide) > 0 {
		setFetchExclude(g, append(fetchExclude(g), hide...))
	}
	sizeCache.Delete(p.Dir)
	return n, freed, nil
}

// HiddenLocal — что убрано только у себя (на GitHub и у друзей есть).
func (p *Project) HiddenLocal() []string {
	out, err := p.git().RunQuiet("ls-files", "-v", "-z")
	if err != nil {
		return nil
	}
	var l []string
	for _, e := range strings.Split(out, "\x00") {
		if strings.HasPrefix(e, "S ") {
			l = append(l, e[2:])
		}
	}
	sort.Strings(l)
	return l
}

// RestoreLocal возвращает убранные у себя файлы (скачивает их снова).
func (p *Project) RestoreLocal(rels []string) error {
	g := p.git()
	set := map[string]bool{}
	for _, r := range rels {
		set[r] = true
		g.RunQuiet("update-index", "--no-skip-worktree", "--", r)
	}
	var keep []string
	for _, x := range fetchExclude(g) {
		if !set[x] {
			keep = append(keep, x)
		}
	}
	setFetchExclude(g, keep)
	if _, err := g.Run(append([]string{"lfs", "pull", "--include"}, strings.Join(rels, ","))...); err != nil {
		return err
	}
	_, err := g.Run(append([]string{"checkout", "--"}, rels...)...)
	sizeCache.Delete(p.Dir)
	return err
}

func fetchExclude(g *Git) []string {
	out, _ := g.RunQuiet("config", "--get", "lfs.fetchexclude")
	var l []string
	for _, x := range strings.Split(strings.TrimSpace(out), ",") {
		if x = strings.TrimSpace(x); x != "" {
			l = append(l, x)
		}
	}
	return l
}

func setFetchExclude(g *Git, l []string) {
	seen := map[string]bool{}
	var u []string
	for _, x := range l {
		if !seen[x] {
			seen[x] = true
			u = append(u, x)
		}
	}
	if len(u) == 0 {
		g.RunQuiet("config", "--unset", "lfs.fetchexclude")
		return
	}
	g.RunQuiet("config", "lfs.fetchexclude", strings.Join(u, ","))
}

// lfsOidsByPath — oid LFS-объекта для каждого файла текущей версии.
func lfsOidsByPath(g *Git) map[string]string {
	m := map[string]string{}
	if out, err := g.RunQuiet("lfs", "ls-files", "--json"); err == nil {
		var v struct {
			Files []lfsObj `json:"files"`
		}
		if json.Unmarshal([]byte(out), &v) == nil {
			for _, o := range v.Files {
				m[o.Name] = o.Oid
			}
			return m
		}
	}
	out, _ := g.RunQuiet("lfs", "ls-files", "-l")
	for _, line := range strings.Split(out, "\n") {
		if f := strings.SplitN(strings.TrimSpace(line), " ", 3); len(f) == 3 && validOid(f[0]) {
			m[f[2]] = f[0]
		}
	}
	return m
}

func oidUsedElsewhere(oids map[string]string, oid, except string) bool {
	for p, o := range oids {
		if o == oid && p != except {
			return true
		}
	}
	return false
}
