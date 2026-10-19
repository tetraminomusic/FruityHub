package app

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Plugin transfer: the requester asks (through our server), the owner allows
// it (or has allowed it in advance in the settings). When both computers are
// online, the owner's app zips the plugin and streams it through the server
// straight into the requester's app — nothing is stored on the server. The
// requester's app puts the plugin into the folder where its plugins of that
// format live and adds it to FL Studio's plugin database.

// Candidate is a file the owner could send instead of the plugin itself.
type Candidate struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Inspection tells the owner what can be sent for a request.
type Inspection struct {
	Found      bool        `json:"found"`
	Path       string      `json:"path,omitempty"`
	Format     string      `json:"format,omitempty"`
	Version    string      `json:"version,omitempty"`
	Size       int64       `json:"size,omitempty"`
	Compatible bool        `json:"compatible"`
	Reason     string      `json:"reason"`
	Installers []Candidate `json:"installers"`
	SameOS     bool        `json:"sameOs"`
}

// bundleHasPlatform: VST3/CLAP bundles may carry binaries for several systems.
func bundleHasPlatform(path, targetOS string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return false
	}
	switch targetOS {
	case "macOS":
		return exists(filepath.Join(path, "Contents", "MacOS"))
	case "Windows":
		for _, d := range []string{"x86_64-win", "arm64x-win", "arm64-win", "x86-win"} {
			if exists(filepath.Join(path, "Contents", d)) {
				return true
			}
		}
	}
	return false
}

var installerExts = map[string][]string{"Windows": {".exe", ".msi", ".zip"}, "macOS": {".pkg", ".dmg", ".zip"}}

// findInstallers looks in Downloads/Desktop for installer files of the plugin.
func findInstallers(plugin, vendor, targetOS string) []Candidate {
	key := normName(plugin)
	if len(key) < 3 {
		return nil
	}
	exts := installerExts[targetOS]
	home, _ := os.UserHomeDir()
	var out []Candidate
	for _, root := range []string{filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop")} {
		seen := 0
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if seen++; seen > 20000 {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if rel, _ := filepath.Rel(root, path); strings.Count(rel, string(os.PathSeparator)) >= 3 || strings.HasSuffix(strings.ToLower(path), ".app") {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			ok := false
			for _, e := range exts {
				ok = ok || e == ext
			}
			if !ok || !strings.Contains(normName(strings.TrimSuffix(d.Name(), ext)), key) {
				return nil
			}
			info, _ := d.Info()
			c := Candidate{Path: path, Name: d.Name()}
			if info != nil {
				c.Size = info.Size()
			}
			out = append(out, c)
			return nil
		})
	}
	return out
}

