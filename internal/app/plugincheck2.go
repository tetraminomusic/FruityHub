package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ---------- «Проверить плагин» ----------
// Всё, что можно узнать на этом компьютере, почему плагин «появился, но не открывается»:
// загружается ли его файл (на Windows — пробной загрузкой в отдельном процессе), каких библиотек не хватает,
// есть ли папки, на которые он ссылается, стоит ли программа производителя для активации.

// CheckPlugin проверяет установленный плагин и возвращает отчёт (как после установки).
func CheckPlugin(cfg *Config, job *Job, name, format string) (*InstallReport, error) {
	ip := FindInstalled(cfg, name, format)
	if ip == nil {
		ScanInstalled(cfg, true)
		ip = FindInstalled(cfg, name, format)
	}
	if ip == nil || ip.Path == "" {
		return nil, errors.New("не нашёл у тебя файл этого плагина")
	}
	rep := &InstallReport{ID: "check-" + time.Now().Format("150405"), Plugin: ip.Name, From: "проверка на этом компьютере", Path: ip.Path,
		At: time.Now().Unix(), Data: []string{}, Warnings: []Warning{}}
	job.Logf("Проверяю %s (%s)", ip.Name, ip.Path)
	vendor := pluginVendor(ip.Vendor, ip)
	checkInstalled(job, rep, ip.Format, vendor)

	bin := pluginBinary(ip.Path)
	if bin == "" {
		rep.Warnings = append(rep.Warnings, Warning{Text: "Внутри пакета плагина нет исполняемого файла для этой системы — плагин неполный или собран под другую систему. Переустанови его."})
	} else {
		job.Logf("Пробую загрузить %s…", filepath.Base(bin))
		ok, msg := probePluginLoad(bin)
		switch {
		case ok:
			rep.Data = append(rep.Data, "Файл плагина загружается: "+msg)
		case msg != "":
			rep.Warnings = append(rep.Warnings, Warning{Text: "Файл плагина не загружается: " + msg})
		}
		// пути, на которые ссылается плагин, а их здесь нет
		keys := []string{normName(ip.Name)}
		for _, p := range missingRefPaths(bin, keys, normName(vendor)) {
			rep.Warnings = append(rep.Warnings, Warning{Text: "Плагин ссылается на " + p + " — такой папки на этом компьютере нет. Её ставит установщик плагина или программа производителя; можно переустановить плагин у друга (с данными)."})
		}
	}
	if w := arturiaResourcesCheck(ip.Name, vendor); w != "" {
		rep.Warnings = append(rep.Warnings, Warning{Text: w})
	}
	if len(rep.Warnings) == 0 {
		rep.Data = append(rep.Data, "Проблем не нашёл. Если в FL плагин всё равно не открывается — это почти всегда лицензия/активация (см. программу производителя) или нужен «Make bridged».")
	}
	addReport(*rep)
	markReportSeen(rep.ID)
	return rep, nil
}

