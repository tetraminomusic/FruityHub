package app

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"

	"flsync/internal/flp"
)

// ---------- проверка перед отправкой: не пропали ли сэмплы ----------
//
// FL Studio при «удалить неиспользуемое аудио» не знает, что сэмпл загружен в синт (Serum, Vital, Sytrus…),
// и может удалить его с диска. Перед отправкой FruityHub смотрит:
//  1. аудио, которое есть в последней отправленной версии, а с диска пропало (и удалял его не FruityHub);
//  2. сэмплы, на которые ссылается проект (в каналах и внутри плагинов), но которых нет.
// Пропавшее можно вернуть одной кнопкой: из прошлой версии проекта (git) или из Корзины.

// LostFile — пропавший файл и откуда его можно вернуть.
type LostFile struct {
	Rel    string `json:"rel"`              // куда вернуть (путь в проекте)
	Name   string `json:"name"`             // имя файла
	Kind   string `json:"kind"`             // deleted — пропал из папки; missing — проект ссылается, а файла нет
	Plugin bool   `json:"plugin,omitempty"` // ссылка изнутри плагина (синт)
	From   string `json:"from,omitempty"`   // git — из версии проекта, trash — из Корзины, "" — вернуть неоткуда
	Rev    string `json:"rev,omitempty"`    // версия в git
	Src    string `json:"src,omitempty"`    // путь в git (или файл в Корзине)
}

type Precheck struct {
	Lost []LostFile `json:"lost"`
}

// intentionalPath — список файлов, которые удалил сам FruityHub (их не считаем пропавшими).
func intentionalPath(dir string) string {
	return filepath.Join(dir, ".flsync-local", "deleted.json")
}

func markIntentional(dir string, rels []string) {
	var l []string
	readJSON(intentionalPath(dir), &l)
	l = append(l, rels...)
	os.MkdirAll(filepath.Dir(intentionalPath(dir)), 0o755)
	writeJSON(intentionalPath(dir), l)
}

// CheckLost ищет пропавшие сэмплы и аудио.
func (p *Project) CheckLost() Precheck {
	res := Precheck{Lost: []LostFile{}}
	g := p.git()
	if !g.IsRepo() {
		return res
	}
	var intentional []string
	readJSON(intentionalPath(p.Dir), &intentional)
	skip := map[string]bool{}
	for _, r := range intentional {
		skip[r] = true
	}
	seen := map[string]bool{}
	if st, err := g.Status(); err == nil {
		for _, e := range st {
			if e.Kind != "deleted" || !flp.IsAudio(e.Path) || skip[e.Path] {
				continue
			}
			seen[strings.ToLower(flp.BaseName(e.Path))] = true
			res.Lost = append(res.Lost, LostFile{Rel: e.Path, Name: flp.BaseName(e.Path), Kind: "deleted", From: "git", Rev: "HEAD", Src: e.Path})
		}
	}
	a := p.Analyze()
	var hist map[string][2]string // имя → (версия, путь) — самая свежая версия, где файл был
	for _, s := range a.Samples {
		if s.Status != "missing" || seen[strings.ToLower(s.Name)] {
			continue
		}
		seen[strings.ToLower(s.Name)] = true
		lf := LostFile{Name: s.Name, Kind: "missing", Plugin: s.Embedded, Rel: audioDir + "/" + s.Name}
		if hist == nil {
			hist = audioHistory(g)
		}
		if h, ok := hist[strings.ToLower(s.Name)]; ok {
			lf.From, lf.Rev, lf.Src, lf.Rel = "git", h[0], h[1], h[1]
		} else if t := findInTrash(s.Name); t != "" {
			lf.From, lf.Src = "trash", t
		}
		res.Lost = append(res.Lost, lf)
	}
	sort.SliceStable(res.Lost, func(i, j int) bool { return res.Lost[i].Plugin && !res.Lost[j].Plugin })
	return res
}

// audioHistory — для каждого имени аудиофайла: последняя версия проекта, где он был, и его путь.
func audioHistory(g *Git) map[string][2]string {
	m := map[string][2]string{}
	out, err := g.RunQuiet("log", "--all", "--format=@%H", "--name-only", "--diff-filter=AMR")
	if err != nil {
		return m
	}
	rev := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "@") {
			rev = l[1:]
			continue
		}
		if l == "" || !flp.IsAudio(l) {
			continue
		}
		k := strings.ToLower(flp.BaseName(l))
		if _, ok := m[k]; !ok { // git log — от новых к старым: первое — самое свежее
			m[k] = [2]string{rev, l}
		}
	}
	return m
}

