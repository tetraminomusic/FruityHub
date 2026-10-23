package app

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"flsync/internal/flp"
)

// InstalledPlugin is a plugin found on this machine.
type InstalledPlugin struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Version string `json:"version,omitempty"`
	Vendor  string `json:"vendor,omitempty"`
	Path    string `json:"-"`
	Source  string `json:"-"` // "disk", "fl-db", "fl-install"
}

// Machine is the inventory a participant publishes into the repo.
type Machine struct {
	User      string            `json:"user"`
	GitHub    string            `json:"github,omitempty"`
	OS        string            `json:"os"`
	FLVersion string            `json:"flVersion,omitempty"`
	Updated   string            `json:"updated"`
	Plugins   []InstalledPlugin `json:"plugins"`
}

func flInstallDirs() []string {
	var out []string
	switch runtime.GOOS {
	case "darwin":
		m, _ := filepath.Glob("/Applications/FL Studio*.app/Contents/Resources/FL")
		out = m
	case "windows":
		pf := os.Getenv("ProgramFiles")
		m, _ := filepath.Glob(filepath.Join(pf, "Image-Line", "FL Studio*"))
		out = m
	}
	return out
}

var nonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`)
var archSuffix = regexp.MustCompile(`(x64|x86|64bit|32bit|_64|vst3|vst2|vst|stereo|mono)$`)

func normName(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "")
	for i := 0; i < 3; i++ {
		t := archSuffix.ReplaceAllString(s, "")
		if t == s || len(t) < 3 {
			break
		}
		s = t
	}
	return s
}

// ---------- plugin version detection ----------

type versionCache struct {
	M     map[string]string `json:"m"`
	dirty bool
}

func loadVersionCache() *versionCache {
	vc := &versionCache{M: map[string]string{}}
	readJSON(filepath.Join(ConfigDir(), "plugin-versions.json"), vc)
	if vc.M == nil {
		vc.M = map[string]string{}
	}
	return vc
}

func (vc *versionCache) save() {
	if vc.dirty {
		writeJSON(filepath.Join(ConfigDir(), "plugin-versions.json"), vc)
	}
}

func (vc *versionCache) get(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	key := fmt.Sprintf("%s|%d", path, st.ModTime().Unix())
	if v, ok := vc.M[key]; ok {
		return v
	}
	v := detectVersion(path, st.IsDir())
	vc.M[key] = v
	vc.dirty = true
	return v
}

var plistVer = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)
var plistVer2 = regexp.MustCompile(`<key>CFBundleVersion</key>\s*<string>([^<]+)</string>`)

func detectVersion(path string, dir bool) string {
	if dir {
		if b, err := os.ReadFile(filepath.Join(path, "Contents", "Info.plist")); err == nil {
			if m := plistVer.FindSubmatch(b); m != nil {
				return strings.TrimSpace(string(m[1]))
			}
			if m := plistVer2.FindSubmatch(b); m != nil {
				return strings.TrimSpace(string(m[1]))
			}
		}
		var mi struct {
			Version string `json:"Version"`
		}
		if b, err := os.ReadFile(filepath.Join(path, "Contents", "moduleinfo.json")); err == nil {
			if json.Unmarshal(b, &mi) == nil && mi.Version != "" {
				return mi.Version
			}
		}
		// Windows VST3 bundle: Contents/x86_64-win/Name.vst3 (a PE dll)
		m, _ := filepath.Glob(filepath.Join(path, "Contents", "x86_64-win", "*.vst3"))
		if len(m) > 0 {
			return peVersion(m[0])
		}
		return ""
	}
	l := strings.ToLower(path)
	if strings.HasSuffix(l, ".dll") || strings.HasSuffix(l, ".vst3") || strings.HasSuffix(l, ".clap") {
		return peVersion(path)
	}
	return ""
}

// peVersion reads VS_FIXEDFILEINFO from a Windows PE file.
func peVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sig := []byte{0xBD, 0x04, 0xEF, 0xFE}
	buf := make([]byte, 1<<20)
	var carry []byte
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			if i := bytes.Index(chunk, sig); i >= 0 && i+16 <= len(chunk) {
				ms := binary.LittleEndian.Uint32(chunk[i+8:])
				ls := binary.LittleEndian.Uint32(chunk[i+12:])
				return fmt.Sprintf("%d.%d.%d.%d", ms>>16, ms&0xffff, ls>>16, ls&0xffff)
			} else if i >= 0 {
				carry = chunk[i:]
				continue
			}
			if len(chunk) > 32 {
				carry = append([]byte{}, chunk[len(chunk)-32:]...)
			}
		}
		if err == io.EOF || err != nil {
			return ""
		}
	}
}

// ---------- manifest ----------

type MachineStatus struct {
	User    string `json:"user"`
	OS      string `json:"os"`
	State   string `json:"state"` // ok, missing, other-format, unknown
	Version string `json:"version,omitempty"`
	Note    string `json:"note,omitempty"`
}

type PluginRow struct {
	flp.Plugin
	Machines []MachineStatus `json:"machines"`
	Problem  string          `json:"problem,omitempty"`
	Link     string          `json:"link,omitempty"`
	LinkNote string          `json:"linkNote,omitempty"`
	Files    []LinkFile      `json:"files,omitempty"`
}

type CatalogEntry struct {
	URL   string     `json:"url"`
	Note  string     `json:"note"`
	Files []LinkFile `json:"files,omitempty"` // небольшие файлы к плагину (пресеты, установщик, ключ-файл бесплатной версии…)
}

// LinkFile — файл, прикреплённый к ссылке плагина. Лежит в хранилище проекта на GitHub (черновик релиза).
type LinkFile struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Asset string `json:"asset"`
	By    string `json:"by,omitempty"`
}

func (p *Project) Machines() []Machine {
	var ms []Machine
	ents, _ := os.ReadDir(filepath.Join(p.Dir, ".flsync", "machines"))
	for _, e := range ents {
		var m Machine
		if readJSON(filepath.Join(p.Dir, ".flsync", "machines", e.Name()), &m) == nil && m.User != "" {
			ms = append(ms, m)
		}
	}
	return ms
}

func (p *Project) Catalog() map[string]CatalogEntry {
	c := map[string]CatalogEntry{}
	readJSON(filepath.Join(p.Dir, ".flsync", "catalog.json"), &c)
	return c
}

func (p *Project) SetCatalog(name, url, note string) error {
	c := p.Catalog()
	k := normName(name)
	files := c[k].Files
	if url == "" && note == "" && len(files) == 0 {
		delete(c, k)
	} else {
		c[k] = CatalogEntry{URL: url, Note: note, Files: files}
	}
	return writeJSON(filepath.Join(p.Dir, ".flsync", "catalog.json"), c)
}

func (p *Project) myMachine(a *Analysis) Machine {
	me := Machine{User: p.Cfg.MachineID(), OS: OSName(), Updated: time.Now().Format("2006-01-02 15:04")}
	var prev Machine
	readJSON(filepath.Join(p.Dir, ".flsync", "machines", me.User+".json"), &prev)
	me.FLVersion = prev.FLVersion
	// FL version of .flp files changed locally = the version of *my* FL Studio
	g := p.git()
	if g.IsRepo() {
		if st, err := g.Status(); err == nil {
			for _, s := range st {
				if s.IsFLP && s.Kind != "deleted" {
					for _, f := range a.FLPs {
						if f.Rel == s.Path && f.Info.FLVersion != "" && cmpVersion(f.Info.FLVersion, me.FLVersion) > 0 {
							me.FLVersion = f.Info.FLVersion
						}
					}
				}
			}
		}
	} else {
		for _, f := range a.FLPs {
			if cmpVersion(f.Info.FLVersion, me.FLVersion) > 0 {
				me.FLVersion = f.Info.FLVersion
			}
		}
	}
	for _, ip := range ScanInstalled(p.Cfg, false) {
		me.Plugins = append(me.Plugins, InstalledPlugin{Name: ip.Name, Format: ip.Format, Version: ip.Version})
	}
	if prev.User != "" && prev.FLVersion == me.FLVersion && samePlugins(prev.Plugins, me.Plugins) {
		me.Updated = prev.Updated // don't create a diff just for the timestamp
	}
	return me
}

func samePlugins(a, b []InstalledPlugin) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Format != b[i].Format || a[i].Version != b[i].Version {
			return false
		}
	}
	return true
}

func cmpVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscanf(pa[i], "%d", &x)
		}
		if i < len(pb) {
			fmt.Sscanf(pb[i], "%d", &y)
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// PluginReport compares plugins used in the project with every participant.
func (p *Project) PluginReport(a *Analysis, machines []Machine) []PluginRow {
	cat := p.Catalog()
	rows := []PluginRow{}
	for _, pl := range a.Plugins {
		r := PluginRow{Plugin: pl}
		if c, ok := cat[normName(pl.Name)]; ok {
			r.Link, r.LinkNote, r.Files = c.URL, c.Note, c.Files
		}
		key := normName(pl.Name)
		var versions []string
		for _, m := range machines {
			ms := MachineStatus{User: m.User, OS: m.OS, State: "missing"}
			hasFLData := false
			var other []string
			for _, ip := range m.Plugins {
				if ip.Format == "FL" {
					hasFLData = true
				}
				if normName(ip.Name) != key {
					continue
				}
				if ip.Format == pl.Format || (pl.Format == "VST" && strings.HasPrefix(ip.Format, "VST")) {
					ms.State = "ok"
					if ip.Version != "" {
						ms.Version = ip.Version
					}
				} else {
					other = append(other, ip.Format)
				}
			}
			if ms.State != "ok" && len(other) > 0 {
				ms.State, ms.Note = "other-format", "есть как "+strings.Join(uniq(other), "/")
			}
			if ms.State == "missing" && pl.Format == "FL" {
				// native plugins: the FL database/installation may not be visible on
				// every machine, so don't raise a false alarm
				ms.State, ms.Note = "unknown", "встроенный FL — не нашёл в списке, проверь редакцию FL"
				if !hasFLData {
					ms.Note = "встроенный плагин FL"
				}
			}
			if ms.Version != "" {
				versions = append(versions, ms.Version)
			}
			r.Machines = append(r.Machines, ms)
		}
		var probs []string
		for _, ms := range r.Machines {
			switch ms.State {
			case "missing":
				probs = append(probs, "нет у "+ms.User)
			case "other-format":
				probs = append(probs, ms.User+": другой формат. Возможно, слетят пресеты")
			}
		}
		if len(uniq(versions)) > 1 {
			// состояние плагина из более новой версии старая часто не читает — сброс пресета
			probs = append(probs, "разные версии. Возможно, слетят пресеты")
		}
		if pl.Format == "AU" {
			for _, m := range machines {
				if m.OS == "Windows" {
					probs = append(probs, "AU нет на Windows — нужен VST3")
					break
				}
			}
		}
		r.Problem = strings.Join(probs, "; ")
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool { return (rows[i].Problem != "") && (rows[j].Problem == "") })
	return rows
}

func uniq(s []string) []string {
	m := map[string]bool{}
	var r []string
	for _, x := range s {
		if !m[x] {
			m[x] = true
			r = append(r, x)
		}
	}
	return r
}

type ProjectManifest struct {
	Generated string       `json:"generated"`
	FLVersion string       `json:"flVersion"`
	Plugins   []flp.Plugin `json:"plugins"`
	Samples   []SampleRef  `json:"samples"`
}

// WriteManifest writes PLUGINS.txt, .flsync/project.json and my inventory.
func (p *Project) WriteManifest(a *Analysis) error {
	me := p.myMachine(a)
	if err := writeJSON(filepath.Join(p.Dir, ".flsync", "machines", me.User+".json"), me); err != nil {
		return err
	}
	var flv string
	for _, f := range a.FLPs {
		if cmpVersion(f.Info.FLVersion, flv) > 0 {
			flv = f.Info.FLVersion
		}
	}
	var plugins []flp.Plugin
	for _, pl := range a.Plugins {
		pl.Path = "" // machine-specific, keep the manifest stable
		pl.UserName = ""
		plugins = append(plugins, pl)
	}
	var samples []SampleRef
	for _, s := range a.Samples {
		samples = append(samples, SampleRef{Name: s.Name, FLPs: s.FLPs, Embedded: s.Embedded})
	}
	if err := writeJSON(filepath.Join(p.Dir, ".flsync", "project.json"), ProjectManifest{FLVersion: flv, Plugins: plugins, Samples: samples}); err != nil {
		return err
	}
	machines := p.Machines()
	rows := p.PluginReport(a, machines)
	var b strings.Builder
	b.WriteString("ПЛАГИНЫ ПРОЕКТА — файл генерирует «Программа Узбека» при каждой отправке, руками не править\n")
	b.WriteString(strings.Repeat("=", 78) + "\n")
	if flv != "" {
		b.WriteString("Проект сохранён в FL Studio " + flv + "\n")
	}
	for _, m := range machines {
		fv := m.FLVersion
		if fv == "" {
			fv = "?"
		}
		b.WriteString(fmt.Sprintf("Участник: %-16s %-8s FL %s\n", m.User, m.OS, fv))
	}
	b.WriteString("\n")
	for _, r := range rows {
		mark := "✓"
		if r.Problem != "" {
			mark = "!"
		}
		vendor := ""
		if r.Vendor != "" {
			vendor = " — " + r.Vendor
		}
		b.WriteString(fmt.Sprintf("[%s] %-5s %s%s\n", mark, r.Format, r.Name, vendor))
		for _, ms := range r.Machines {
			st := map[string]string{"ok": "есть", "missing": "НЕТ", "other-format": "другой формат", "unknown": "?"}[ms.State]
			if ms.Version != "" {
				st += " v" + ms.Version
			}
			if ms.Note != "" && ms.State != "other-format" {
				st += " (" + ms.Note + ")"
			} else if ms.State == "other-format" {
				st += " (" + ms.Note + ")"
			}
			b.WriteString(fmt.Sprintf("        %-16s %s\n", ms.User+":", st))
		}
		if r.Problem != "" {
			b.WriteString("        Внимание: " + r.Problem + "\n")
		}
		if r.Link != "" {
			b.WriteString("        ↓ " + r.Link + "\n")
		}
	}
	path := filepath.Join(p.Dir, "PLUGINS.txt")
	if old, err := os.ReadFile(path); err == nil && string(old) == b.String() {
		return nil
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