// missingRefPaths — абсолютные пути из файла плагина, связанные с его именем или производителем, которых нет.
func missingRefPaths(bin string, keys []string, vkey string) []string {
	info := analyzeBinary(bin)
	seen := map[string]bool{}
	var out []string
	for _, s := range info.paths {
		p := strings.Trim(strings.TrimSpace(s), `"'`)
		if m := envVarRe.FindStringSubmatch(p); m != nil {
			if v := os.Getenv(m[1]); v != "" {
				p = v + p[len(m[0]):]
			} else {
				continue
			}
		}
		if loc := fmtSpecRe.FindStringIndex(p); loc != nil {
			p = p[:loc[0]]
		}
		p = strings.TrimRight(p, `/\`)
		if len(p) < 6 || !filepath.IsAbs(p) || strings.ContainsAny(p, "*?<>|\n") || codeExts[strings.ToLower(filepath.Ext(p))] {
			continue
		}
		related := false
		for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
			if vendorMatch(seg, vkey) || anyNameMatch(seg, keys) {
				related = true
			}
		}
		lp := strings.ToLower(p)
		if !related || seen[lp] || exists(p) || strings.Contains(lp, `\users\`) && !strings.Contains(lp, strings.ToLower(os.Getenv("USERNAME"))) {
			continue
		}
		// путь к исходникам у разработчика (D:\build\...) — не наш случай: проверяем, что корень диска/папки существует
		if vol := filepath.VolumeName(p); vol != "" && !exists(vol+`\`) {
			continue
		}
		seen[lp] = true
		out = append(out, p)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

// probePluginLoad загружает файл плагина в отдельном процессе FruityHub (если плагин упадёт — упадёт он, не приложение).
func probePluginLoad(bin string) (bool, string) {
	if runtime.GOOS != "windows" {
		return false, ""
	}
	exe, err := os.Executable()
	if err != nil {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), "FRUITYHUB_PROBE="+bin)
	cmd.Dir = filepath.Dir(bin)
	hideWindow(cmd)
	out, err := cmd.Output()
	res := strings.TrimSpace(string(out))
	switch {
	case ctx.Err() != nil:
		return false, "плагин завис при загрузке (30 секунд) — возможно, ждёт окно активации или лицензию"
	case strings.HasPrefix(res, "OK"):
		return true, strings.TrimSpace(strings.TrimPrefix(res, "OK"))
	case strings.HasPrefix(res, "ERR"):
		return false, strings.TrimSpace(strings.TrimPrefix(res, "ERR"))
	case err != nil:
		return false, fmt.Sprintf("плагин аварийно завершился при загрузке (%v) — часто так бывает из-за защиты/лицензии или повреждённых файлов; переустанови его", err)
	}
	return false, ""
}

// ---------- программы производителей для активации ----------

type vendorApp struct {
	keys []string // как производитель называется в плагинах (normName)
	name string   // программа
	dirs []string // как называется её папка
	mac  string   // приложение на Mac
	what string   // что там сделать
}

var vendorApps = []vendorApp{
	{[]string{"roland", "rolandcloud"}, "Roland Cloud Manager", []string{"Roland Cloud Manager"}, "Roland Cloud Manager.app", "войди в аккаунт Roland и нажми Install / Activate у этого плагина (нужна своя подписка Roland Cloud или купленная лицензия)"},
	{[]string{"arturia"}, "Arturia Software Center", []string{"Arturia Software Center"}, "Arturia Software Center.app", "войди в аккаунт Arturia и активируй плагин (или включи бесплатный пробный период)"},
	{[]string{"nativeinstruments", "izotope"}, "Native Access", []string{"Native Access"}, "Native Access.app", "войди в аккаунт и активируй продукт"},
	{[]string{"waves"}, "Waves Central", []string{"Waves Central"}, "Waves Central.app", "войди в аккаунт Waves и активируй лицензию на этот компьютер"},
	{[]string{"pluginalliance", "brainworx"}, "Plugin Alliance Installation Manager", []string{"Plugin Alliance", "Plugin Alliance Installation Manager"}, "Plugin Alliance Installation Manager.app", "войди в аккаунт и активируй плагин"},
	{[]string{"softube"}, "Softube Central", []string{"Softube Central"}, "Softube Central.app", "войди в аккаунт и активируй плагин"},
	{[]string{"ikmultimedia"}, "IK Product Manager", []string{"IK Product Manager"}, "IK Product Manager.app", "войди в аккаунт и авторизуй продукт"},
	{[]string{"toontrack"}, "Toontrack Product Manager", []string{"Toontrack Product Manager"}, "Toontrack Product Manager.app", "войди в аккаунт и авторизуй продукт"},
	{[]string{"ujam"}, "UJAM Finder", []string{"UJAM Finder", "UJAM App"}, "UJAM Finder.app", "войди в аккаунт и активируй продукт"},
	{[]string{"spitfireaudio", "spitfire"}, "Spitfire Audio", []string{"Spitfire Audio"}, "Spitfire Audio.app", "войди в аккаунт и скачай/авторизуй библиотеку"},
	{[]string{"universalaudio", "uaudio"}, "UA Connect", []string{"UA Connect"}, "UA Connect.app", "войди в аккаунт и активируй плагин"},
}

func findVendorApp(vendor string) *vendorApp {
	v := normName(vendor)
	if v == "" {
		return nil
	}
	for i, a := range vendorApps {
		for _, k := range a.keys {
			if v == k || strings.HasPrefix(v, k) {
				return &vendorApps[i]
			}
		}
	}
	return nil
}

// vendorAppInstalled ищет программу производителя на этом компьютере.
func vendorAppInstalled(a *vendorApp) bool {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return exists(filepath.Join("/Applications", a.mac)) || exists(filepath.Join(home, "Applications", a.mac))
	}
	if runtime.GOOS != "windows" {
		return false
	}
	var roots []string
	for _, e := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if v := os.Getenv(e); v != "" {
			roots = append(roots, v)
		}
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		roots = append(roots, filepath.Join(v, "Programs"))
	}
	for _, r := range roots {
		for _, d := range a.dirs {
			if exists(filepath.Join(r, d)) {
				return true
			}
		}
		// бывает в папке производителя: Program Files\Arturia\Arturia Software Center
		ents, _ := os.ReadDir(r)
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			for _, d := range a.dirs {
				if exists(filepath.Join(r, e.Name(), d)) {
					return true
				}
			}
		}
	}
	return false
}

// activationWarning — понятный текст про активацию: какая программа нужна и стоит ли она.
func activationWarning(plugin, vendor string) string {
	a := findVendorApp(vendor)
	if a == nil {
		return "Плагины " + vendor + " обычно активируются своей лицензией (аккаунт, ключ или iLok). Лицензия при передаче не переносится: если в FL серое окно или просит ключ — поставь плагин установщиком производителя и войди в свой аккаунт."
	}
	if vendorAppInstalled(a) {
		return a.name + " на этом компьютере есть. Если " + plugin + " не открывается или работает в демо-режиме — открой " + a.name + ", " + a.what + ". Файлы плагина уже на месте, лицензия при передаче не переносится."
	}
	return plugin + " работает только после активации через " + a.name + ", а её на этом компьютере нет. Скачай " + a.name + " с сайта производителя, " + a.what + ". Без этого перенесённый плагин не откроется (лицензия при передаче не переносится)."
}

// arturiaResourcesCheck: у Arturia интерфейс плагина лежит отдельно от файла плагина — в папке ресурсов
// (Windows: C:\ProgramData\Arturia\<плагин>\resources, Mac: /Library/Arturia/<плагин>/resources).
// Без неё — «Error on GUI loading … gui-activation-generated.xml not found».
func arturiaResourcesCheck(plugin, vendor string) string {
	if !strings.HasPrefix(normName(vendor), "arturia") && !strings.HasPrefix(normName(plugin), "arturia") {
		return ""
	}
	var cands []string
	switch runtime.GOOS {
	case "windows":
		for _, e := range []string{"ProgramData", "ProgramFiles"} {
			if v := os.Getenv(e); v != "" {
				cands = append(cands, filepath.Join(v, "Arturia", plugin, "resources"))
			}
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		cands = []string{filepath.Join("/Library/Arturia", plugin, "resources"), filepath.Join(home, "Library", "Arturia", plugin, "resources")}
	default:
		return ""
	}
	for _, c := range cands {
		if !dirExists(c) {
			continue
		}
		if f := filepath.Join(c, "gui_xml", "gui-activation-generated.xml"); !exists(f) {
			return "В папке ресурсов Arturia (" + c + ") нет окна активации gui_xml/gui-activation-generated.xml — из-за этого «Error on GUI loading». " +
				"Нажми «Переустановить у …» (придут все файлы заново). Если у друга этого файла тоже нет — поставь плагин через Arturia Software Center."
		}
		return ""
	}
	return "Не нашёл папку ресурсов Arturia для " + plugin + " (ожидал " + cands[0] + "). Без неё плагин показывает «Error on GUI loading». " +
		"Нажми «Переустановить у …» — папка придёт вместе с плагином; если у друга ресурсы лежат в другом месте (своя папка в Arturia Software Center), поставь плагин через Arturia Software Center."
}
