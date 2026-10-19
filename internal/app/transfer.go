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
	"strings"
	"time"
)

// Plugin transfer (through our server): the requester asks, the owner
// allows it (or has allowed it in advance in the settings), the owner's app
// zips the plugin and uploads it to the server, the requester's app downloads
// and installs it, then the server deletes the file.

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
// itself; otherwise that file (an installer) is sent.
func ApproveRequest(cfg *Config, job *Job, id, file string) error {
	r, err := cfg.myRequest(id, true)
	if err != nil {
		return err
	}
	if r.Status != ReqPending {
		return fmt.Errorf("запрос уже обработан (%s)", r.Status)
	}
	h := cfg.Hub()
	if file != "" {
		st, err := os.Stat(file)
		if err != nil || st.IsDir() {
			return errors.New("файл установщика не найден")
		}
		job.Logf("Отправляю установщик %s для %s", filepath.Base(file), r.FromNick)
		if err := h.UploadFile(job, r.ID, file, "installer", filepath.Base(file), "установщик", r.Format); err != nil {
			return err
		}
		job.Logf("✓ Установщик отправлен, %s сможет запустить его у себя", r.FromNick)
		LogActivity("", "ok", fmt.Sprintf("🔌 Отправлен установщик %s для %s", r.Plugin, r.FromNick))
		return nil
	}
	in := InspectRequest(cfg, r)
	if !in.Compatible {
		return errors.New(in.Reason)
	}
	job.Logf("Упаковываю %s…", r.Plugin)
	tmp := filepath.Join(os.TempDir(), "uzbek-plugin-"+r.ID+".zip")
	defer os.Remove(tmp)
	if err := zipPath(in.Path, tmp); err != nil {
		return err
	}
	note := ""
	if in.Version != "" {
		note = "версия " + in.Version
	}
	if err := h.UploadFile(job, r.ID, tmp, "plugin", filepath.Base(in.Path)+".zip", note, in.Format); err != nil {
		return err
	}
	job.Logf("✓ %s отправлен, у %s он установится сам", r.Plugin, r.FromNick)
	LogActivity("", "ok", fmt.Sprintf("🔌 Отправлен плагин %s для %s", r.Plugin, r.FromNick))
	return nil
}

// InstallRequest: the requester downloads and installs an approved plugin.
func InstallRequest(cfg *Config, job *Job, id string) (string, error) {
	r, err := cfg.myRequest(id, false)
	if err != nil {
		return "", err
	}
	if r.Status != ReqApproved {
		return "", fmt.Errorf("плагин ещё не готов (%s)", r.Status)
	}
	h := cfg.Hub()
	if r.Kind == "installer" {
		home, _ := os.UserHomeDir()
		dst, _, _ := uniqueTarget(filepath.Join(home, "Downloads"), r.FileName, "")
		job.Logf("Скачиваю установщик %s…", r.FileName)
		if err := h.DownloadFile(job, r, dst); err != nil {
			return "", err
		}
		h.DoneRequest(r.ID, true, "установщик скачан")
		LogActivity("", "ok", fmt.Sprintf("🔌 Получен установщик %s от %s", r.Plugin, r.ToNick))
		if strings.HasSuffix(strings.ToLower(dst), ".zip") {
			OpenFolder(filepath.Dir(dst))
			job.Logf("✓ Архив установщика сохранён в «Загрузки» — открыл папку")
		} else {
			OpenURL(dst)
			job.Logf("✓ Установщик сохранён в «Загрузки» и запущен — пройди установку")
		}
		return dst, nil
	}
	tmp := filepath.Join(os.TempDir(), "uzbek-plugin-"+r.ID+".zip")
	defer os.Remove(tmp)
	job.Logf("Скачиваю %s…", r.Plugin)
	if err := h.DownloadFile(job, r, tmp); err != nil {
		return "", err
	}
	dir, err := installDir(cfg, job, r.Format)
	if err != nil {
		h.DoneRequest(r.ID, false, err.Error())
		return "", err
	}
	top, err := unzipTo(tmp, dir, job)
	if err != nil {
		h.DoneRequest(r.ID, false, err.Error())
		return "", err
	}
	if runtime.GOOS == "darwin" && r.Format == "AU" {
		killAUCache() // make macOS re-read the Audio Unit cache
	}
	dst := filepath.Join(dir, top)
	h.DoneRequest(r.ID, true, "")
	ScanInstalled(cfg, true)
	LogActivity("", "ok", fmt.Sprintf("🔌 Установлен %s от %s", r.Plugin, r.ToNick))
	job.Logf("✓ Установлено: %s", dst)
	job.Logf("Теперь в FL Studio: Options → Manage plugins → Find installed plugins (Start scan)")
	return dst, nil
}

