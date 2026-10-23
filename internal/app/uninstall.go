package app

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------- «Удалить плагин у себя»: сам плагин (все копии), его данные и запись в базе плагинов FL ----------

type UninstallItem struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // plugin, data, fldb
	Size    int64  `json:"size"`
	Why     string `json:"why,omitempty"`
	Checked bool   `json:"checked"` // отмечено по умолчанию
	Admin   bool   `json:"admin,omitempty"`
}

type UninstallPlan struct {
	Name   string          `json:"name"`
	Format string          `json:"format"`
	Items  []UninstallItem `json:"items"`
}

// uninstallPlan — что можно удалить. Плагин — только файлы/пакеты с расширением плагина;
// данные — то же, что ушло бы с ним при передаче (их нашёл findPluginDataEx), отмечены по умолчанию
// только «свои» папки; большие библиотеки — без галочки.
func uninstallPlan(cfg *Config, name, format string) (*UninstallPlan, error) {
	ip := FindInstalled(cfg, name, format)
	if ip == nil {
		ScanInstalled(cfg, true)
		ip = FindInstalled(cfg, name, format)
	}
	if ip == nil {
		return nil, errors.New("не нашёл у тебя этот плагин")
	}
	plan := &UninstallPlan{Name: ip.Name, Format: ip.Format}
	seen := map[string]bool{}
	add := func(it UninstallItem) {
		k := strings.ToLower(filepath.Clean(it.Path))
		if it.Path == "" || seen[k] || !exists(it.Path) {
			return
		}
		seen[k] = true
		it.Admin = !writableDir(filepath.Dir(it.Path))
		plan.Items = append(plan.Items, it)
	}
	var copies []string
	key := normName(ip.Name)
	for _, p := range ScanInstalled(cfg, false) {
		path := strings.TrimRight(p.Path, `/\`)
		if normName(p.Name) != key || p.Format != ip.Format || path == "" {
			continue
		}
		if pluginExts[strings.ToLower(filepath.Ext(path))] == "" { // не плагин — не трогаем
			continue
		}
		if !seen[strings.ToLower(filepath.Clean(path))] && exists(path) {
			copies = append(copies, path)
		}
		add(UninstallItem{Path: path, Kind: "plugin", Size: dirSize(path), Checked: true, Why: "сам плагин"})
	}
	if len(copies) == 0 {
		return nil, errors.New("не нашёл файл плагина на диске")
	}
	vendor := pluginVendor(ip.Vendor, ip)
	if vendor == "" {
		vendor = guessVendor(cfg, copies[0])
	}
	take, skip, _ := findPluginDataEx(ip.Name, vendor, copies[0])
	dataPath := func(d PluginData) string {
		if d.abs != "" {
			return d.abs
		}
		r := dataRoot(d.Root)
		if r == nil || strings.Trim(d.Rel, "/") == "" {
			return ""
		}
		return filepath.Join(r.Path, filepath.FromSlash(d.Rel))
	}
	for _, d := range take {
		add(UninstallItem{Path: dataPath(d), Kind: "data", Size: d.Size, Why: d.Why, Checked: true})
	}
	for _, d := range skip {
		why := d.Note
		if d.Why != "" {
			why = d.Why + " · " + why
		}
		add(UninstallItem{Path: dataPath(d), Kind: "data", Size: d.Size, Why: why})
	}
	for _, f := range flDBFilesFor(copies) {
		add(UninstallItem{Path: f, Kind: "fldb", Checked: true, Why: "запись в базе плагинов FL"})
	}
	return plan, nil
}

// flDBFilesFor — .nfo/.fst из базы плагинов FL, которые указывают на эти файлы плагина.
func flDBFilesFor(copies []string) []string {
	want := map[string]bool{}
	for _, c := range copies {
		want[strings.ToLower(filepath.Clean(c))] = true
	}
	var out []string
	filepath.WalkDir(flPluginDBDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".nfo") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff")), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "ps_file_filename_0") {
				if want[strings.ToLower(filepath.Clean(strings.TrimRight(strings.TrimSpace(v), `/\`)))] {
					out = append(out, path)
					if fst := strings.TrimSuffix(path, filepath.Ext(path)) + ".fst"; exists(fst) {
						out = append(out, fst)
					}
				}
				break
			}
		}
		return nil
	})
	return out
}

// UninstallPlugin убирает отмеченное в Корзину (защищённое — в резервную папку FruityHub с правами администратора).
func UninstallPlugin(cfg *Config, job *Job, name, format string, paths []string) (string, error) {
	plan, err := uninstallPlan(cfg, name, format)
	if err != nil {
		return "", err
	}
	byPath := map[string]UninstallItem{}
	for _, it := range plan.Items {
		byPath[strings.ToLower(filepath.Clean(it.Path))] = it
	}
	var sel []UninstallItem
	for _, p := range paths {
		if it, ok := byPath[strings.ToLower(filepath.Clean(p))]; ok {
			sel = append(sel, it)
		}
	}
	if len(sel) == 0 {
		return "", errors.New("ничего не выбрано")
	}
	bak := filepath.Join(ConfigDir(), "plugin-backups", "удалено "+time.Now().Format("20060102-150405"))
	batch := &elevBatch{}
	var done, toAdmin []string
	for _, it := range sel {
		if !it.Admin {
			var err error
			if st, e := os.Stat(it.Path); e == nil && st.IsDir() {
				err = moveDirToTrash(it.Path)
			} else {
				err = moveToTrash(it.Path)
			}
			if err == nil && !exists(it.Path) {
				done = append(done, it.Path)
				job.Logf("✓ В Корзину: %s", it.Path)
				continue
			}
		}
		batch.MoveAway(it.Path, bak)
		toAdmin = append(toAdmin, it.Path)
	}
	var problems []string
	if !batch.Empty() {
		problems = batch.Run(job)
		for _, p := range toAdmin {
			if !exists(p) {
				done = append(done, p)
				job.Logf("✓ Убрано в %s: %s", bak, p)
			}
		}
	}
	ScanInstalled(cfg, true)
	LogActivity("", "info", fmt.Sprintf("Удалён плагин %s (%s): %d объектов", plan.Name, plan.Format, len(done)))
	if len(done) < len(sel) {
		msg := "не всё удалилось (закрой FL Studio и попробуй ещё раз)"
		if len(problems) > 0 {
			msg += ": " + strings.Join(problems, "; ")
		}
		return "", errors.New(msg)
	}
	res := fmt.Sprintf("%s удалён: %d объектов в Корзине", plan.Name, len(done))
	if len(toAdmin) > 0 {
		res += " (защищённые — в " + bak + ")"
	}
	job.Logf("%s. Перезапусти FL Studio", res)
	return res, nil
}
