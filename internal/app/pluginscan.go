package app

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// PluginFolder is a folder where we look for plugins, with the reason why.
type PluginFolder struct {
	Path   string `json:"path"`
	Source string `json:"source"` // стандартная, база FL, настройки FL, реестр VST, вручную
	Exists bool   `json:"exists"`
	Count  int    `json:"count"`
}

var pluginExts = map[string]string{".vst3": "VST3", ".component": "AU", ".clap": "CLAP", ".dll": "VST2", ".vst": "VST2"}

func defaultPluginDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		var d []string
		for _, root := range []string{"/Library/Audio/Plug-Ins", filepath.Join(home, "Library/Audio/Plug-Ins")} {
			for _, sub := range []string{"VST3", "VST", "Components", "CLAP"} {
				d = append(d, filepath.Join(root, sub))
			}
		}
		return d
	case "windows":
		pf, pf86 := envOr("ProgramFiles", `C:\Program Files`), envOr("ProgramFiles(x86)", `C:\Program Files (x86)`)
		cf := envOr("CommonProgramFiles", filepath.Join(pf, "Common Files"))
		la := os.Getenv("LOCALAPPDATA")
		d := []string{
			filepath.Join(cf, "VST3"), filepath.Join(cf, "CLAP"), filepath.Join(cf, "VST2"),
			filepath.Join(pf, "VSTPlugins"), filepath.Join(pf, "Steinberg", "VstPlugins"), filepath.Join(cf, "Steinberg", "VST2"),
			filepath.Join(pf86, "VSTPlugins"), filepath.Join(pf86, "Steinberg", "VstPlugins"),
			filepath.Join(pf, "Image-Line", "Shared", "VST"),
		}
		if la != "" {
			d = append(d, filepath.Join(la, "Programs", "Common", "VST3"), filepath.Join(la, "Programs", "Common", "CLAP"))
		}
		return d
	}
	return []string{filepath.Join(home, ".vst3"), filepath.Join(home, ".vst"), filepath.Join(home, ".clap"), "/usr/lib/vst3", "/usr/lib/vst"}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- FL Studio plugin database ----------

type flDBEntry struct {
	Name, Vendor, Path, Format string
}

func flPluginDBDir() string {
	return filepath.Join(flUserData(), "Presets", "Plugin database", "Installed")
}

