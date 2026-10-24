package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ---------- сэмплы внутри плагинов (Pigments, Serum, Kontakt…) ----------
//
// Сэмпл, который загружен прямо в плагин, FL не видит: путь к нему лежит в состоянии плагина.
// FruityHub находит такие пути (в том числе в сжатом состоянии), при отправке копирует файл в Audio/,
// а у получателя кладёт копию туда, где её будет искать плагин: тот же путь относительно домашней
// папки (/Users/<ник>/… → /Users/<ты>/…) или тот же системный путь (/Library/Arturia/…),
// если у получателя есть папка производителя. Чужие папки не создаются.

// pluginSampleTarget — куда положить сэмпл, чтобы плагин его нашёл на этом компьютере ("" — некуда).
func pluginSampleTarget(orig string) string {
	path, factory := resolveFLPath(orig)
	if factory || path == "" {
		return ""
	}
	winPath := len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
	if winPath != (runtime.GOOS == "windows") { // путь другой системы — плагин там его всё равно не найдёт
		return ""
	}
	if !winPath && !strings.HasPrefix(path, "/") {
		return ""
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if winPath {
		parts = parts[1:] // без «C:»
	}
	home, _ := os.UserHomeDir()
	var base string
	var rest []string
	switch {
	case len(parts) > 3 && strings.EqualFold(parts[0], "Users"): // домашняя папка отправителя
		base, rest = home, parts[2:]
	case len(parts) > 2:
		root := "/"
		if winPath {
			root = path[:3]
		}
		base, rest = root, parts
	default:
		return ""
	}
	if base == "" || len(rest) < 2 {
		return ""
	}
	// папка производителя (первые две папки пути) должна уже быть: Music/Arturia, Library/Arturia, ProgramData/Xfer…
	anchor := filepath.Join(append([]string{base}, rest[:min(2, len(rest)-1)]...)...)
	if !dirExists(anchor) {
		return ""
	}
	return filepath.Join(append([]string{base}, rest...)...)
}

// placePluginSamples кладёт сэмплы плагинов из Audio/ туда, где их ждут плагины. Возвращает, сколько положено.
func (p *Project) placePluginSamples() int {
	n := 0
	for _, s := range p.Analyze().Samples {
		if !s.Embedded || s.Status != "project" || s.Local == "" {
			continue
		}
		dst := pluginSampleTarget(s.Original)
		if dst == "" || exists(dst) {
			continue
		}
		src := filepath.Join(p.Dir, filepath.FromSlash(s.Local))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		if err := os.Link(src, dst); err != nil {
			if err := copyFile(src, dst); err != nil {
				p.Job.Logf("Внимание: сэмпл плагина %s не положил в %s: %v", s.Name, filepath.Dir(dst), err)
				continue
			}
		}
		n++
	}
	if n > 0 {
		p.Job.Logf("Сэмплы плагинов разложены туда, где их ищут плагины: %d", n)
	}
	return n
}

// ---------- поиск сэмпла плагина по имени файла ----------
// Плагины часто хранят только имя файла (сэмпл лежит в их библиотеке). Ищем его в обычных местах
// библиотек: Музыка, Документы, Arturia/Native Instruments/Xfer… Индекс строится один раз, не дольше пары секунд.

var sampleIndex struct {
	start sync.Once
	ready chan struct{}
	names map[string]string
}

func init() { sampleIndex.ready = make(chan struct{}) }

func sampleRoots() []string {
	home, _ := os.UserHomeDir()
	var r []string
	for _, d := range []string{"Music", "Documents", "Desktop", "Downloads", "Splice"} {
		r = append(r, filepath.Join(home, d))
	}
	switch runtime.GOOS {
	case "darwin":
		r = append(r, "/Library/Arturia", "/Library/Application Support", filepath.Join(home, "Library", "Application Support"), "/Users/Shared")
	case "windows":
		r = append(r, os.Getenv("ProgramData"), `C:\Users\Public`)
	}
	return r
}

// findSampleByName — полный путь к аудиофайлу с таким именем в библиотеках сэмплов ("" — не нашёл или индекс ещё строится).
func findSampleByName(name string) string {
	sampleIndex.start.Do(func() { go buildSampleIndex() })
	select {
	case <-sampleIndex.ready:
		return sampleIndex.names[strings.ToLower(name)]
	default:
		return ""
	}
}

func buildSampleIndex() {
	names := map[string]string{}
	deadline := time.Now().Add(4 * time.Second)
	count := 0
	for _, root := range sampleRoots() {
		if root == "" || !dirExists(root) {
			continue
		}
		var walk func(dir string, depth int)
		walk = func(dir string, depth int) {
			if depth > 7 || count > 200000 || time.Now().After(deadline) {
				return
			}
			ents, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range ents {
				count++
				n := e.Name()
				if strings.HasPrefix(n, ".") {
					continue
				}
				if e.IsDir() {
					if strings.HasSuffix(strings.ToLower(n), ".app") || strings.EqualFold(n, "Caches") {
						continue
					}
					walk(filepath.Join(dir, n), depth+1)
					continue
				}
				switch strings.ToLower(filepath.Ext(n)) {
				case ".wav", ".aif", ".aiff", ".flac", ".mp3", ".ogg":
					k := strings.ToLower(n)
					if _, ok := names[k]; !ok {
						names[k] = filepath.Join(dir, n)
					}
				}
			}
		}
		walk(root, 0)
	}
	sampleIndex.names = names
	close(sampleIndex.ready)
}
