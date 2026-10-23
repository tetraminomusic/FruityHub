package app

import (
	"bufio"
	"debug/macho"
	"debug/pe"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Разбор самого файла плагина: что ему нужно кроме него самого.
//   - библиотеки из таблицы импорта (PE на Windows, Mach-O на Mac) и те, что он грузит по имени;
//   - пути, зашитые в бинарник (напр. «Arturia\DX7 V\resources», «%APPDATA%\Vendor», «/Library/Vendor/…»);
//   - ключи реестра, где плагин ищет свои папки (Windows).
// Найденное на компьютере отправителя уходит вместе с плагином, недостающее у получателя — в предупреждения.

const sideRoot = "PLUGINDIR" // «рядом с файлом плагина» у получателя

// DepItem — одна найденная зависимость, для окна «Зависимости».
type DepItem struct {
	Kind   string `json:"kind"`   // lib, path, registry
	Name   string `json:"name"`   // имя библиотеки / путь как он записан в плагине / ключ реестра
	Path   string `json:"path"`   // где нашлось на этом компьютере
	Status string `json:"status"` // take (поедет с плагином), inside (уже в пакете плагина), system, runtime, missing, skip
	Note   string `json:"note,omitempty"`
}

// DepReport — всё, что нашлось в плагине.
type DepReport struct {
	Plugin   string       `json:"plugin"`
	Path     string       `json:"path"`
	Binary   string       `json:"binary"`
	Arch     []string     `json:"arch"`
	Items    []DepItem    `json:"items"`
	Data     []PluginData `json:"data"`     // что уйдёт вместе с плагином (папки данных и библиотеки)
	DataSkip []PluginData `json:"dataSkip"` // что нашлось, но не поедет
	Registry []RegValue   `json:"registry"` // пути из реестра, которые перенесутся
}

// RegValue — строковое значение реестра с путём (лицензии и прочее не берём).
type RegValue struct {
	Hive string `json:"hive"` // HKCU / HKLM
	Key  string `json:"key"`  // SOFTWARE\Vendor\Plugin
	Name string `json:"name"`
	Type string `json:"type"` // REG_SZ / REG_EXPAND_SZ
	Data string `json:"data"`
}

// depCand — кандидат в данные плагина, найденный разбором бинарника.
type depCand struct {
	root   DataRoot
	rel    string
	abs    string // для библиотек рядом с плагином (sideRoot)
	vendor bool   // папка производителя целиком
	why    string
}

type binInfo struct {
	libs    []string // импорт
	dynLibs []string // имена .dll/.dylib в строках (грузятся вручную)
	paths   []string
	regKeys []string
}

var (
	binCacheMu sync.Mutex
	binCache   = map[string]binCacheEntry{}
)

type binCacheEntry struct {
	mod  time.Time
	info *binInfo
}

// analyzeBinary читает таблицу импорта и строки бинарника (с кэшем по времени изменения).
func analyzeBinary(bin string) *binInfo {
	st, err := os.Stat(bin)
	if err != nil {
		return &binInfo{}
	}
	binCacheMu.Lock()
	if e, ok := binCache[bin]; ok && e.mod.Equal(st.ModTime()) {
		binCacheMu.Unlock()
		return e.info
	}
	binCacheMu.Unlock()
	info := &binInfo{libs: binImports(bin)}
	seen := map[string]bool{}
	if st.Size() < 1<<30 {
		binStrings(bin, func(s string) {
			if seen[s] || len(seen) > 50000 {
				return
			}
			seen[s] = true
			switch {
			case regKeyRe.MatchString(s):
				if k := cleanRegKey(s); k != "" {
					info.regKeys = append(info.regKeys, k)
				}
			case dllNameRe.MatchString(s):
				if strings.HasSuffix(strings.ToLower(s), ".dll") {
					s = filepath.Base(strings.ReplaceAll(s, `\`, "/"))
				}
				info.dynLibs = append(info.dynLibs, s) // для Mac — полный путь
			case strings.ContainsAny(s, `/\`):
				info.paths = append(info.paths, s)
			}
		})
	}
	binCacheMu.Lock()
	binCache[bin] = binCacheEntry{st.ModTime(), info}
	binCacheMu.Unlock()
	return info
}

var (
	regKeyRe  = regexp.MustCompile(`(?i)^(?:HKEY_(?:LOCAL_MACHINE|CURRENT_USER)\\|HKLM\\|HKCU\\)?SOFTWARE\\[^\\%]{2,}`)
	dllNameRe = regexp.MustCompile(`(?i)^[^%*?<>|"]*[\w+-]\.(dll|dylib)$`)
	fmtSpecRe = regexp.MustCompile(`%[-+ #0-9.]*l?[sdiuxXcSfp]|\{[0-9]*\}|\$\{`)
	envVarRe  = regexp.MustCompile(`^%(\w+)%`)
)

func cleanRegKey(s string) string {
	i := strings.Index(strings.ToUpper(s), `SOFTWARE\`)
	if i < 0 || fmtSpecRe.MatchString(s) {
		return ""
	}
	k := strings.TrimRight(s[i:], `\ `)
	if strings.ContainsAny(k, `/*?"<>|`) || len(k) > 200 {
		return ""
	}
	l := strings.ToLower(k)
	for _, p := range []string{`software\microsoft`, `software\classes`, `software\policies`, `software\wow6432node\microsoft`, `software\image-line`} {
		if strings.HasPrefix(l, p) {
			return ""
		}
	}
	return k
}

// binImports — библиотеки из таблицы импорта.
func binImports(bin string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			out = append(out, n)
		}
	}
	if f, err := pe.Open(bin); err == nil {
		syms, _ := f.ImportedSymbols() // «функция:библиотека.dll»
		for _, s := range syms {
			if i := strings.LastIndex(s, ":"); i >= 0 {
				add(s[i+1:])
			}
		}
		f.Close()
		return out
	}
	if ff, err := macho.OpenFat(bin); err == nil {
		for _, a := range ff.Arches {
			libs, _ := a.ImportedLibraries()
			for _, l := range libs {
				add(l)
			}
		}
		ff.Close()
		return out
	}
	if f, err := macho.Open(bin); err == nil {
		libs, _ := f.ImportedLibraries()
		for _, l := range libs {
			add(l)
		}
		f.Close()
	}
	return out
}

// binStrings находит в файле читаемые строки — обычные (ASCII/UTF-8) и UTF-16 (так Windows-программы хранят пути).
func binStrings(file string, fn func(string)) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	const minLen, maxLen = 6, 300
	var a []byte     // текущая ASCII-строка
	var u [2][]byte  // UTF-16LE-строки для чётного и нечётного смещения
	var prev [2]byte // предыдущий байт в каждой позиции
	emit := func(b []byte) {
		if len(b) >= minLen && len(b) <= maxLen {
			fn(string(b))
		}
	}
	printable := func(c byte) bool { return c >= 0x20 && c < 0x7f }
	pos := 0
	for {
		c, err := r.ReadByte()
		if err != nil {
			break
		}
		if printable(c) && len(a) <= maxLen {
			a = append(a, c)
		} else {
			if printable(c) { // слишком длинная строка — не путь
				a = a[:0]
			} else {
				emit(a)
				a = a[:0]
			}
		}
		// UTF-16LE: пары (символ, 0). Позиция pos — второй байт пары, начатой в pos-1.
		k := pos & 1
		if pos > 0 {
			lo := prev[k^1]
			if c == 0 && lo >= 0x20 && lo < 0x7f {
				u[k] = append(u[k], lo)
				if len(u[k]) > maxLen {
					u[k] = u[k][:0]
				}
			} else if len(u[k]) > 0 {
				emit(u[k])
				u[k] = u[k][:0]
			}
		}
		prev[k] = c
		pos++
	}
	emit(a)
	emit(u[0])
	emit(u[1])
}

// ---------- пути из строк плагина ----------

// Первые папки, которые никогда не считаются данными плагина (системное, средства разработки, шрифты).
var depSkipPrefix = []string{"microsoft", "windows", "apple", "commonfiles", "referenceassemblies", "dotnet", "git", "python",
	"nodejs", "imageline", "flstudio", "fruityhub", "msbuild", "windowskits", "intel", "nvidia", "amd", "realtek", "google",
	"mozilla", "fonts", "colorsync", "launchagents", "launchdaemons", "receipts", "security", "developer", "updates",
	"documentation", "spotlight", "quicklook", "screensavers", "keychains", "caches", "logs", "preferences", "frameworks",
	"audio", "extensions", "privilegedhelpertools", "staging", "internet", "webserver", "printers", "modem", "perl", "ruby",
	"java", "oracle", "steam", "steinberg", "packagecache", "pace", "paceantipiracy", "ilok", "licen", "nuget", "chocolatey", "jetbrains", "visualstudio", "unity", "epicgames", "vst", "vst2", "vst3", "vstplugins", "clap", "components", "plugins", "users",
	"system", "temp", "tmp", "packages", "application", "applicationsupport", "library", "documents", "desktop", "downloads",
	"appdata", "local", "roaming", "programdata", "programfiles", "programfilesx86", "bin", "lib", "include", "src", "source",
	"build", "out", "debug", "release", "x64", "x86", "users", "home", "volumes", "usr", "opt", "var", "etc", "private"}

var codeExts = map[string]bool{".cpp": true, ".c": true, ".h": true, ".hpp": true, ".mm": true, ".m": true, ".pdb": true,
	".obj": true, ".o": true, ".cc": true, ".cxx": true, ".inl": true, ".rs": true, ".swift": true, ".lib": true, ".a": true}

func depSkipSeg(seg string) bool {
	n := normName(seg)
	if len(n) < 3 {
		return true
	}
	for _, p := range depSkipPrefix {
		if n == p || len(p) >= 5 && strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// splitDepPath разбивает строку из плагина на корень данных и путь внутри него.
// Возвращает варианты (одна строка может подходить к нескольким корням).
func splitDepPath(s string, roots []DataRoot) []depCand {
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if s == "" || strings.ContainsAny(s, "*?<>|\n\r\t") || strings.Contains(s, "://") {
		return nil
	}
	if m := envVarRe.FindStringSubmatch(s); m != nil { // %APPDATA%\Vendor
		v := os.Getenv(m[1])
		if strings.EqualFold(m[1], "ALLUSERSPROFILE") && v == "" {
			v = os.Getenv("ProgramData")
		}
		if v == "" {
			return nil
		}
		s = v + s[len(m[0]):]
	}
	if strings.HasPrefix(s, "~/") {
		home, _ := os.UserHomeDir()
		s = home + s[1:]
	}
	if fmtSpecRe.MatchString(s) {
		// «%s\Arturia\%s» — берём кусок до первой подстановки
		s = s[:fmtSpecRe.FindStringIndex(s)[0]]
	}
	norm := strings.ReplaceAll(s, `\`, "/")
	if codeExts[strings.ToLower(filepath.Ext(norm))] {
		return nil // путь к исходникам сборщика, не данные
	}
	var out []depCand
	isAbs := strings.HasPrefix(norm, "/") || len(norm) > 2 && norm[1] == ':' && norm[2] == '/'
	if isAbs { // абсолютный путь; если он не в известных корнях — пробуем его как кусок пути («\Arturia\DX7 V\»)
		ln := strings.ToLower(norm)
		best := -1
		for i, r := range roots { // самый длинный подходящий корень: /Library/Application Support раньше /Library
			rp := strings.ToLower(strings.ReplaceAll(r.Path, `\`, "/"))
			if strings.HasPrefix(ln, rp+"/") && (best < 0 || len(rp) > len(roots[best].Path)) {
				best = i
			}
		}
		if best >= 0 {
			rel := norm[len(roots[best].Path)+1:]
			return []depCand{{root: roots[best], rel: rel}}
		}
	}
	segs := splitSegs(norm)
	if len(segs) < 2 {
		return nil
	}
	for _, r := range roots {
		// кусок может начинаться с хвоста корня: «Application Support/Vendor/…», «Common Files/Vendor/…»
		rs := splitSegs(strings.ReplaceAll(r.Path, `\`, "/"))
		start := 0
		for i := 1; i <= 3 && i < len(segs) && i <= len(rs); i++ {
			if strings.EqualFold(strings.Join(segs[:i], "/"), strings.Join(rs[len(rs)-i:], "/")) {
				start = i
			}
		}
		out = append(out, depCand{root: r, rel: strings.Join(segs[start:], "/")})
	}
	return out
}

func splitSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		s = strings.TrimSpace(s)
		if s == "" || s == "." {
			continue
		}
		if s == ".." || strings.HasSuffix(s, ":") && len(s) == 2 {
			if s == ".." {
				return nil
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// resolveDepPath: существует ли такой путь у этого компьютера, и что из него переносить.
// Переносим папку второго уровня («Arturia/DX7 V») или файл; папку производителя целиком — только если
// она названа как производитель или плагин.
func resolveDepPath(c depCand, keys []string, vkey string) (depCand, bool) {
	segs := splitSegs(c.rel)
	if len(segs) == 0 || depSkipSeg(segs[0]) {
		return c, false
	}
	p := c.root.Path
	var have []string
	for _, s := range segs {
		n := filepath.Join(p, s)
		if _, err := os.Stat(n); err != nil {
			break
		}
		p = n
		have = append(have, s)
	}
	if len(have) == 0 {
		return c, false
	}
	related := false
	for _, s := range have {
		for _, k := range keys {
			if nameMatch(s, k) || len(k) >= 4 && strings.Contains(normName(s), k) {
				related = true
			}
		}
		if vendorMatch(s, vkey) {
			related = true
		}
	}
	first := filepath.Join(c.root.Path, have[0])
	switch {
	case len(have) >= 2:
		c.rel = have[0] + "/" + have[1]
		if !related && len(have) < len(segs) { // путь не дошёл до конца и ничем не связан с плагином — скорее совпадение
			return c, false
		}
		// общая папка («Arturia/Presets»), а в ней есть папка этого плагина — берём только её
		if sub, err := os.ReadDir(filepath.Join(c.root.Path, have[0], have[1])); err == nil && !anyNameMatch(have[1], keys) {
			for _, e := range sub {
				if e.IsDir() && anyNameMatch(e.Name(), keys) {
					c.rel += "/" + e.Name()
					break
				}
			}
		}
	case fileExists(first):
		if !related {
			return c, false
		}
		c.rel = have[0]
	default:
		if !related {
			return c, false
		}
		c.rel = have[0]
		c.vendor = true
	}
	return c, true
}

func anyNameMatch(s string, keys []string) bool {
	for _, k := range keys {
		if nameMatch(s, k) {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// ---------- библиотеки ----------

var vcRuntimeRe = regexp.MustCompile(`(?i)^(vcruntime|msvcp|msvcr|concrt|vccorlib|vcomp|mfc|ucrtbase)\d*`)

func isWinSystemLib(name string) bool {
	l := strings.ToLower(name)
	if strings.HasPrefix(l, "api-ms-") || strings.HasPrefix(l, "ext-ms-") {
		return true
	}
	if runtime.GOOS != "windows" {
		return knownWinSystem[l]
	}
	sys := os.Getenv("SystemRoot")
	return fileExists(filepath.Join(sys, "System32", name)) && !vcRuntimeRe.MatchString(l) || knownWinSystem[l]
}

var knownWinSystem = map[string]bool{"kernel32.dll": true, "user32.dll": true, "gdi32.dll": true, "advapi32.dll": true,
	"shell32.dll": true, "ole32.dll": true, "oleaut32.dll": true, "comdlg32.dll": true, "comctl32.dll": true, "winmm.dll": true,
	"ws2_32.dll": true, "opengl32.dll": true, "d3d11.dll": true, "d2d1.dll": true, "dwrite.dll": true, "dxgi.dll": true,
	"shlwapi.dll": true, "version.dll": true, "imm32.dll": true, "uxtheme.dll": true, "dwmapi.dll": true, "crypt32.dll": true,
	"wininet.dll": true, "winhttp.dll": true, "iphlpapi.dll": true, "setupapi.dll": true, "bcrypt.dll": true, "ntdll.dll": true,
	"gdiplus.dll": true, "msimg32.dll": true, "psapi.dll": true, "rpcrt4.dll": true, "secur32.dll": true, "netapi32.dll": true,
	"userenv.dll": true, "wtsapi32.dll": true, "dbghelp.dll": true, "mpr.dll": true, "winspool.drv": true, "glu32.dll": true,
	"d3d9.dll": true, "d3d12.dll": true, "d3dcompiler_47.dll": true, "dsound.dll": true, "avrt.dll": true, "mfplat.dll": true,
	"powrprof.dll": true, "hid.dll": true, "wintrust.dll": true, "normaliz.dll": true, "msvcrt.dll": true, "wldap32.dll": true,
	"ksuser.dll": true, "propsys.dll": true, "shcore.dll": true, "dnsapi.dll": true, "cfgmgr32.dll": true, "mswsock.dll": true,
	"oleacc.dll": true, "usp10.dll": true, "msacm32.dll": true, "winusb.dll": true, "credui.dll": true, "wevtapi.dll": true}

// winLibSearchDirs — где Windows ищет библиотеки (кроме папки FL).
func winLibSearchDirs(binDir string) []string {
	dirs := []string{binDir}
	if sys := os.Getenv("SystemRoot"); sys != "" {
		dirs = append(dirs, filepath.Join(sys, "System32"), sys)
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != "" {
			dirs = append(dirs, p)
		}
	}
	return dirs
}

func findLib(name string, dirs []string) string {
	for _, d := range dirs {
		if p := filepath.Join(d, name); fileExists(p) {
			return p
		}
	}
	return ""
}

// rootFor: в каком корне данных лежит путь (самый длинный подходящий).
func rootFor(abs string, roots []DataRoot) (DataRoot, string, bool) {
	la := strings.ToLower(filepath.Clean(abs))
	best := -1
	for i, r := range roots {
		rp := strings.ToLower(filepath.Clean(r.Path))
		if strings.HasPrefix(la, rp+string(os.PathSeparator)) && (best < 0 || len(rp) > len(roots[best].Path)) {
			best = i
		}
	}
	if best < 0 {
		return DataRoot{}, "", false
	}
	rel, _ := filepath.Rel(roots[best].Path, abs)
	return roots[best], filepath.ToSlash(rel), true
}

// ---------- сборка ----------

// pluginDeps разбирает плагин: что найдено и что из этого переносить.
// Возвращает отчёт и кандидатов в данные для findPluginData.
func pluginDeps(pluginPath string, keys []string, vkey string) (*DepReport, []depCand) {
	rep := &DepReport{Path: pluginPath, Items: []DepItem{}, Data: []PluginData{}, DataSkip: []PluginData{}, Registry: []RegValue{}}
	bin := pluginBinary(pluginPath)
	if bin == "" {
		return rep, nil
	}
	rep.Binary, rep.Arch = bin, binArch(bin)
	info := analyzeBinary(bin)
	roots := dataRoots()
	var cands []depCand
	inBundle := filepath.Clean(bin) != filepath.Clean(pluginPath)
	binDir := filepath.Dir(bin)

	// библиотеки
	libSeen := map[string]bool{}
	addLib := func(name string, imported bool) {
		l := strings.ToLower(name)
		if libSeen[l] {
			return
		}
		libSeen[l] = true
		it := DepItem{Kind: "lib", Name: name}
		if strings.HasSuffix(l, ".dll") || strings.HasSuffix(l, ".drv") {
			switch {
			case vcRuntimeRe.MatchString(l):
				it.Status, it.Note = "runtime", "Microsoft Visual C++ — у получателя проверю при установке"
			case isWinSystemLib(name):
				it.Status = "system"
			default:
				p := findLib(name, winLibSearchDirs(binDir))
				switch {
				case p == "":
					if !imported {
						return // имя в строках, а файла нет — скорее всего, необязательная библиотека
					}
					it.Status, it.Note = "missing", "не нашёл и у тебя — возможно, грузится из папки FL"
				case strings.EqualFold(filepath.Dir(p), binDir) && inBundle:
					it.Path, it.Status = p, "inside"
				default:
					it.Path, it.Status = p, "take"
					it.Note = "положу рядом с плагином"
					cands = append(cands, depCand{root: DataRoot{Key: sideRoot}, rel: filepath.Base(p), abs: p, why: "библиотека, которую загружает плагин"})
				}
			}
			if it.Status == "system" && !imported {
				return
			}
			rep.Items = append(rep.Items, it)
			return
		}
		// Mac: /usr/lib, /System — системные; @rpath/@loader_path — внутри пакета; остальное ищем
		switch {
		case strings.HasPrefix(name, "/usr/lib/") || strings.HasPrefix(name, "/System/"):
			if !imported {
				return
			}
			it.Status = "system"
		case strings.HasPrefix(name, "@executable_path"):
			it.Status, it.Note = "system", "библиотека FL Studio"
		case strings.HasPrefix(name, "@"):
			rel := name[strings.Index(name, "/")+1:]
			found := ""
			for _, d := range []string{binDir, filepath.Join(binDir, "..", "Frameworks"), filepath.Join(binDir, "..", "Resources")} {
				if p := filepath.Join(d, rel); exists(p) {
					found = filepath.Clean(p)
					break
				}
			}
			if found == "" {
				it.Status, it.Note = "missing", "не нашёл внутри пакета плагина"
			} else {
				it.Path, it.Status = found, "inside"
			}
		case strings.HasPrefix(name, "/"):
			unit := name
			if i := strings.Index(name, ".framework/"); i >= 0 {
				unit = name[:i+len(".framework")]
			}
			if !exists(unit) {
				if !imported {
					return
				}
				it.Status, it.Note = "missing", "нет и у тебя"
				break
			}
			it.Path = unit
			if r, rel, ok := rootFor(unit, roots); ok {
				it.Status = "take"
				cands = append(cands, depCand{root: r, rel: rel, why: "библиотека, которую загружает плагин"})
			} else {
				it.Status, it.Note = "skip", "лежит вне известных папок — скопируй вручную, если у друга плагин не загрузится"
			}
		default:
			if !imported {
				return
			}
			it.Status = "missing"
		}
		rep.Items = append(rep.Items, it)
	}
	for _, l := range info.libs {
		addLib(l, true)
	}
	for _, l := range info.dynLibs {
		addLib(l, false)
	}

	// пути
	pathSeen := map[string]bool{}
	for _, s := range info.paths {
		for _, c := range splitDepPath(s, roots) {
			rc, ok := resolveDepPath(c, keys, vkey)
			if !ok {
				continue
			}
			id := rc.root.Key + "/" + strings.ToLower(rc.rel)
			full := filepath.Join(rc.root.Path, filepath.FromSlash(rc.rel))
			if pathSeen[id] || strings.HasPrefix(strings.ToLower(filepath.Clean(pluginPath)), strings.ToLower(full)) {
				continue
			}
			pathSeen[id] = true
			rc.why = "плагин ссылается на эту папку"
			cands = append(cands, rc)
			rep.Items = append(rep.Items, DepItem{Kind: "path", Name: s, Path: full, Status: "take"})
			break
		}
	}

	// реестр (только Windows)
	if runtime.GOOS == "windows" {
		vals := regCollect(regRelatedKeys(info.regKeys, keys, vkey), keys, vkey)
		for _, v := range vals {
			own := regForPlugin(v, keys)
			// переносим значения ключа самого плагина (SOFTWARE\Arturia\Jup-8 V4) и общие настройки производителя
			// (SOFTWARE\Arturia), но не ключи других его продуктов
			if !own && !regVendorRoot(v.Key) {
				continue
			}
			rep.Registry = append(rep.Registry, v)
			rep.Items = append(rep.Items, DepItem{Kind: "registry", Name: v.Hive + `\` + v.Key + ` → ` + v.Name, Path: v.Data, Status: "take"})
			// путь из ключа самого плагина — тоже его данные (общие пути производителя — нет: там всё подряд)
			if !own {
				continue
			}
			if r, rel, ok := rootFor(expandEnvPrefix(v.Data), roots); ok {
				if rc, ok := resolveDepPath(depCand{root: r, rel: rel}, keys, vkey); ok {
					rc.why = "путь из реестра плагина"
					cands = append(cands, rc)
				}
			}
		}
	}
	sort.SliceStable(rep.Items, func(i, j int) bool { return depOrder[rep.Items[i].Status] < depOrder[rep.Items[j].Status] })
	return rep, cands
}

var depOrder = map[string]int{"missing": 0, "take": 1, "runtime": 2, "skip": 3, "inside": 4, "system": 5}

func expandEnvPrefix(s string) string {
	if m := envVarRe.FindStringSubmatch(s); m != nil {
		if v := os.Getenv(m[1]); v != "" {
			return v + s[len(m[0]):]
		}
	}
	return s
}

// regRelatedKeys — ключи из строк плагина, в которых есть имя производителя или плагина.
func regRelatedKeys(found []string, keys []string, vkey string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range found {
		if len(out) >= 30 || seen[strings.ToLower(k)] {
			continue
		}
		related := false
		for _, s := range strings.Split(k, `\`)[1:] {
			if vendorMatch(s, vkey) || anyNameMatch(s, keys) {
				related = true
			}
		}
		if related {
			seen[strings.ToLower(k)] = true
			out = append(out, k)
		}
	}
	return out
}

// regLicenseName — значения реестра с лицензией/активацией не переносим.
func regLicenseName(n string) bool {
	l := strings.ToLower(n)
	for _, w := range []string{"licen", "serial", "auth", "activ", "token", "passw", "hwid", "unlock"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// regPathValue — значение похоже на путь к папке или файлу.
var regPathRe = regexp.MustCompile(`^(?:[A-Za-z]:\\|%\w+%\\|\\\\)`)

var vcYear = map[string]string{"71": "2003", "80": "2005", "90": "2008", "100": "2010", "110": "2012", "120": "2013"}
var vcVerRe = regexp.MustCompile(`(?i)^(?:msvcp|msvcr|mfc)(\d+)u?\.dll$`)

// missingLibWarnings — у получателя после установки: каких библиотек плагину не хватает на этом компьютере.
func missingLibWarnings(pluginPath string) []Warning {
	bin := pluginBinary(pluginPath)
	if bin == "" {
		return nil
	}
	binDir := filepath.Dir(bin)
	var out []Warning
	var miss []string
	for _, l := range binImports(bin) {
		low := strings.ToLower(l)
		switch {
		case strings.HasSuffix(low, ".dll") || strings.HasSuffix(low, ".drv"):
			if runtime.GOOS != "windows" || strings.HasPrefix(low, "api-ms-") || strings.HasPrefix(low, "ext-ms-") {
				continue
			}
			if findLib(l, winLibSearchDirs(binDir)) != "" {
				continue
			}
			if m := vcVerRe.FindStringSubmatch(low); m != nil && vcYear[m[1]] != "" {
				out = append(out, Warning{Text: "Плагину нужен Microsoft Visual C++ " + vcYear[m[1]] + " Redistributable (" + l + ") — его нет на этом компьютере. Скачай с сайта Microsoft («Visual C++ " + vcYear[m[1]] + " Redistributable x64») и установи."})
				continue
			}
			if vcRuntimeRe.MatchString(low) {
				continue // 2015–2022 проверяется отдельно (кнопка «Установить Visual C++»)
			}
			miss = append(miss, l)
		case runtime.GOOS == "darwin" && strings.HasPrefix(l, "/") && !strings.HasPrefix(l, "/usr/lib/") && !strings.HasPrefix(l, "/System/"):
			if !exists(l) {
				miss = append(miss, l)
			}
		case runtime.GOOS == "darwin" && (strings.HasPrefix(l, "@loader_path/") || strings.HasPrefix(l, "@rpath/")):
			rel := l[strings.Index(l, "/")+1:]
			ok := false
			for _, d := range []string{binDir, filepath.Join(binDir, "..", "Frameworks"), filepath.Join(binDir, "..", "Resources")} {
				if exists(filepath.Join(d, rel)) {
					ok = true
				}
			}
			if !ok && strings.HasPrefix(l, "@loader_path/") {
				miss = append(miss, l)
			}
		}
	}
	if len(miss) > 0 {
		out = append(out, Warning{Text: "Плагину не хватает библиотек: " + strings.Join(miss, ", ") + ". Обычно их ставит установщик плагина (или они идут вместе с другой программой производителя) — без них плагин не загрузится. Попроси у друга установщик или скачай его у производителя."})
	}
	return out
}

// PluginDeps — отчёт «Зависимости» для установленного плагина.
func PluginDeps(cfg *Config, name, format string) (*DepReport, error) {
	ip := FindInstalled(cfg, name, format)
	if ip == nil {
		ScanInstalled(cfg, true)
		ip = FindInstalled(cfg, name, format)
	}
	if ip == nil || ip.Path == "" {
		return nil, errors.New("не нашёл у тебя файл этого плагина")
	}
	_, _, rep := findPluginDataEx(ip.Name, ip.Vendor, ip.Path)
	rep.Plugin = ip.Name
	if rep.Binary == "" {
		return rep, errors.New("не нашёл исполняемый файл внутри плагина " + ip.Path)
	}
	return rep, nil
}

// regForPlugin: значение относится к этому плагину — в ключе или в пути есть его имя.
func regForPlugin(v RegValue, keys []string) bool {
	for _, part := range [][]string{strings.Split(v.Key, `\`), strings.FieldsFunc(v.Data, func(r rune) bool { return r == '\\' || r == '/' })} {
		for _, seg := range part {
			if anyNameMatch(seg, keys) {
				return true
			}
		}
	}
	return false
}

// regVendorRoot: SOFTWARE\<Производитель> — сам ключ производителя, без подключей.
func regVendorRoot(key string) bool {
	parts := strings.Split(strings.Trim(key, `\`), `\`)
	return len(parts) == 2 || len(parts) == 3 && strings.EqualFold(parts[1], "WOW6432Node")
}
