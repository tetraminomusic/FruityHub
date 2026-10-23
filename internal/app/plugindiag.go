package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ---------- «Диагностика» плагина: подробный текстовый отчёт, чтобы сравнить два компьютера ----------

// guessVendor — производитель, если в базе FL его нет: подпапка в папке плагинов (VST3\Arturia\…)
// или moduleinfo.json внутри пакета VST3.
func guessVendor(cfg *Config, pluginPath string) string {
	if b, err := os.ReadFile(filepath.Join(pluginPath, "Contents", "Resources", "moduleinfo.json")); err == nil {
		var mi struct {
			FactoryInfo struct {
				Vendor string `json:"Vendor"`
			} `json:"Factory Info"`
		}
		if json.Unmarshal(b, &mi) == nil && strings.TrimSpace(mi.FactoryInfo.Vendor) != "" {
			return strings.TrimSpace(mi.FactoryInfo.Vendor)
		}
	}
	if _, sub := pluginRootOf(cfg, pluginPath); sub != "" {
		return strings.Split(filepath.ToSlash(sub), "/")[0]
	}
	return ""
}

func PluginDiag(cfg *Config, job *Job, name, format string) (string, error) {
	ip := FindInstalled(cfg, name, format)
	if ip == nil {
		ScanInstalled(cfg, true)
		ip = FindInstalled(cfg, name, format)
	}
	if ip == nil || ip.Path == "" {
		return "", errors.New("не нашёл у тебя файл этого плагина")
	}
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("=== Диагностика %s (%s) ===", ip.Name, ip.Format)
	w("FruityHub %s · %s %s · %s · %s", Version, OSName(), runtime.GOARCH, cfg.MachineID(), time.Now().Format("2006-01-02 15:04"))
	vendor := pluginVendor(ip.Vendor, ip)
	if vendor == "" {
		vendor = guessVendor(cfg, ip.Path)
		w("Производитель: %s (угадан по папке/moduleinfo)", vendor)
	} else {
		w("Производитель: %s", vendor)
	}
	w("Плагин: %s · версия %s", ip.Path, ip.Version)
	bin := pluginBinary(ip.Path)
	if st, err := os.Stat(bin); err == nil {
		w("Файл: %s · %s · %s · %s", bin, fmtBytes(st.Size()), strings.Join(binArch(bin), ","), st.ModTime().Format("2006-01-02"))
	} else {
		w("Файл плагина внутри пакета: НЕ НАЙДЕН")
	}

	job.Logf("Ищу данные плагина…")
	take, skip, deps := findPluginDataEx(ip.Name, vendor, ip.Path)
	w("\n--- Что уйдёт вместе с плагином (%d) ---", len(take))
	for _, d := range take {
		base, files := dataFiles(&d)
		w("+ [%s] %s → %s · %d файлов · %s · %s", d.Root, d.Rel, base, len(files), fmtBytes(d.Size), d.Why)
	}
	for _, d := range skip {
		w("- [%s] %s · %s · НЕ уйдёт: %s", d.Root, d.Rel, fmtBytes(d.Size), d.Note)
	}
	w("\n--- Что нужно файлу плагина ---")
	for _, it := range deps.Items {
		if it.Status == "system" {
			continue
		}
		w("%s %s → %s [%s] %s", it.Kind, it.Name, it.Path, it.Status, it.Note)
	}
	for _, r := range deps.Registry {
		w("реестр %s\\%s · %s = %s", r.Hive, r.Key, r.Name, r.Data)
	}

	job.Logf("Ищу папки с именем плагина…")
	w("\n--- Папки с именем плагина или производителя ---")
	keys := []string{normName(ip.Name)}
	vkey := normName(vendor)
	n := 0
	for _, r := range dataRoots() {
		walkDepth(r.Path, 3, func(p string, d fs.DirEntry, depth int) bool {
			if !d.IsDir() {
				return false
			}
			nm := d.Name()
			if anyNameMatch(nm, keys) || depth == 1 && vendorMatch(nm, vkey) {
				cnt, size := countTree(p, 20000)
				w("[%s] %s · %d файлов · %s", r.Key, p, cnt, fmtBytes(size))
				n++
				return anyNameMatch(nm, keys) // в папку плагина дальше не идём
			}
			return n > 40
		})
	}
	if n == 0 {
		w("(ничего)")
	}

	if strings.HasPrefix(vkey, "arturia") {
		job.Logf("Проверяю ресурсы Arturia…")
		w("\n--- Arturia ---")
		if msg := arturiaResourcesCheck(ip.Name, vendor); msg != "" {
			w("Проверка: %s", msg)
		} else {
			w("Проверка: папка ресурсов и окно активации на месте")
		}
		var roots []string
		switch runtime.GOOS {
		case "windows":
			for _, e := range []string{"ProgramData", "ProgramFiles", "ProgramFiles(x86)", "CommonProgramFiles", "APPDATA", "LOCALAPPDATA"} {
				if v := os.Getenv(e); v != "" {
					roots = append(roots, filepath.Join(v, "Arturia"))
				}
			}
			roots = append(roots, filepath.Join(documentsDir(), "Arturia"))
		case "darwin":
			home, _ := os.UserHomeDir()
			roots = []string{"/Library/Arturia", filepath.Join(home, "Library", "Arturia"), "/Library/Application Support/Arturia", filepath.Join(home, "Library", "Application Support", "Arturia"), filepath.Join(home, "Documents", "Arturia")}
		}
		hits := 0
		for _, r := range roots {
			if !dirExists(r) {
				continue
			}
			ents, _ := os.ReadDir(r)
			var names []string
			for _, e := range ents {
				names = append(names, e.Name())
			}
			w("%s: %s", r, strings.Join(names, " | "))
			walkDepth(r, 6, func(p string, d fs.DirEntry, depth int) bool {
				if strings.EqualFold(d.Name(), "gui-activation-generated.xml") && hits < 15 {
					w("  найден %s", p)
					hits++
				}
				return false
			})
		}
		if hits == 0 {
			w("Файла gui-activation-generated.xml на этом компьютере нет нигде в папках Arturia")
		}
		if runtime.GOOS == "windows" {
			for _, k := range []string{`HKLM\SOFTWARE\Arturia`, `HKCU\SOFTWARE\Arturia`} {
				if out := regQueryText(k); out != "" {
					w("\n%s:\n%s", k, out)
				}
			}
		}
	}
	if rep, ok := allReports()[lastReportID(ip.Name)]; ok {
		w("\n--- Последняя установка ---")
		w("%s от %s → %s", time.Unix(rep.At, 0).Format("2006-01-02 15:04"), rep.From, rep.Path)
		for _, d := range rep.Data {
			w("✓ %s", d)
		}
		for _, x := range rep.Warnings {
			w("! %s", x.Text)
		}
	}
	return b.String(), nil
}

func lastReportID(plugin string) string {
	id := ""
	var at int64
	for k, r := range allReports() {
		if strings.EqualFold(r.Plugin, plugin) && r.At > at && !strings.HasPrefix(k, "check-") {
			id, at = k, r.At
		}
	}
	return id
}

// walkDepth обходит папку не глубже depth; fn возвращает true, чтобы не заходить внутрь.
func walkDepth(root string, depth int, fn func(p string, d fs.DirEntry, depth int) bool) {
	var rec func(dir string, lvl int)
	rec = func(dir string, lvl int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			skip := fn(p, e, lvl)
			if e.IsDir() && !skip && lvl < depth && e.Type()&os.ModeSymlink == 0 {
				rec(p, lvl+1)
			}
		}
	}
	rec(root, 1)
}

func countTree(p string, max int) (int, int64) {
	n, size := 0, int64(0)
	filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			n++
			if i, e := d.Info(); e == nil {
				size += i.Size()
			}
		}
		if n >= max {
			return filepath.SkipAll
		}
		return nil
	})
	return n, size
}
