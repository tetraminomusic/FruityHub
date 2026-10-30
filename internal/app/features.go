package app

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"flsync/internal/flp"
)

// ---------- свой фон ----------

var bgExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// customBackground — картинка, которую пользователь поставил фоном (лежит в папке настроек).
func customBackground() string {
	for _, e := range bgExts {
		p := filepath.Join(ConfigDir(), "background"+e)
		if exists(p) {
			return p
		}
	}
	return ""
}

func SetBackground(src string) error {
	ext := strings.ToLower(filepath.Ext(src))
	ok := false
	for _, e := range bgExts {
		ok = ok || e == ext
	}
	if !ok {
		return errors.New("нужна картинка jpg, png или webp")
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st.Size() > 40<<20 {
		return errors.New("картинка больше 40 МБ — возьми поменьше")
	}
	ResetBackground()
	// большая фотография (6000×4000) в памяти окна занимает ~100 МБ — уменьшаем до размера экрана
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		if err := shrinkImage(src, filepath.Join(ConfigDir(), "background.jpg"), 2560); err == nil {
			return nil
		}
	}
	return copyFile(src, filepath.Join(ConfigDir(), "background"+ext))
}

// shrinkImage сохраняет картинку в JPEG не шире maxW (по большей стороне). Маленькую — просто перекодирует.
func shrinkImage(src, dst string, maxW int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxW && strings.EqualFold(filepath.Ext(src), ".jpg") {
		return copyFile(src, dst)
	}
	k := float64(maxW) / float64(max(w, h))
	if k > 1 {
		k = 1
	}
	nw, nh := max(1, int(float64(w)*k)), max(1, int(float64(h)*k))
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// усреднение по квадрату исходных пикселей — без «лесенок»
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+max((y+1)*h/nh, y*h/nh+1)
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+max((x+1)*w/nw, x*w/nw+1)
			var r, g, bl, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, _ := img.At(xx, yy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			out.Pix[(y*nw+x)*4], out.Pix[(y*nw+x)*4+1], out.Pix[(y*nw+x)*4+2], out.Pix[(y*nw+x)*4+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), 255
		}
	}
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer o.Close()
	return jpeg.Encode(o, out, &jpeg.Options{Quality: 88})
}

func ResetBackground() {
	for _, e := range bgExts {
		os.Remove(filepath.Join(ConfigDir(), "background"+e))
	}
}

// ---------- главный .flp, запуск, название в Project info ----------

// MainFLP — .flp этого проекта в корне папки (если их несколько — главный).
func (p *Project) MainFLP() string {
	var root []string
	for _, rel := range p.rootFLPs() {
		if !isCopyName(rel) {
			root = append(root, rel)
		}
	}
	switch len(root) {
	case 0:
		return ""
	case 1:
		return root[0]
	}
	return p.MainFLPGuess(root)
}

// Launch открывает проект в FL Studio (программой, которая открывает .flp на этом компьютере).
func (p *Project) Launch() (string, error) {
	rel := p.MainFLP()
	if rel == "" {
		return "", errors.New("в папке проекта нет .flp")
	}
	path := filepath.Join(p.Dir, filepath.FromSlash(rel))
	return rel, OpenFile(path)
}

