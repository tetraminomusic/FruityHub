package app

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Многим плагинам мало одного файла .vst3/.dll: пресеты, wavetable, скины и прочие ресурсы лежат
// отдельно — в «Документах», ProgramData, Common Files, AppData (на Mac — Application Support).
// Без них плагин в FL открывается серым окном или не грузится. Отправитель находит такие папки
// по имени плагина и производителя и кладёт в пакет, получатель раскладывает их по тем же местам.
// Файлы лицензий переносятся вместе с данными; привязанную к компьютеру активацию
// производитель может потребовать выполнить повторно.

const dataPrefix = "__uzbek_data__/"

const (
	dataDirMax   = 2 << 30 // одна папка данных больше 2 ГБ — не отправляем (это библиотека, её ставят отдельно)
	dataTotalMax = 3 << 30
	vendorDirMax = 300 << 20 // папку производителя целиком берём, только если она небольшая
)

// DataRoot — место, где плагины держат свои данные. Key одинаковый на компьютерах с одной системой.
type DataRoot struct {
	Key    string
	Path   string
	System bool // запись требует прав администратора
}

// PluginData — папка данных плагина внутри одного из DataRoot.
type PluginData struct {
	Root  string       `json:"root"`
	Rel   string       `json:"rel"` // путь относительно корня, с "/"
	Size  int64        `json:"size"`
	Note  string       `json:"note,omitempty"` // почему не отправляется
	Why   string       `json:"why,omitempty"`  // почему считаем это данными плагина
	abs   string       // файл рядом с плагином (Root == sideRoot)
	files []string     // файлы внутри (dataFiles), в постоянном порядке
	skip  map[int]bool // эти файлы у получателя уже есть — не передаём
}

func dataRoots() []DataRoot { return dataRootsOpt(false) }

// dataRootsOpt: withMissing — ещё и несуществующие пользовательские папки (получатель их создаст).
func dataRootsOpt(withMissing bool) []DataRoot {
	home, _ := os.UserHomeDir()
	var out []DataRoot
	add := func(key, p string, sys bool) {
		if p != "" && (dirExists(p) || withMissing && !sys) {
			out = append(out, DataRoot{key, p, sys})
		}
	}
	add("DOCS", documentsDir(), false)
	switch runtime.GOOS {
	case "windows":
		add("APPDATA", os.Getenv("APPDATA"), false)
		add("LOCALAPPDATA", os.Getenv("LOCALAPPDATA"), false)
		add("PROGRAMDATA", os.Getenv("ProgramData"), true)
		add("COMMONFILES", os.Getenv("CommonProgramFiles"), true)
		// многие производители (Arturia, Native Instruments, Spectrasonics…) держат ресурсы плагина в Program Files\<Производитель>\<Плагин>
		add("PROGRAMFILES", os.Getenv("ProgramFiles"), true)
		add("PROGRAMFILESX86", os.Getenv("ProgramFiles(x86)"), true)
	case "darwin":
		add("APPSUP", filepath.Join(home, "Library", "Application Support"), false)
		add("MUSIC", filepath.Join(home, "Music"), false)
		add("SYSAPPSUP", "/Library/Application Support", true)
		add("SYSLIB", "/Library", true) // например /Library/Arturia/<Плагин>/resources
	default:
		add("CONFIG", filepath.Join(home, ".config"), false)
		add("LOCALSHARE", filepath.Join(home, ".local", "share"), false)
	}
	return out
}

func dataRoot(key string) *DataRoot {
	for _, r := range dataRootsOpt(true) {
		if r.Key == key {
			rr := r
			return &rr
		}
	}
	return nil
}

// Папки, которые никогда не считаются данными плагина: там сами плагины, FL, система.
var dataSkip = map[string]bool{
	"vst": true, "vst2": true, "vst3": true, "vstplugins": true, "clap": true, "components": true, "plugins": true,
	"steinberg": true, "imageline": true, "flstudio": true, "microsoft": true, "windows": true, "apple": true,
	"google": true, "mozilla": true, "packages": true, "temp": true, "audio": true, "avid": true, "system": true,
	"adobe": true, "intel": true, "nvidia": true, "amd": true, "oracle": true, "java": true, "common": true,
	"programmafl": true, "progauzbeka": true, "flsync": true, "music": true, "documents": true,
	"commonfiles": true, "windowsapps": true, "internetexplorer": true, "windowsdefender": true, "fruityhub": true,
	"audiounits": true, "frameworks": true, "extensions": true, "caches": true, "logs": true, "preferences": true, "keychains": true,
}