// installDir picks a writable standard plugin folder for this format.
func installDir(cfg *Config, job *Job, format string) (string, error) {
	home, _ := os.UserHomeDir()
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		sub := map[string]string{"VST3": "VST3", "VST2": "VST", "VST": "VST", "AU": "Components", "CLAP": "CLAP"}[format]
		if sub == "" {
			sub = "VST3"
		}
		cands = []string{filepath.Join(home, "Library", "Audio", "Plug-Ins", sub)}
	case "windows":
		cf := envOr("CommonProgramFiles", `C:\Program Files\Common Files`)
		la := os.Getenv("LOCALAPPDATA")
		switch format {
		case "VST3":
			cands = []string{filepath.Join(cf, "VST3"), filepath.Join(la, "Programs", "Common", "VST3")}
		case "CLAP":
			cands = []string{filepath.Join(cf, "CLAP"), filepath.Join(la, "Programs", "Common", "CLAP")}
		default:
			for _, f := range ScannedFolders(cfg) {
				if f.Exists && f.Count > 0 && !strings.Contains(strings.ToLower(f.Path), "vst3") && !strings.Contains(strings.ToLower(f.Path), "clap") {
					cands = append(cands, f.Path)
				}
			}
		}
		cands = append(cands, filepath.Join(home, "Documents", "прога узбека — плагины", format))
	default:
		cands = []string{filepath.Join(home, "."+strings.ToLower(strings.TrimSuffix(format, "2")))}
	}
	for i, c := range cands {
		if os.MkdirAll(c, 0o755) != nil {
			continue
		}
		probe := filepath.Join(c, ".uzbek-write-test")
		if os.WriteFile(probe, nil, 0o644) != nil {
			continue
		}
		os.Remove(probe)
		if i > 0 && runtime.GOOS == "windows" {
			// non-standard folder: remember it and tell the user to add it in FL once
			found := false
			for _, e := range cfg.ExtraPluginDirs {
				if samePath(e, c) {
					found = true
				}
			}
			if !found {
				cfg.ExtraPluginDirs = append(cfg.ExtraPluginDirs, c)
				cfg.Save()
			}
			job.Logf("⚠ В стандартную папку писать нельзя без прав администратора — ставлю в %s. Добавь её один раз в FL: Options → Manage plugins → Plugin search paths", c)
		}
		return c, nil
	}
	return "", errors.New("не нашёл папку для установки, куда можно писать")
}

// ---------- zip with symlinks and permissions (macOS bundles need both) ----------

func zipPath(src, dst string) error {
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
	return zw.Close()
}

// unzipTo extracts into dir. An existing plugin with the same name is moved
// to a backup folder first. Returns the top-level entry name.
func unzipTo(src, dir string, job *Job) (string, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if len(r.File) == 0 {
		return "", errors.New("пустой архив")
	}
	top := strings.SplitN(strings.TrimLeft(r.File[0].Name, "/"), "/", 2)[0]
	if top == "" || top == ".." {
		return "", errors.New("странный архив")
	}
	target := filepath.Join(dir, top)
	if exists(target) {
		bak := filepath.Join(ConfigDir(), "plugin-backups", time.Now().Format("20060102-150405"))
		os.MkdirAll(bak, 0o755)
		if err := os.Rename(target, filepath.Join(bak, top)); err != nil {
			return "", fmt.Errorf("не могу заменить существующий %s (закрой FL Studio): %w", top, err)
		}
		job.Logf("Старая версия перенесена в %s", bak)
	}
	clean := filepath.Clean(dir) + string(os.PathSeparator)
	for _, f := range r.File {
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(p, clean) {
			return "", errors.New("архив пытается писать за пределы папки")
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return "", err
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
				return "", err
			}
		default:
			os.MkdirAll(filepath.Dir(p), 0o755)
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o600)
			if err != nil {
				rc.Close()
				return "", err
			}
			_, err = io.Copy(w, rc)
			rc.Close()
			w.Close()
			if err != nil {
				return "", err
			}
		}
	}
	return top, nil
}