// FLTitle — название в Project info главного .flp.
func (p *Project) FLTitle() (string, error) {
	rel := p.MainFLP()
	if rel == "" {
		return "", errors.New("в папке проекта нет .flp")
	}
	f, err := flp.ParseFile(filepath.Join(p.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	return f.Title(), nil
}

// FLArtist — артист (Author) в Project info главного .flp.
func (p *Project) FLArtist() string {
	rel := p.MainFLP()
	if rel == "" {
		return ""
	}
	info, err := parseFLPMetadataCached(filepath.Join(p.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return info.Artist
}

// SetFLArtist пишет артиста в Project info главного .flp (с резервной копией).
func (p *Project) SetFLArtist(artist string) (bool, error) {
	return p.editMainFLP("артист", func(f *flp.File) bool { return f.SetArtist(toLatin(strings.TrimSpace(artist))) })
}

func (p *Project) editMainFLP(what string, fn func(f *flp.File) bool) (bool, error) {
	rel := p.MainFLP()
	if rel == "" {
		return false, errors.New("в папке проекта нет .flp")
	}
	abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
	raw, err := os.ReadFile(abs)
	if err != nil {
		return false, err
	}
	f, err := flp.Parse(raw)
	if err != nil {
		return false, err
	}
	if !fn(f) {
		return false, nil
	}
	bdir := filepath.Join(p.Dir, ".flsync-local", "backup")
	os.MkdirAll(bdir, 0o755)
	os.WriteFile(filepath.Join(bdir, strings.ReplaceAll(rel, "/", "_")+"."+time.Now().Format("20060102-150405")+".flp"), raw, 0o644)
	if err := os.WriteFile(abs, f.Bytes(), 0o644); err != nil {
		return false, err
	}
	DevLog("info", 0, false, "Project info (%s) в %s изменён", what, rel)
	return true, nil
}

// SetFLTitle пишет название в Project info главного .flp (с резервной копией в .flsync-local/backup).
func (p *Project) SetFLTitle(title string) (bool, error) {
	rel := p.MainFLP()
	if rel == "" {
		return false, errors.New("в папке проекта нет .flp")
	}
	abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
	raw, err := os.ReadFile(abs)
	if err != nil {
		return false, err
	}
	f, err := flp.Parse(raw)
	if err != nil {
		return false, err
	}
	if !f.SetTitle(toLatin(strings.TrimSpace(title))) { // FL не показывает кириллицу — пишем латиницей
		return false, nil
	}
	bdir := filepath.Join(p.Dir, ".flsync-local", "backup")
	os.MkdirAll(bdir, 0o755)
	os.WriteFile(filepath.Join(bdir, strings.ReplaceAll(rel, "/", "_")+"."+time.Now().Format("20060102-150405")+".flp"), raw, 0o644)
	st, _ := os.Stat(abs)
	if err := os.WriteFile(abs, f.Bytes(), 0o644); err != nil {
		return false, err
	}
	if st != nil {
		os.Chtimes(abs, time.Now(), time.Now())
	}
	DevLog("info", 0, false, "название в Project info %s → «%s»", rel, title)
	return true, nil
}

// ---------- удаление проекта ----------

// TrashProject убирает папку проекта в Корзину целиком (можно достать обратно) и из списка.
func (s *Server) TrashProject(path string) error {
	if !exists(path) {
		return errors.New("папка не найдена")
	}
	home, _ := os.UserHomeDir()
	clean := filepath.Clean(path)
	if clean == filepath.Clean(home) || len(clean) < 4 || filepath.Dir(clean) == clean {
		return errors.New("эту папку удалять нельзя")
	}
	if err := moveDirToTrash(clean); err != nil {
		return fmt.Errorf("не получилось убрать в корзину (закрой проект в FL Studio): %w", err)
	}
	var keep []LocalProject
	for _, lp := range s.cfg.Projects {
		if !samePath(lp.Path, path) {
			keep = append(keep, lp)
		}
	}
	s.cfg.Projects = keep
	DevLog("info", 0, false, "проект удалён в корзину: %s", path)
	return s.cfg.Save()
}

// ---------- версия FL Studio ----------

var (
	flVerMu sync.Mutex
	flVerAt time.Time
)

var plistVerRe = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)

// FLApp — установленная FL Studio: версия и программа.
type FLApp struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Name    string `json:"name"`
}

var flAppList []FLApp

// InstalledFLVersions — версии FL Studio, установленные на этом компьютере (новые первыми).
func InstalledFLVersions() []string {
	var out []string
	for _, a := range InstalledFLApps() {
		out = append(out, a.Version)
	}
	return out
}

// InstalledFLApps — установленные FL Studio (новые первыми).
func InstalledFLApps() []FLApp {
	flVerMu.Lock()
	defer flVerMu.Unlock()
	if !flVerAt.IsZero() && time.Since(flVerAt) < 10*time.Minute {
		return flAppList
	}
	var out []FLApp
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			ms, _ := filepath.Glob(filepath.Join(root, "FL Studio*.app"))
			for _, m := range ms {
				if b, err := os.ReadFile(filepath.Join(m, "Contents", "Info.plist")); err == nil {
					if v := plistVerRe.FindSubmatch(b); v != nil {
						out = append(out, FLApp{Version: strings.TrimSpace(string(v[1])), Path: m, Name: strings.TrimSuffix(filepath.Base(m), ".app")})
					}
				}
			}
		}
	case "windows":
		var exes []string
		for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if pf == "" {
				continue
			}
			for _, pat := range []string{"FL Studio*\\FL64.exe", "FL Studio*\\FL.exe"} {
				ms, _ := filepath.Glob(filepath.Join(pf, "Image-Line", pat))
				exes = append(exes, ms...)
			}
		}
		if len(exes) > 0 {
			var q []string
			for _, e := range exes {
				q = append(q, "'"+strings.ReplaceAll(e, "'", "''")+"'")
			}
			cmd := exec.Command("powershell", "-NoProfile", "-Command", "@("+strings.Join(q, ",")+") | ForEach-Object { (Get-Item -LiteralPath $_).VersionInfo.FileVersion }")
			hideWindow(cmd)
			if b, err := cmd.Output(); err == nil {
				i := 0
				for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
					if l = strings.TrimSpace(l); l != "" && i < len(exes) {
						out = append(out, FLApp{Version: strings.ReplaceAll(l, ",", "."), Path: exes[i], Name: filepath.Base(filepath.Dir(exes[i])) + " (" + filepath.Base(exes[i]) + ")"})
					}
					i++
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return newerVersion(out[i].Version, out[j].Version) })
	flAppList, flVerAt = out, time.Now()
	return out
}