func dirSize(p string) int64 {
	var n int64
	filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if i, e := d.Info(); e == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

// InspectRequest: can I send this plugin to the requester, and how?
func InspectRequest(cfg *Config, r *PluginRequest) *Inspection {
	in := &Inspection{SameOS: r.FromOS == OSName()}
	ip := FindInstalled(cfg, r.Plugin, r.Format)
	if ip == nil {
		ScanInstalled(cfg, true)
		ip = FindInstalled(cfg, r.Plugin, r.Format)
	}
	if ip != nil {
		in.Found, in.Path, in.Format, in.Version, in.Size = true, ip.Path, ip.Format, ip.Version, dirSize(ip.Path)
	}
	switch {
	case !in.Found:
		in.Reason = "Не нашёл у тебя файл этого плагина."
	case in.SameOS:
		in.Compatible = true
		in.Reason = "У вас одинаковая система — плагин встанет сразу."
	case ip.Format == "AU":
		in.Reason = "Это Audio Unit — такой формат есть только на Mac."
	case bundleHasPlatform(ip.Path, r.FromOS):
		in.Compatible = true
		in.Reason = "В твоём пакете плагина есть версия и для " + r.FromOS + " — можно отправить как есть."
	default:
		in.Reason = fmt.Sprintf("Твой файл плагина собран только под %s, у %s — %s. Отправь установщик для %s (можно скачать у производителя).", OSName(), r.FromNick, r.FromOS, r.FromOS)
	}
	in.Installers = findInstallers(r.Plugin, r.Vendor, r.FromOS)
	return in
}

func (cfg *Config) myRequest(id string, owner bool) (*PluginRequest, error) {
	r, err := cfg.Hub().Request(id)
	if err != nil {
		return nil, err
	}
	if owner && r.To != cfg.UserID {
		return nil, errors.New("это запрос не к тебе")
	}
	if !owner && r.From != cfg.UserID {
		return nil, errors.New("это не твой запрос")
	}
	return r, nil
}

// ApproveRequest: the owner allowed it. file == "" sends the installed plugin
// itself; otherwise that file (an installer) is sent. The transfer itself
// starts when the requester is online (see SendApproved).
func ApproveRequest(cfg *Config, job *Job, id, file string) error {
	r, err := cfg.myRequest(id, true)
	if err != nil {
		return err
	}
	if r.Status != ReqPending && r.Status != ReqApproved {
		return fmt.Errorf("запрос уже обработан (%s)", r.Status)
	}
	h := cfg.Hub()
	if file != "" {
		st, err := os.Stat(file)
		if err != nil || st.IsDir() {
			return errors.New("файл установщика не найден")
		}
		if err := h.Approve(r.ID, "installer", filepath.Base(file), "установщик", r.Format); err != nil {
			return err
		}
		outboxPut(r.ID, outboxItem{Kind: "installer", File: file})
		job.Logf("Разрешено: установщик %s уйдёт к %s, как только он будет в сети", filepath.Base(file), r.FromNick)
		return nil
	}
	in := InspectRequest(cfg, r)
	if !in.Compatible {
		return errors.New(in.Reason)
	}
	note := ""
	if in.Version != "" {
		note = "версия " + in.Version
	}
	if err := h.Approve(r.ID, "plugin", filepath.Base(in.Path), note, in.Format); err != nil {
		return err
	}
	outboxPut(r.ID, outboxItem{Kind: "plugin", File: in.Path})
	job.Logf("Разрешено: %s уйдёт к %s, как только он будет в сети", r.Plugin, r.FromNick)
	return nil
}

// SendApproved streams an allowed plugin (or installer) to the requester.
func SendApproved(cfg *Config, job *Job, r *PluginRequest) error {
	it, ok := outboxGet(r.ID)
	if !ok { // e.g. allowed before the app was restarted with a cleared outbox
		if r.Kind == "installer" {
			return errors.New("не помню, какой установщик отправлять — ответь на запрос ещё раз")
		}
		ip := FindInstalled(cfg, r.Plugin, r.Format)
		if ip == nil {
			return errors.New("не нашёл у себя этот плагин")
		}
		it = outboxItem{Kind: "plugin", File: ip.Path}
	}
	path := it.File
	if it.Kind == "plugin" {
		job.Logf("Упаковываю %s…", r.Plugin)
		tmp := filepath.Join(os.TempDir(), "uzbek-plugin-"+r.ID+".zip")
		defer os.Remove(tmp)
		var extra map[string][]byte
		if r.FromOS == OSName() { // FL's entries for the plugin only make sense on the same system
			extra = collectFLDBEntries(it.File)
		}
		if err := zipPath(it.File, tmp, extra); err != nil {
			return err
		}
		path = tmp
	}
	job.Logf("Жду, пока %s примет…", r.FromNick)
	if err := cfg.Hub().StreamSend(job, r.ID, path); err != nil {
		return err
	}
	outboxDelete(r.ID)
	job.Logf("✓ %s передан %s", r.Plugin, r.FromNick)
	LogActivity("", "ok", fmt.Sprintf("🔌 %s передан %s", r.Plugin, r.FromNick))
	return nil
}

// ReceiveRequest takes an allowed plugin while the owner's app sends it and
// installs it. For an installer it returns the downloaded file.
func ReceiveRequest(cfg *Config, job *Job, r *PluginRequest) (string, error) {
	if r.From != cfg.UserID {
		return "", errors.New("это не твой запрос")
	}
	h := cfg.Hub()
	if r.Kind == "installer" {
		home, _ := os.UserHomeDir()
		dst, _, _ := uniqueTarget(filepath.Join(home, "Downloads"), r.FileName, "")
		job.Logf("Получаю установщик %s от %s…", r.FileName, r.ToNick)
		if err := h.StreamRecv(job, r, dst); err != nil {
			return "", err
		}
		h.DoneRequest(r.ID, true, "установщик скачан")
		LogActivity("", "ok", fmt.Sprintf("🔌 Получен установщик %s от %s — лежит в «Загрузках»", r.Plugin, r.ToNick))
		job.Logf("✓ Установщик сохранён в «Загрузки»")
		return dst, nil
	}
	tmp := filepath.Join(os.TempDir(), "uzbek-plugin-"+r.ID+".zip")
	defer os.Remove(tmp)
	job.Logf("Получаю %s от %s…", r.Plugin, r.ToNick)
	if err := h.StreamRecv(job, r, tmp); err != nil {
		return "", err
	}
	dst, err := InstallPluginZip(cfg, job, tmp, r.Format)
	if err != nil {
		h.DoneRequest(r.ID, false, err.Error())
		return "", err
	}
	h.DoneRequest(r.ID, true, "")
	LogActivity("", "ok", fmt.Sprintf("🔌 Установлен %s от %s → %s", r.Plugin, r.ToNick, dst))
	return dst, nil
}

// InstallPluginZip unpacks a received plugin into the right folder for its
// format and registers it in FL Studio's plugin database.
func InstallPluginZip(cfg *Config, job *Job, zipFile, format string) (string, error) {
	stage, err := os.MkdirTemp("", "uzbek-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	top, fldb, err := unzipTo(zipFile, stage)
	if err != nil {
		return "", err
	}
	if format == "" {
		format = pluginExts[strings.ToLower(filepath.Ext(top))]
	}
	dir, admin := pluginTargetDir(cfg, format)
	if dir == "" {
		return "", errors.New("не нашёл папку для плагинов " + format)
	}
	dst := filepath.Join(dir, top)
	if admin {
		job.Logf("Папка %s защищена — Windows попросит разрешение", dir)
		if err := elevatedReplace(filepath.Join(stage, top), dst); err != nil {
			return "", err
		}
	} else {
		if exists(dst) {
			bak := filepath.Join(ConfigDir(), "plugin-backups", time.Now().Format("20060102-150405"))
			os.MkdirAll(bak, 0o755)
			if err := moveAll(dst, filepath.Join(bak, top)); err != nil {
				return "", fmt.Errorf("не могу заменить существующий %s (закрой FL Studio): %w", top, err)
			}
			job.Logf("Старая версия перенесена в %s", bak)
		}
		if err := moveAll(filepath.Join(stage, top), dst); err != nil {
			return "", err
		}
	}
	if runtime.GOOS == "darwin" && format == "AU" {
		killAUCache() // make macOS re-read the Audio Unit cache
	}
	job.Logf("✓ Установлено: %s", dst)
	if oldPath := fldb["__path__"]; oldPath != nil {
		delete(fldb, "__path__")
		if n := registerInFLDB(fldb, string(oldPath), dst); n > 0 {
			job.Logf("✓ Добавлен в базу плагинов FL Studio — найдёшь его в браузере Plugin database и в списке плагинов (если FL открыт — перезапусти)")
		}
	} else {
		job.Logf("Теперь в FL Studio: Options → Manage plugins → Find installed plugins")
	}
	ScanInstalled(cfg, true)
	return dst, nil
}

// pluginTargetDir: the folder where this computer keeps plugins of that format
// (the one most of them are in), so FL finds the new one together with the rest.
func pluginTargetDir(cfg *Config, format string) (dir string, needsAdmin bool) {
	count := map[string]int{}
	for _, p := range ScanInstalled(cfg, false) {
		if p.Format == format && p.Path != "" {
			count[filepath.Dir(strings.TrimRight(p.Path, `/\`))]++
		}
	}
	var cands []string
	for d := range count {
		cands = append(cands, d)
	}
	sort.Slice(cands, func(i, j int) bool { return count[cands[i]] > count[cands[j]] })
	cands = append(cands, standardPluginDirs(format)...)
	for _, c := range cands {
		if writableDir(c) {
			return c, false
		}
		if runtime.GOOS == "windows" && dirExists(c) {
			return c, true // ask Windows for permission (UAC) instead of hiding it elsewhere
		}
	}
	return "", false
}

func standardPluginDirs(format string) []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		sub := map[string]string{"VST3": "VST3", "VST2": "VST", "VST": "VST", "AU": "Components", "CLAP": "CLAP"}[format]
		if sub == "" {
			sub = "VST3"
		}
		return []string{filepath.Join("/Library/Audio/Plug-Ins", sub), filepath.Join(home, "Library", "Audio", "Plug-Ins", sub)}
	case "windows":
		cf := envOr("CommonProgramFiles", `C:\Program Files\Common Files`)
		pf := envOr("ProgramFiles", `C:\Program Files`)
		la := os.Getenv("LOCALAPPDATA")
		switch format {
		case "VST3":
			return []string{filepath.Join(cf, "VST3"), filepath.Join(la, "Programs", "Common", "VST3")}
		case "CLAP":
			return []string{filepath.Join(cf, "CLAP"), filepath.Join(la, "Programs", "Common", "CLAP")}
		default:
			return []string{filepath.Join(pf, "VSTPlugins"), filepath.Join(pf, "Steinberg", "VSTPlugins"), filepath.Join(pf, "Image-Line", "FL Studio 2024", "Plugins", "VST"),
				filepath.Join(home, "Documents", "VST Plugins")}
		}
	default:
		return []string{filepath.Join(home, "."+strings.ToLower(strings.TrimSuffix(format, "2")))}
	}
}

func writableDir(c string) bool {
	if os.MkdirAll(c, 0o755) != nil {
		return false
	}
	probe := filepath.Join(c, ".uzbek-write-test")
	if os.WriteFile(probe, nil, 0o644) != nil {
		return false
	}
	os.Remove(probe)
	return true
}

// moveAll renames, or copies when the folders are on different disks.
func moveAll(src, dst string) error {
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return os.MkdirAll(t, 0o755)
		case info.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, t)
		default:
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(t, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, in)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		}
	})
}

// ---------- outbox: what I allowed and still have to send ----------

type outboxItem struct {
	Kind string `json:"kind"`
	File string `json:"file"`
}

var outboxMu sync.Mutex

func outboxPath() string { return filepath.Join(ConfigDir(), "outbox.json") }

func outboxLoad() map[string]outboxItem {
	m := map[string]outboxItem{}
	readJSON(outboxPath(), &m)
	return m
}

func outboxPut(id string, it outboxItem) {
	outboxMu.Lock()
	defer outboxMu.Unlock()
	m := outboxLoad()
	m[id] = it
	writeJSON(outboxPath(), m)
}

func outboxGet(id string) (outboxItem, bool) {
	outboxMu.Lock()
	defer outboxMu.Unlock()
	it, ok := outboxLoad()[id]
	return it, ok
}

func outboxDelete(id string) {
	outboxMu.Lock()
	defer outboxMu.Unlock()
	m := outboxLoad()
	delete(m, id)
	writeJSON(outboxPath(), m)
}

// ---------- zip with symlinks and permissions (macOS bundles need both) ----------

func zipPath(src, dst string, extra map[string][]byte) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	base := filepath.Dir(strings.TrimRight(src, `/\`))
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		}
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, err = io.WriteString(w, t)
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		f.Close()
		return err
	})
	if err != nil {
		return err
	}
	if len(extra) > 0 { // FL database entries for the plugin + where it was on the sender's computer
		extra["__path__"] = []byte(strings.TrimRight(src, `/\`))
		for name, b := range extra {
			w, err := zw.Create(fldbPrefix + name)
			if err != nil {
				return err
			}
			w.Write(b)
		}
	}
	return zw.Close()
}

// unzipTo extracts a received plugin into an empty folder. FL database
// entries packed by the sender come back separately. Returns the top entry.
func unzipTo(src, dir string) (string, map[string][]byte, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return "", nil, err
	}
	defer r.Close()
	fldb := map[string][]byte{}
	top := ""
	clean := filepath.Clean(dir) + string(os.PathSeparator)
	for _, f := range r.File {
		if rel, ok := splitFLDB(f.Name); ok {
			rc, err := f.Open()
			if err == nil {
				b, _ := io.ReadAll(io.LimitReader(rc, 4<<20))
				rc.Close()
				fldb[rel] = b
			}
			continue
		}
		t := strings.SplitN(strings.TrimLeft(f.Name, "/"), "/", 2)[0]
		if t == "" || t == ".." {
			return "", nil, errors.New("странный архив")
		}
		if top == "" {
			top = t
		} else if t != top {
			return "", nil, errors.New("в архиве больше одного плагина")
		}
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(p, clean) {
			return "", nil, errors.New("архив пытается писать за пределы папки")
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return "", nil, err
			}
		case mode&os.ModeSymlink != 0:
			rc, _ := f.Open()
			t, _ := io.ReadAll(rc)
			rc.Close()
			os.MkdirAll(filepath.Dir(p), 0o755)
			if runtime.GOOS == "windows" {
				continue // symlinks only matter in macOS bundles
			}
			if err := os.Symlink(string(t), p); err != nil {
				return "", nil, err
			}
		default:
			os.MkdirAll(filepath.Dir(p), 0o755)
			rc, err := f.Open()
			if err != nil {
				return "", nil, err
			}
			w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o600)
			if err != nil {
				rc.Close()
				return "", nil, err
			}
			_, err = io.Copy(w, rc)
			rc.Close()
			w.Close()
			if err != nil {
				return "", nil, err
			}
			os.Chtimes(p, f.Modified, f.Modified)
		}
	}
	if top == "" {
		return "", nil, errors.New("пустой архив")
	}
	return top, fldb, nil
}