// readFLPluginDB parses .nfo files: UTF-8 "key=value" lines. Keys we use:
// ps_file_name_0 (plugin name), ps_file_filename_0 (plugin path, may start
// with %FLPath% for native plugins), ps_file_vendorname_0. Note that
// ps_presetfilename also contains "filename" — hence exact key matches.
func readFLPluginDB() []flDBEntry {
	var out []flDBEntry
	filepath.WalkDir(flPluginDBDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".nfo") {
			return nil
		}
		cat := strings.ToUpper(filepath.Base(filepath.Dir(path)))
		e := flDBEntry{Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
		e.Format = map[string]string{"FRUITY": "FL", "VST": "VST2", "VST2": "VST2", "VST3": "VST3", "AU": "AU", "AUDIOUNIT": "AU", "CLAP": "CLAP"}[cat]
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			switch {
			case k == "ps_file_vendorname_0" && e.Vendor == "":
				e.Vendor = v
			case k == "ps_file_filename_0" && e.Path == "":
				e.Path = v
			case k == "ps_file_name_0" && v != "":
				e.Name = v // the real plugin name ("Serum"), the file name may be "Serum_2"
			}
		}
		f.Close()
		if e.Format == "" && e.Path != "" {
			e.Format = pluginExts[strings.ToLower(filepath.Ext(strings.TrimRight(e.Path, `/\`)))]
		}
		if e.Format != "" {
			out = append(out, e)
		}
		return nil
	})
	return out
}

// pluginRoot turns ".../VST3/Xfer/Serum.vst3" into ".../VST3/Xfer".
func pluginRoot(p string) string { return filepath.Dir(strings.TrimRight(p, `/\`)) }

// PluginFolders lists every folder we scan and where it came from.
func PluginFolders(cfg *Config) []PluginFolder {
	var out []PluginFolder
	seen := map[string]bool{}
	add := func(p, src string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		k := strings.ToLower(p)
		if seen[k] {
			for i := range out {
				if strings.ToLower(out[i].Path) == k && !strings.Contains(out[i].Source, src) {
					out[i].Source += " + " + src
				}
			}
			return
		}
		// skip sub-folders of something already listed
		for _, f := range out {
			if f.Exists && isInside(f.Path, p) && !samePath(f.Path, p) {
				return
			}
		}
		seen[k] = true
		_, err := os.Stat(p)
		out = append(out, PluginFolder{Path: p, Source: src, Exists: err == nil})
	}
	for _, d := range cfg.ExtraPluginDirs {
		add(d, "вручную")
	}
	for _, d := range flSettingsPluginDirs() {
		add(d, "настройки FL")
	}
	for _, d := range systemVSTDirs() {
		add(d, "системный путь VST")
	}
	for _, d := range defaultPluginDirs() {
		add(d, "стандартная")
	}
	for _, e := range readFLPluginDB() {
		if e.Path != "" && filepath.IsAbs(e.Path) {
			add(pluginRoot(e.Path), "база FL")
		}
	}
	return out
}

var (
	scanMu      sync.Mutex
	scanCache   []InstalledPlugin
	scanFolders []PluginFolder
	scanTime    time.Time
)

// ScanInstalled finds plugins on disk and in FL Studio's plugin database.
// Без force отвечает сразу: из памяти или из прошлого сканирования на диске, а если оно старое —
// обновляет его в фоне (сканирование папок плагинов бывает долгим).
func ScanInstalled(cfg *Config, force bool) []InstalledPlugin {
	if !force {
		scanMu.Lock()
		if scanCache == nil {
			if res, folders, at, ok := loadScanCache(); ok {
				scanCache, scanFolders, scanTime = res, folders, at
			}
		}
		if scanCache != nil {
			res, stale := scanCache, time.Since(scanTime) > 10*time.Minute
			scanMu.Unlock()
			if stale {
				refreshScanInBackground(cfg)
			}
			return res
		}
		scanMu.Unlock()
	}
	return scanInstalledNow(cfg)
}

var scanRefreshing sync.Mutex

func refreshScanInBackground(cfg *Config) {
	if !scanRefreshing.TryLock() {
		return
	}
	go func() {
		defer scanRefreshing.Unlock()
		scanInstalledNow(cfg)
	}()
}

var scanWork sync.Mutex

func scanInstalledNow(cfg *Config) []InstalledPlugin {
	scanWork.Lock()
	defer scanWork.Unlock()
	vc := loadVersionCache()
	idx := map[string]int{}
	var res []InstalledPlugin
	add := func(p InstalledPlugin) {
		if p.Name == "" {
			return
		}
		k := p.Format + "|" + normName(p.Name)
		if i, ok := idx[k]; ok {
			r := &res[i]
			if r.Path == "" && p.Path != "" {
				r.Path, r.Version = p.Path, p.Version
			}
			if r.Vendor == "" {
				r.Vendor = p.Vendor
			}
			return
		}
		idx[k] = len(res)
		res = append(res, p)
	}
	folders := PluginFolders(cfg)
	for i, f := range folders {
		if !f.Exists {
			continue
		}
		filepath.WalkDir(f.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil || path == f.Path {
				return nil
			}
			fm := pluginExts[strings.ToLower(filepath.Ext(d.Name()))]
			if fm == "" || (fm == "VST2" && runtime.GOOS != "windows" && strings.HasSuffix(strings.ToLower(d.Name()), ".dll")) {
				return nil
			}
			add(InstalledPlugin{Name: strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())), Format: fm, Path: path, Source: "disk", Version: vc.get(path)})
			folders[i].Count++
			if d.IsDir() {
				return filepath.SkipDir // bundle
			}
			return nil
		})
	}
	for _, e := range readFLPluginDB() {
		p := InstalledPlugin{Name: e.Name, Format: e.Format, Vendor: e.Vendor, Source: "fl-db"}
		if e.Path != "" && exists(e.Path) {
			p.Path, p.Version = e.Path, vc.get(e.Path)
		}
		add(p)
	}
	for _, fl := range flInstallDirs() {
		for _, sub := range []string{"Generators", "Effects"} {
			ents, _ := os.ReadDir(filepath.Join(fl, "Plugins", "Fruity", sub))
			for _, e := range ents {
				if e.IsDir() {
					add(InstalledPlugin{Name: e.Name(), Format: "FL", Source: "fl-install"})
				}
			}
		}
	}
	vc.save()
	sort.Slice(res, func(i, j int) bool { return strings.ToLower(res[i].Name) < strings.ToLower(res[j].Name) })
	scanMu.Lock()
	scanCache, scanFolders, scanTime = res, folders, time.Now()
	scanMu.Unlock()
	go saveScanCache(res, folders)
	return res
}

// ScannedFolders returns folders with plugin counts from the last scan.
func ScannedFolders(cfg *Config) []PluginFolder {
	ScanInstalled(cfg, false)
	scanMu.Lock()
	defer scanMu.Unlock()
	return scanFolders
}

// FindInstalled finds a local plugin by name/format (used for transfers).
func FindInstalled(cfg *Config, name, format string) *InstalledPlugin {
	k := normName(name)
	for _, p := range ScanInstalled(cfg, false) {
		if normName(p.Name) == k && (p.Format == format || format == "" || (format == "VST" && strings.HasPrefix(p.Format, "VST"))) && p.Path != "" {
			cp := p
			return &cp
		}
	}
	return nil
}