// nameMatch: папка названа по плагину («Serum», «Serum Presets», «Vital»). Короткие имена — только точно.
func nameMatch(dir, key string) bool {
	n := normName(dir)
	if len(key) < 3 || n == "" || dataSkip[n] {
		return false
	}
	return n == key || len(key) >= 4 && strings.HasPrefix(n, key)
}

// vendorMatch: папка производителя («Xfer» для «Xfer Records», «Native Instruments»).
func vendorMatch(dir, vkey string) bool {
	n := normName(dir)
	if len(vkey) < 3 || n == "" || dataSkip[n] {
		return false
	}
	return n == vkey || len(n) >= 4 && strings.HasPrefix(vkey, n) || len(vkey) >= 4 && strings.HasPrefix(n, vkey)
}

// findPluginData ищет папки данных плагина на этом компьютере.
func findPluginData(name, vendor, pluginPath string) (take, skip []PluginData) {
	take, skip, _ = findPluginDataEx(name, vendor, pluginPath)
	return
}

// findPluginDataEx — то же плюс разбор самого файла плагина (библиотеки, зашитые пути, реестр).
func findPluginDataEx(name, vendor, pluginPath string) (take, skip []PluginData, deps *DepReport) {
	if vendor == "" { // в базе FL производителя нет — угадываем по папке плагина или moduleinfo.json
		vendor = guessVendor(&Config{}, pluginPath)
	}
	keys := []string{normName(name)}
	if b := normName(strings.TrimSuffix(filepath.Base(strings.TrimRight(pluginPath, `/\`)), filepath.Ext(pluginPath))); b != keys[0] && len(b) >= 3 {
		keys = append(keys, b)
	}
	vkey := normName(vendor)
	match := func(d string) bool {
		for _, k := range keys {
			if nameMatch(d, k) {
				return true
			}
		}
		return false
	}
	plug := strings.ToLower(filepath.Clean(pluginPath))
	seen := map[string]bool{}
	var total int64
	sep := string(os.PathSeparator)
	under := func(a, b string) bool { return a == b || strings.HasPrefix(a, b+sep) } // a внутри b
	fullOf := func(d PluginData) string {
		if d.abs != "" {
			return strings.ToLower(filepath.Clean(d.abs))
		}
		if r := dataRoot(d.Root); r != nil {
			return strings.ToLower(filepath.Clean(filepath.Join(r.Path, filepath.FromSlash(d.Rel))))
		}
		return ""
	}
	// имена других установленных плагинов — чтобы не забрать целиком папку производителя, где лежат и они
	others := map[string]bool{}
	for _, ip := range ScanInstalled(&Config{}, false) {
		if n := normName(ip.Name); n != "" && !anyNameMatch(ip.Name, keys) {
			others[n] = true
		}
	}
	hasOtherProducts := func(dir string) bool {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() && others[normName(e.Name())] {
				return true
			}
		}
		return false
	}
	consider := func(r DataRoot, rel string, vendorLevel bool, why string) {
		full := filepath.Join(r.Path, filepath.FromSlash(rel))
		lf := strings.ToLower(filepath.Clean(full))
		if seen[lf] || under(plug, lf) || under(lf, plug) {
			return
		}
		seen[lf] = true
		for _, t := range take { // уже берём папку, в которой это лежит
			if under(lf, fullOf(t)) {
				return
			}
		}
		if vendorLevel {
			// папку производителя целиком — только если она названа ровно как производитель, в ней нет других его
			// продуктов и мы ещё не взяли из неё папку самого плагина (своя папка всегда точнее общей)
			if normName(filepath.Base(full)) != vkey || hasOtherProducts(full) {
				return
			}
			for _, t := range take {
				if under(fullOf(t), lf) && !isLicenseFile(filepath.Base(fullOf(t))) {
					return
				}
			}
		}
		d := PluginData{Root: r.Key, Rel: rel, Size: dirSizeData(full), Why: why}
		switch {
		case d.Size == 0:
			return
		case vendorLevel && d.Size > vendorDirMax:
			d.Note = "общая папка производителя, слишком большая — скопируй нужное вручную"
		case d.Size > dataDirMax:
			d.Note = "больше 2 ГБ — это библиотека, её ставят установщиком"
		case total+d.Size > dataTotalMax:
			d.Note = "вместе с остальным больше 3 ГБ"
		}
		if d.Note != "" {
			skip = append(skip, d)
			return
		}
		// берём — а то, что уже взяли внутри этой папки, теперь лишнее
		kept := take[:0]
		for _, t := range take {
			if under(fullOf(t), lf) {
				total -= t.Size
				continue
			}
			kept = append(kept, t)
		}
		take = kept
		total += d.Size
		take = append(take, d)
	}
	for _, r := range dataRoots() {
		ents, err := os.ReadDir(r.Path)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				if isLicenseFile(e.Name()) && match(e.Name()) {
					consider(r, e.Name(), false, "файл лицензии плагина")
				}
				continue
			}
			switch {
			case match(e.Name()):
				consider(r, e.Name(), false, "названа как плагин")
			case vendorMatch(e.Name(), vkey):
				// папка производителя: берём из неё папки этого плагина, а если их нет — её целиком (если небольшая)
				// и на уровень глубже: «Arturia/Presets/DX7 V», «Arturia/Resources/DX7 V»
				sub, _ := os.ReadDir(filepath.Join(r.Path, e.Name()))
				found := false
				for _, s := range sub {
					if !s.IsDir() {
						if isLicenseFile(s.Name()) && match(s.Name()) {
							consider(r, e.Name()+"/"+s.Name(), false, "файл лицензии плагина")
						}
						continue
					}
					if match(s.Name()) {
						consider(r, e.Name()+"/"+s.Name(), false, "папка плагина у производителя")
						found = true
						continue
					}
					sub2, _ := os.ReadDir(filepath.Join(r.Path, e.Name(), s.Name()))
					for _, s2 := range sub2 {
						if s2.IsDir() && match(s2.Name()) {
							consider(r, e.Name()+"/"+s.Name()+"/"+s2.Name(), false, "папка плагина у производителя")
							found = true
						} else if !s2.IsDir() && isLicenseFile(s2.Name()) && match(s2.Name()) {
							consider(r, e.Name()+"/"+s.Name()+"/"+s2.Name(), false, "файл лицензии плагина")
						}
					}
				}
				if !found {
					consider(r, e.Name(), true, "папка производителя")
				}
			case isLicenseDir(e.Name()):
				sub, _ := os.ReadDir(filepath.Join(r.Path, e.Name()))
				for _, s := range sub {
					if match(s.Name()) && (s.IsDir() || isLicenseFile(s.Name())) {
						consider(r, e.Name()+"/"+s.Name(), false, "лицензия в общей папке")
					}
				}
			}
		}
	}
	// то, на что ссылается сам файл плагина
	deps, cands := pluginDeps(pluginPath, keys, vkey)
	// В общей папке VST берём только лицензии с именем этого плагина.
	// Содержимое самого пакета (.vst3/.component) уже входит в архив плагина.
	if st, err := os.Lstat(pluginPath); err == nil && st.Mode().IsRegular() {
		parent := filepath.Dir(filepath.Clean(pluginPath))
		ents, _ := os.ReadDir(parent)
		for _, e := range ents {
			if !e.IsDir() && isLicenseFile(e.Name()) && match(e.Name()) {
				cands = append(cands, depCand{root: DataRoot{Key: sideRoot}, rel: e.Name(), abs: filepath.Join(parent, e.Name()), why: "файл лицензии рядом с плагином"})
			}
		}
	}
	for _, c := range cands {
		if c.root.Key == sideRoot {
			if st, err := os.Lstat(c.abs); err == nil && st.Mode().IsRegular() && !seen[strings.ToLower(c.abs)] {
				seen[strings.ToLower(c.abs)] = true
				take = append(take, PluginData{Root: sideRoot, Rel: c.rel, Size: st.Size(), Why: c.why, abs: c.abs})
				total += st.Size()
			}
			continue
		}
		consider(c.root, c.rel, c.vendor, c.why)
	}
	sort.Slice(take, func(i, j int) bool { return take[i].Root+take[i].Rel < take[j].Root+take[j].Rel })
	deps.Data, deps.DataSkip = take, skip
	if deps.Data == nil {
		deps.Data = []PluginData{}
	}
	if deps.DataSkip == nil {
		deps.DataSkip = []PluginData{}
	}
	return take, skip, deps
}

// isLicenseFile распознаёт отдельные файлы лицензий для поиска вне папки данных.
// Файлы интерфейса и ресурсов передаются всегда, даже если в названии есть «activation»
// (у Arturia окно активации — часть интерфейса: gui-activation-generated.xml; без него плагин не открывается).
var resourceExts = map[string]bool{".xml": true, ".png": true, ".jpg": true, ".jpeg": true, ".svg": true, ".ttf": true, ".otf": true,
	".css": true, ".html": true, ".js": true, ".json": true, ".wav": true, ".aif": true, ".aiff": true, ".fxp": true, ".fxb": true,
	".vstpreset": true, ".nki": true, ".ini": false}

func isLicenseFile(name string) bool {
	n := strings.ToLower(name)
	ext := filepath.Ext(n)
	if pluginExts[ext] != "" || ext == ".so" || ext == ".dylib" || ext == ".exe" {
		return false
	}
	switch ext {
	case ".lic", ".license", ".licence", ".key", ".ilok", ".keyfile", ".reg":
		return true
	}
	if resourceExts[ext] {
		return false
	}
	for _, w := range []string{"licen", "serial", "keyfile", "authoriz", "authoris"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

func isLicenseDir(name string) bool {
	switch strings.ToLower(name) {
	case "licenses", "licences", "license", "licence", "licensing":
		return true
	}
	return false
}

func dirSizeData(p string) int64 {
	var n int64
	filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if i, e := d.Info(); e == nil && i.Mode().IsRegular() {
				n += i.Size()
			}
		}
		if n > dataDirMax+1 {
			return filepath.SkipAll
		}
		return nil
	})
	return n
}

// zipData дописывает папки данных в архив плагина (кроме файлов, которые у получателя уже есть).
func zipData(zw *zip.Writer, data []PluginData) error {
	for i := range data {
		d := &data[i]
		base, files := dataFiles(d)
		for j, f := range files {
			if d.skip[j] {
				continue
			}
			path := base
			name := dataPrefix + d.Root + "/" + d.Rel
			if f != "" {
				path = filepath.Join(base, filepath.FromSlash(f))
				name += "/" + f
			}
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue // недоступный файл пропускаем
			}
			h, err := zip.FileInfoHeader(info)
			if err != nil {
				continue
			}
			h.Name = name
			h.Method = zipMethodFor(zw, info.Name(), info.Size())
			fh, err := os.Open(path)
			if err != nil {
				continue
			}
			w, err := zw.CreateHeader(h)
			if err == nil {
				_, err = io.Copy(w, fh)
			}
			fh.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// installData раскладывает полученные папки данных (stage/<KEY>/<rel>/…) по местам на этом компьютере.
// Уже существующие файлы не трогает — свои пресеты получателя не затираются.
// binDir — папка с файлом установленного плагина: туда ложатся его библиотеки (PLUGINDIR).
// То, что требует прав администратора, добавляется в batch (выполнится одним запросом прав).
// overwrite (переустановка) — файлы данных заменяются версией отправителя (свои старые уходят в резервную копию).
func installData(job *Job, stage, binDir string, batch *elevBatch, overwrite bool) (done []string, problems []string) {
	bak := ""
	if overwrite {
		bak = filepath.Join(ConfigDir(), "plugin-backups", time.Now().Format("20060102-150405")+"-данные")
	}
	keys, _ := os.ReadDir(stage)
	for _, k := range keys {
		if !k.IsDir() {
			continue
		}
		r := dataRoot(k.Name())
		if k.Name() == sideRoot && binDir != "" {
			r = &DataRoot{Key: sideRoot, Path: binDir, System: !writableDir(binDir)}
		}
		src := filepath.Join(stage, k.Name())
		if r == nil {
			problems = append(problems, "не знаю, куда положить данные из "+k.Name()+" на этой системе")
			continue
		}
		if !r.System || writableDir(r.Path) {
			n, err := mergeTreeOpt(src, r.Path, bak)
			if err == nil || batch == nil {
				if err != nil {
					problems = append(problems, "данные в "+r.Path+": "+err.Error())
				} else if n > 0 {
					done = append(done, describeData(src, r.Path))
				}
				continue
			}
			// часть файлов не легла (нет прав на вложенную папку) — докладываем с правами администратора
		}
		if batch == nil {
			problems = append(problems, "нет прав на запись в "+r.Path)
			continue
		}
		if overwrite {
			batch.MergeOver(src, r.Path)
		} else {
			batch.Merge(src, r.Path)
		}
		done = append(done, describeData(src, r.Path))
	}
	return
}

func describeData(src, root string) string {
	var names []string
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		names = append(names, filepath.Join(root, e.Name()))
	}
	return strings.Join(names, ", ")
}

// mergeTree копирует файлы, которых ещё нет в dst. Возвращает число скопированных.
func mergeTree(src, dst string) (int, error) { return mergeTreeOpt(src, dst, "") }

// mergeTreeOpt: bak != "" — существующие файлы заменяются, а старые копии кладутся в bak (с теми же путями).
func mergeTreeOpt(src, dst, bak string) (int, error) {
	n := 0
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		t := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if exists(t) {
			if bak == "" {
				return nil
			}
			b := filepath.Join(bak, filepath.Base(dst), rel)
			os.MkdirAll(filepath.Dir(b), 0o755)
			if err := copyFile(t, b); err != nil {
				return err
			}
		}
		if err := copyFile(path, t); err != nil {
			return err
		}
		os.Chtimes(t, info.ModTime(), info.ModTime())
		n++
		return nil
	})
	return n, err
}