// RestoreLost возвращает файлы в папку проекта и направляет на них пути в .flp.
func (p *Project) RestoreLost(list []LostFile) (int, error) {
	g := p.git()
	n := 0
	var firstErr error
	for _, f := range list {
		rel := filepath.ToSlash(f.Rel)
		if rel == "" || strings.Contains(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		dst := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if exists(dst) {
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		var err error
		switch f.From {
		case "git":
			err = g.ShowToFile(f.Rev, f.Src, dst)
		case "trash":
			err = copyFile(f.Src, dst)
		default:
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			p.Job.Logf("Внимание: %s не вернул: %v", f.Name, err)
			continue
		}
		p.Job.Logf("✓ Вернул %s", rel)
		n++
	}
	if n > 0 {
		p.Relink()
		sizeCache.Delete(p.Dir)
	}
	return n, firstErr
}

// findInTrash ищет файл с таким именем в Корзине ("" — нет).
func findInTrash(name string) string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		ents, _ := os.ReadDir(filepath.Join(home, ".Trash"))
		base := strings.TrimSuffix(name, filepath.Ext(name))
		for _, e := range ents {
			n := e.Name()
			if strings.EqualFold(n, name) || strings.HasPrefix(strings.ToLower(n), strings.ToLower(base)+" ") && strings.EqualFold(filepath.Ext(n), filepath.Ext(name)) {
				return filepath.Join(home, ".Trash", n)
			}
		}
	case "windows":
		return findInRecycleBin(name)
	}
	return ""
}

// findInRecycleBin: в Корзине Windows у каждого файла $R… — содержимое, а $I… — исходный путь.
func findInRecycleBin(name string) string {
	drives := []string{os.Getenv("SystemDrive")}
	for _, d := range "DEFGH" {
		drives = append(drives, string(d)+":")
	}
	for _, d := range drives {
		if d == "" {
			continue
		}
		sids, _ := os.ReadDir(d + `\$Recycle.Bin`)
		for _, sid := range sids {
			dir := filepath.Join(d+`\$Recycle.Bin`, sid.Name())
			ents, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range ents {
				if !strings.HasPrefix(e.Name(), "$I") {
					continue
				}
				orig := recycleOrigPath(filepath.Join(dir, e.Name()))
				if orig != "" && strings.EqualFold(filepath.Base(orig), name) {
					r := filepath.Join(dir, "$R"+strings.TrimPrefix(e.Name(), "$I"))
					if exists(r) {
						return r
					}
				}
			}
		}
	}
	return ""
}

func recycleOrigPath(iFile string) string {
	b, err := os.ReadFile(iFile)
	if err != nil || len(b) < 28 {
		return ""
	}
	ver := binary.LittleEndian.Uint64(b)
	var u []uint16
	switch ver {
	case 2: // Windows 10/11: длина пути — в символах
		n := int(binary.LittleEndian.Uint32(b[24:]))
		for i := 0; i < n && 28+i*2+1 < len(b); i++ {
			u = append(u, binary.LittleEndian.Uint16(b[28+i*2:]))
		}
	case 1: // Vista–8: путь фиксированной длины с 24-го байта
		for i := 24; i+1 < len(b); i += 2 {
			u = append(u, binary.LittleEndian.Uint16(b[i:]))
		}
	default:
		return ""
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

// restoreMissingFromHistory — сэмплы, на которые ссылается проект (в том числе изнутри плагинов), но которых
// нет ни в папке проекта, ни там, где их ждёт .flp, достаются из прошлых версий проекта, если там они были.
func (p *Project) restoreMissingFromHistory() int {
	if !p.git().IsRepo() {
		return 0
	}
	var list []LostFile
	for _, f := range p.CheckLost().Lost {
		if f.Kind == "missing" && f.From == "git" {
			list = append(list, f)
		}
	}
	if len(list) == 0 {
		return 0
	}
	n, _ := p.RestoreLost(list)
	if n > 0 {
		p.Job.Logf("Сэмплы, которых не было на диске, достал из прошлых версий проекта: %d", n)
		LogActivity(p.Dir, "ok", fmt.Sprintf("Сэмплы, которых не было на диске, достаны из прошлых версий проекта: %d", n))
	}
	return n
}