// LaunchWith открывает главный .flp в выбранной FL Studio (путь из InstalledFLApps).
func (p *Project) LaunchWith(app string) (string, error) {
	ok := false
	for _, a := range InstalledFLApps() {
		if a.Path == app {
			ok = true
		}
	}
	if !ok {
		return "", errors.New("такой FL Studio на этом компьютере нет")
	}
	rel := p.MainFLP()
	if rel == "" {
		return "", errors.New("в папке проекта нет .flp")
	}
	path := filepath.Join(p.Dir, filepath.FromSlash(rel))
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", "-a", app, path)
	} else {
		cmd = exec.Command(app, path)
		cmd.Dir = filepath.Dir(app)
	}
	return rel, cmd.Start()
}

func majorMinor(v string) string {
	p := strings.Split(v, ".")
	if len(p) >= 2 {
		return p[0] + "." + p[1]
	}
	return v
}

// FLCompat — сохранён ли проект в более новой FL Studio, чем установлена здесь.
type FLCompat struct {
	Saved     string `json:"saved"`     // в какой версии сохранён главный .flp
	Installed string `json:"installed"` // самая новая установленная здесь
	TooOld    bool   `json:"tooOld"`
	Text      string `json:"text,omitempty"`
}

func (p *Project) FLCompat() FLCompat {
	var c FLCompat
	rel := p.MainFLP()
	if rel == "" {
		return c
	}
	if info, err := parseFLPMetadataCached(filepath.Join(p.Dir, filepath.FromSlash(rel))); err == nil {
		c.Saved = info.FLVersion
	}
	if vs := InstalledFLVersions(); len(vs) > 0 {
		c.Installed = vs[0]
	}
	if c.Saved != "" && c.Installed != "" && newerVersion(majorMinor(c.Saved), majorMinor(c.Installed)) {
		c.TooOld = true
		c.Text = fmt.Sprintf("Проект сохранён в FL Studio %s, а у тебя %s — старая версия может открыть его с ошибками или не открыть вовсе. Обнови FL Studio.", majorMinor(c.Saved), majorMinor(c.Installed))
	}
	return c
}

// ---------- перетаскивание папки в окно ----------

// Браузерное окно не сообщает, откуда перетащили папку, — только её имя и что в ней лежит. Поэтому
// приложение ищет на диске папку с таким именем и таким содержимым (недолго и не везде).

type DroppedFolder struct {
	Name     string   `json:"name"`
	Children []string `json:"children"` // имена файлов и папок внутри (сколько удалось прочитать)
	Plugins  bool     `json:"plugins"`  // ищем папку с плагинами (тогда смотрим и в системных местах)
}

var skipSearch = map[string]bool{"node_modules": true, ".git": true, "library": true, "appdata": true, "windows": true,
	"$recycle.bin": true, "system volume information": true, ".trash": true, "programdata": true, "proc": true, "sys": true,
	".cache": true, ".npm": true, ".cargo": true, ".rustup": true, "go": true, ".local": true}

