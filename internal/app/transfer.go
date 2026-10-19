package app

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Plugin transfer: the requester asks, the owner explicitly allows it, the
// owner's app zips the plugin and uploads it to the project storage, the
// requester's app downloads and installs it, then the file is deleted from
// the storage. Requests live on the meta branch (requests/<id>.json).

type PluginRequest struct {
	ID          string `json:"id"`
	Plugin      string `json:"plugin"`
	Format      string `json:"format"`
	Vendor      string `json:"vendor,omitempty"`
	From        string `json:"from"` // who asks
	FromOS      string `json:"fromOs"`
	To          string `json:"to"` // who has the plugin
	Status      string `json:"status"`
	Created     int64  `json:"created"`
	Updated     int64  `json:"updated"`
	AssetID     int64  `json:"assetId,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Note        string `json:"note,omitempty"`
	InstalledTo string `json:"installedTo,omitempty"`
	Kind        string `json:"kind,omitempty"`     // "plugin" (ready to install) or "installer" (a file to run)
	FileName    string `json:"fileName,omitempty"` // for installers
	Auto        bool   `json:"auto,omitempty"`     // approved automatically (trusted friend)
}

// Statuses
const (
	ReqPending   = "pending"
	ReqApproved  = "approved" // uploaded, waiting for the requester to install
	ReqDeclined  = "declined"
	ReqInstalled = "installed"
	ReqFailed    = "failed"
	ReqCancelled = "cancelled"
)

func (p *Project) Requests() []PluginRequest {
	var rs []PluginRequest
	for _, b := range p.git().MetaFiles("requests/") {
		var r PluginRequest
		if json.Unmarshal(b, &r) == nil && r.ID != "" {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Created > rs[j].Created })
	return rs
}

func (p *Project) request(id string) (*PluginRequest, error) {
	p.git().Fetch()
	for _, r := range p.Requests() {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, errors.New("запрос не найден")
}

func (p *Project) saveRequest(r *PluginRequest, msg string) error {
	r.Updated = time.Now().Unix()
	return p.git().MetaWrite(map[string][]byte{"requests/" + r.ID + ".json": mustJSON(r)}, msg)
}

// RequestPlugin asks another participant for a plugin.
func (p *Project) RequestPlugin(name, format, vendor, to string) (*PluginRequest, error) {
	me := p.Cfg.MachineID()
	if to == me {
		return nil, errors.New("нельзя попросить плагин у самого себя")
	}
	var owner *Machine
	for _, m := range p.Machines() {
		if m.User == to {
			mm := m
			owner = &mm
		}
	}
	if owner == nil {
		return nil, fmt.Errorf("не знаю участника %s", to)
	}
	for _, r := range p.Requests() {
		if r.From == me && normName(r.Plugin) == normName(name) && (r.Status == ReqPending || r.Status == ReqApproved) {
			return nil, errors.New("такой запрос уже отправлен — ждём ответа")
		}
	}
	now := time.Now().Unix()
	r := &PluginRequest{ID: newID(me), Plugin: name, Format: format, Vendor: vendor, From: me, FromOS: OSName(), To: to,
		Status: ReqPending, Created: now}
	return r, p.saveRequest(r, fmt.Sprintf("🔌 %s просит у %s плагин %s", me, to, name))
}

func (p *Project) CancelRequest(id string) error {
	r, err := p.request(id)
	if err != nil {
		return err
	}
	if r.From != p.Cfg.MachineID() {
		return errors.New("отменить может только тот, кто просил")
	}
	if r.AssetID != 0 {
		if s, err := p.storage(); err == nil {
			s.Delete(r.AssetID)
		}
	}
	r.Status, r.AssetID = ReqCancelled, 0
	return p.saveRequest(r, "🔌 запрос отменён")
}

func (p *Project) DeclineRequest(id, reason string) error {
	r, err := p.request(id)
	if err != nil {
		return err
	}
	if r.To != p.Cfg.MachineID() {
		return errors.New("это запрос не к тебе")
	}
	r.Status, r.Note = ReqDeclined, strings.TrimSpace(reason)
	return p.saveRequest(r, "🔌 "+r.To+" отклонил запрос "+r.Plugin)
}

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

// Inspect: can I send this plugin to the requester, and how?
func (p *Project) Inspect(id string) (*Inspection, error) {
	r, err := p.request(id)
	if err != nil {
		return nil, err
	}
	in := &Inspection{SameOS: r.FromOS == OSName()}
	ip := FindInstalled(p.Cfg, r.Plugin, r.Format)
	if ip == nil {
		ScanInstalled(p.Cfg, true)
		ip = FindInstalled(p.Cfg, r.Plugin, r.Format)
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
		in.Reason = fmt.Sprintf("Твой файл плагина собран только под %s, у %s — %s. Отправь установщик для %s (можно скачать у производителя).", OSName(), r.From, r.FromOS, r.FromOS)
	}
	in.Installers = findInstallers(r.Plugin, r.Vendor, r.FromOS)
	return in, nil
}

// ApproveRequest: the owner allowed it. file == "" sends the installed plugin
// itself; otherwise that file (an installer) is sent.
func (p *Project) ApproveRequest(id, file string, trust bool) error {
	r, err := p.request(id)
	if err != nil {
		return err
	}
	if r.To != p.Cfg.MachineID() {
		return errors.New("это запрос не к тебе")
	}
	if r.Status != ReqPending {
		return fmt.Errorf("запрос уже обработан (%s)", r.Status)
	}
	if trust {
		found := false
		for _, t := range p.Cfg.TrustedRequesters {
			found = found || strings.EqualFold(t, r.From)
		}
		if !found {
			p.Cfg.TrustedRequesters = append(p.Cfg.TrustedRequesters, r.From)
			p.Cfg.Save()
		}
	}
	s, err := p.storage()
	if err != nil {
		return err
	}
	if file != "" {
		st, err := os.Stat(file)
		if err != nil || st.IsDir() {
			return errors.New("файл установщика не найден")
		}
		p.Job.Logf("Отправляю установщик %s", filepath.Base(file))
		a, err := s.Upload(p.Job, "plugin-"+r.ID+strings.ToLower(filepath.Ext(file)), r.Plugin+" — установщик", file)
		if err != nil {
			return err
		}
		r.Status, r.AssetID, r.Size, r.Kind, r.FileName, r.Note = ReqApproved, a.ID, a.Size, "installer", filepath.Base(file), "установщик"
		if err := p.saveRequest(r, "🔌 "+r.To+" отправил установщик "+r.Plugin); err != nil {
			return err
		}
		p.Job.Logf("✓ Установщик отправлен, %s сможет запустить его у себя", r.From)
		return nil
	}
	in, err := p.Inspect(id)
	if err != nil {
		return err
	}
	if !in.Compatible {
		return errors.New(in.Reason)
	}
	p.Job.Logf("Упаковываю %s…", r.Plugin)
	tmp := filepath.Join(os.TempDir(), "uzbek-plugin-"+r.ID+".zip")
	defer os.Remove(tmp)
	if err := zipPath(in.Path, tmp); err != nil {
		return err
	}
	a, err := s.Upload(p.Job, "plugin-"+r.ID+".zip", r.Plugin+" ("+in.Format+")", tmp)
	if err != nil {
		return err
	}
	r.Status, r.AssetID, r.Size, r.Format, r.Kind = ReqApproved, a.ID, a.Size, in.Format, "plugin"
	r.Note = ""
	if in.Version != "" {
		r.Note = "версия " + in.Version
	}
	if err := p.saveRequest(r, "🔌 "+r.To+" разрешил и отправил "+r.Plugin); err != nil {
		return err
	}
	p.Job.Logf("✓ Плагин отправлен, %s сможет установить его у себя", r.From)
	return nil
}

// InstallRequest: the requester downloads and installs an approved plugin.
func (p *Project) InstallRequest(id string) (string, error) {
	r, err := p.request(id)
	if err != nil {
		return "", err
	}
	if r.From != p.Cfg.MachineID() {
		return "", errors.New("это не твой запрос")
	}
	if r.Status != ReqApproved || r.AssetID == 0 {
		return "", fmt.Errorf("плагин ещё не готов (%s)", r.Status)
	}
	s, err := p.storage()
	if err != nil {
		return "", err
	}
	if r.Kind == "installer" {
		home, _ := os.UserHomeDir()
		dst, _, _ := uniqueTarget(filepath.Join(home, "Downloads"), r.FileName, "")
		p.Job.Logf("Скачиваю установщик %s…", r.FileName)
		if err := s.Download(p.Job, Asset{ID: r.AssetID, Size: r.Size}, dst); err != nil {
			return "", err
		}
		s.Delete(r.AssetID)
		r.Status, r.AssetID, r.InstalledTo = ReqInstalled, 0, dst
		p.saveRequest(r, "🔌 "+r.From+" получил установщик "+r.Plugin)
		if strings.HasSuffix(strings.ToLower(dst), ".zip") {
			OpenFolder(filepath.Dir(dst))
			p.Job.Logf("✓ Архив установщика сохранён в «Загрузки» — открыл папку")
		} else {
			OpenURL(dst)
			p.Job.Logf("✓ Установщик сохранён в «Загрузки» и запущен — пройди установку")
		}
		return dst, nil
	}
	tmp := filepath.Join(os.TempDir(), "uzbek-plugin-"+r.ID+".zip")
	defer os.Remove(tmp)
	p.Job.Logf("Скачиваю %s…", r.Plugin)
	if err := s.Download(p.Job, Asset{ID: r.AssetID, Size: r.Size}, tmp); err != nil {
		return "", err
	}
	dir, err := p.installDir(r.Format)
	if err != nil {
		return "", err
	}
	top, err := unzipTo(tmp, dir, p.Job)
	if err != nil {
		r.Status, r.Note = ReqFailed, err.Error()
		p.saveRequest(r, "🔌 установка не удалась")
		return "", err
	}
	if runtime.GOOS == "darwin" && r.Format == "AU" {
		// make macOS re-read the Audio Unit cache
		killAUCache()
	}
	s.Delete(r.AssetID)
	r.Status, r.AssetID, r.InstalledTo = ReqInstalled, 0, filepath.Join(dir, top)
	p.saveRequest(r, "🔌 "+r.From+" установил "+r.Plugin)
	ScanInstalled(p.Cfg, true)
	p.Job.Logf("✓ Установлено: %s", r.InstalledTo)
	p.Job.Logf("Теперь в FL Studio: Options → Manage plugins → Find installed plugins (Start scan)")
	return r.InstalledTo, nil
}

// installDir picks a writable standard plugin folder for this format.
func (p *Project) installDir(format string) (string, error) {
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
			for _, f := range ScannedFolders(p.Cfg) {
				if f.Exists && f.Count > 0 && !strings.Contains(strings.ToLower(f.Path), "vst3") && !strings.Contains(strings.ToLower(f.Path), "clap") {
					cands = append(cands, f.Path)
				}
			}
		}
		cands = append(cands, filepath.Join(home, "Documents", "Программа Узбека — плагины", format))
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
			for _, e := range p.Cfg.ExtraPluginDirs {
				if samePath(e, c) {
					found = true
				}
			}
			if !found {
				p.Cfg.ExtraPluginDirs = append(p.Cfg.ExtraPluginDirs, c)
				p.Cfg.Save()
			}
			p.Job.Logf("⚠ В стандартную папку писать нельзя без прав администратора — ставлю в %s. Добавь её один раз в FL: Options → Manage plugins → Plugin search paths", c)
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