func (s *Server) LocateDropped(d DroppedFolder) []string {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return nil
	}
	want := map[string]bool{}
	for _, c := range d.Children {
		want[strings.ToLower(c)] = true
	}
	home, _ := os.UserHomeDir()
	var roots []string
	add := func(p string) {
		if p != "" && dirExists(p) {
			roots = append(roots, p)
		}
	}
	add(s.cfg.LastParent)
	for _, lp := range s.cfg.Projects {
		add(filepath.Dir(lp.Path))
	}
	if d.Plugins {
		for _, f := range []string{"VST3", "VST2", "VST", "CLAP", "AU"} {
			for _, p := range standardPluginDirs(f) {
				add(filepath.Dir(p))
			}
		}
		for _, p := range ScannedFolders(s.cfg) {
			add(filepath.Dir(p.Path))
		}
		if runtime.GOOS == "windows" {
			add(os.Getenv("ProgramFiles"))
			add(os.Getenv("ProgramFiles(x86)"))
			add(os.Getenv("CommonProgramFiles"))
		}
		if runtime.GOOS == "darwin" {
			add("/Library/Audio/Plug-Ins")
			add(filepath.Join(home, "Library", "Audio", "Plug-Ins"))
		}
	}
	for _, sub := range []string{"Desktop", "Documents", "Music", "Downloads", "OneDrive"} {
		add(filepath.Join(home, sub))
	}
	add(documentsDir())
	add(home)
	if runtime.GOOS == "windows" {
		for c := 'C'; c <= 'Z'; c++ {
			add(string(c) + `:\`)
		}
	}
	if runtime.GOOS == "darwin" {
		if vs, err := os.ReadDir("/Volumes"); err == nil {
			for _, v := range vs {
				add(filepath.Join("/Volumes", v.Name()))
			}
		}
	}

	deadline := time.Now().Add(6 * time.Second)
	seen := map[string]bool{}
	var found []string
	score := func(dir string) bool {
		if len(want) == 0 {
			return true
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		have := 0
		for _, e := range ents {
			if want[strings.ToLower(e.Name())] {
				have++
			}
		}
		return have*10 >= len(want)*8 // 80% совпадений по содержимому
	}
	type item struct {
		dir   string
		depth int
	}
	for _, r := range roots {
		queue := []item{{r, 0}}
		for len(queue) > 0 && time.Now().Before(deadline) {
			it := queue[0]
			queue = queue[1:]
			key := strings.ToLower(filepath.Clean(it.dir))
			if seen[key] {
				continue
			}
			seen[key] = true
			if strings.EqualFold(filepath.Base(it.dir), name) && score(it.dir) {
				found = append(found, it.dir)
				if len(found) >= 5 {
					return found
				}
				continue
			}
			if it.depth >= 5 {
				continue
			}
			ents, err := os.ReadDir(it.dir)
			if err != nil {
				continue
			}
			for _, e := range ents {
				n := e.Name()
				if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || (strings.HasPrefix(n, ".") && n != ".vst3") {
					continue
				}
				if skipSearch[strings.ToLower(n)] && !strings.EqualFold(n, name) {
					continue
				}
				queue = append(queue, item{filepath.Join(it.dir, n), it.depth + 1})
			}
		}
	}
	return found
}

// ---------- мелочи ----------

func atoiDef(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// projectModified — когда проект последний раз меняли: самое свежее время .flp в корне папки
// (FL пишет .flp при каждом сохранении, «Получить» тоже обновляет его). Нет .flp — время самой папки.
func projectModified(dir string) int64 { m, _ := projectMeta(dir); return m }

// projectArtist — артист (Author в Project info) самого свежего .flp в корне проекта.
func projectArtist(dir string) string { _, a := projectMeta(dir); return a }

func projectMeta(dir string) (int64, string) {
	var best time.Time
	bestPath := ""
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, ""
	}
	for _, e := range ents {
		n := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasSuffix(n, ".flp") || strings.Contains(n, "autosave") || strings.Contains(n, "overwritten") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().After(best) {
			best, bestPath = fi.ModTime(), filepath.Join(dir, e.Name())
		}
	}
	if best.IsZero() {
		if fi, err := os.Stat(dir); err == nil {
			best = fi.ModTime()
		}
	}
	// артист как его ввели (кириллицей) — из метаданных трека; в .flp он латиницей, FL кириллицу не понимает
	var m TrackMeta
	readJSON(metaPath(dir), &m)
	artist := strings.TrimSpace(m.Artist)
	if artist == "" && bestPath != "" {
		if info, err := parseFLPMetadataCached(bestPath); err == nil {
			artist = info.Artist
		}
	}
	return best.Unix(), artist
}
