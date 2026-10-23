package app

import (
	"archive/zip"
	"context"
	"errors"
	"flsync/internal/hub"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func utf16le(s string) []byte {
	var b []byte
	for _, c := range []byte(s) {
		b = append(b, c, 0)
	}
	return b
}

func TestBinStrings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	var b []byte
	b = append(b, 0, 1, 2, 0xff)
	b = append(b, []byte(`Arturia\DX7 V\resources`)...)
	b = append(b, 0, 0x90, 3)
	b = append(b, utf16le(`%APPDATA%\Vendor\Thing`)...)
	b = append(b, 0, 0, 7) // нечётное смещение для следующей UTF-16 строки
	b = append(b, utf16le(`SOFTWARE\Arturia\DX7 V`)...)
	b = append(b, 0, 0, 'a', 'b', 0)
	os.WriteFile(p, b, 0o644)
	got := map[string]bool{}
	binStrings(p, func(s string) { got[s] = true })
	for _, w := range []string{`Arturia\DX7 V\resources`, `%APPDATA%\Vendor\Thing`, `SOFTWARE\Arturia\DX7 V`} {
		if !got[w] {
			t.Errorf("не нашёл %q среди %v", w, got)
		}
	}
	if !regKeyRe.MatchString(`SOFTWARE\Arturia\DX7 V`) || cleanRegKey(`HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Windows`) != "" {
		t.Error("ключи реестра")
	}
	if cleanRegKey(`SOFTWARE\%s\%s`) != "" {
		t.Error("ключ с подстановкой не должен браться")
	}
}

// Таблица импорта: собираем крошечные программы под Windows и Mac и смотрим, что библиотеки читаются.
func TestBinImports(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main(){}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\ngo 1.21\n"), 0o644)
	for _, c := range []struct{ goos, want string }{{"windows", "kernel32.dll"}, {"darwin", "/usr/lib/libSystem.B.dylib"}} {
		out := filepath.Join(dir, "x-"+c.goos)
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS="+c.goos, "GOARCH=amd64", "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", c.goos, err, b)
		}
		libs := binImports(out)
		found := false
		for _, l := range libs {
			if strings.EqualFold(l, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: нет %s в %v", c.goos, c.want, libs)
		}
	}
}

// Плагин ссылается на папки, которые не названы его именем: они находятся разбором бинарника и уходят вместе с ним.
func TestPluginDepsFromBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	share := filepath.Join(home, ".local", "share")
	writeF(t, filepath.Join(share, "ArtShared", "Engine", "data", "x.bin"), "engine")
	writeF(t, filepath.Join(share, "ArtShared", "Other", "big.bin"), "other")           // не упомянута — не уходит
	writeF(t, filepath.Join(share, "ArtLib", "libart.dylib"), "lib")                    // библиотека по абсолютному пути
	writeF(t, filepath.Join(share, "Arturia", "Presets", "DX7 V", "a.preset"), "p")     // общая папка → только папка плагина
	writeF(t, filepath.Join(share, "Arturia", "Presets", "Pigments", "b.preset"), "p2") // чужой плагин
	writeF(t, filepath.Join(share, "Random", "keep.txt"), "r")                          // путь не дошёл до конца и не связан с плагином
	writeF(t, filepath.Join(home, ".config", "Fonts", "Arial", "a.ttf"), "font")        // системное — пропускаем
	plug := filepath.Join(home, ".vst3", "DX7 V.vst3")
	var bin []byte
	bin = append(bin, 0, 0)
	for _, s := range []string{
		`ArtShared\Engine\data`,
		share + "/ArtLib/libart.dylib",
		`/Arturia/Presets/`,
		`Random\Thing\Missing`,
		`Fonts/Arial/a.ttf`,
		`/home/builder/src/dx7/Main.cpp`,
	} {
		bin = append(bin, []byte(s)...)
		bin = append(bin, 0)
	}
	bin = append(bin, utf16le(`ArtShared\Engine`)...)
	writeF(t, filepath.Join(plug, "Contents", "MacOS", "DX7 V"), string(bin))

	take, skip, rep := findPluginDataEx("DX7 V", "Arturia", plug)
	got := map[string]string{}
	for _, d := range take {
		got[d.Root+":"+d.Rel] = d.Why
	}
	for _, w := range []string{"LOCALSHARE:ArtShared/Engine", "LOCALSHARE:ArtLib/libart.dylib", "LOCALSHARE:Arturia/Presets/DX7 V"} {
		if _, ok := got[w]; !ok {
			t.Errorf("не взял %s; взял %v (пропустил %v)", w, got, skip)
		}
	}
	if len(got) != 3 {
		t.Errorf("лишнее: %v", got)
	}
	if len(rep.Items) == 0 || rep.Binary == "" {
		t.Errorf("отчёт пустой: %+v", rep)
	}

	// в архиве: папки и отдельный файл библиотеки
	zf := filepath.Join(root, "p.zip")
	if err := zipPlugin(plug, zf, nil, take, skip); err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.OpenReader(zf)
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	zr.Close()
	for _, w := range []string{dataPrefix + "LOCALSHARE/ArtLib/libart.dylib", dataPrefix + "LOCALSHARE/ArtShared/Engine/data/x.bin",
		dataPrefix + "LOCALSHARE/Arturia/Presets/DX7 V/a.preset"} {
		if !names[w] {
			t.Errorf("в архиве нет %s: %v", w, names)
		}
	}

	// получатель
	recv := filepath.Join(root, "recv")
	t.Setenv("HOME", recv)
	os.MkdirAll(filepath.Join(recv, ".vst3"), 0o755)
	if _, err := InstallPluginZipEx(&Config{}, &Job{}, zf, "VST3", "Arturia"); err != nil {
		t.Fatal(err)
	}
	rs := filepath.Join(recv, ".local", "share")
	for _, p := range []string{"ArtLib/libart.dylib", "ArtShared/Engine/data/x.bin", "Arturia/Presets/DX7 V/a.preset"} {
		if !exists(filepath.Join(rs, p)) {
			t.Errorf("у получателя нет %s", p)
		}
	}
	if exists(filepath.Join(rs, "Arturia", "Presets", "Pigments")) || exists(filepath.Join(rs, "ArtShared", "Other")) {
		t.Error("ушло лишнее")
	}
}

// Библиотека рядом с одиночным файлом плагина (VST2 .dll) кладётся рядом с ним у получателя.
func TestSideLibInstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "send"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	src := filepath.Join(root, "send", "plugins", "Synth.clap")
	writeF(t, src, "plugin")
	writeF(t, filepath.Join(root, "send", "plugins", "synthcore.dll"), "core")
	data := []PluginData{{Root: sideRoot, Rel: "synthcore.dll", Size: 4, abs: filepath.Join(root, "send", "plugins", "synthcore.dll")}}
	zf := filepath.Join(root, "p.zip")
	if err := zipPlugin(src, zf, nil, data, nil); err != nil {
		t.Fatal(err)
	}
	recv := filepath.Join(root, "recv")
	t.Setenv("HOME", recv)
	os.MkdirAll(filepath.Join(recv, ".clap"), 0o755)
	rep, err := InstallPluginZipEx(&Config{}, &Job{}, zf, "CLAP", "")
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(filepath.Dir(rep.Path), "synthcore.dll")) {
		t.Fatalf("библиотека не легла рядом с плагином %s", rep.Path)
	}
}

func TestSplitDepPath(t *testing.T) {
	roots := []DataRoot{{Key: "APPSUP", Path: "/Users/x/Library/Application Support"}, {Key: "SYSLIB", Path: "/Library"},
		{Key: "SYSAPPSUP", Path: "/Library/Application Support"}}
	c := splitDepPath("/Library/Application Support/Arturia/DX7 V", roots)
	if len(c) != 1 || c[0].root.Key != "SYSAPPSUP" || c[0].rel != "Arturia/DX7 V" {
		t.Fatalf("абсолютный: %+v", c)
	}
	c = splitDepPath("Application Support/Arturia/DX7 V", roots)
	ok := false
	for _, x := range c {
		if x.root.Key == "APPSUP" && x.rel == "Arturia/DX7 V" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("хвост корня: %+v", c)
	}
	if c := splitDepPath(`%s\Arturia\%s\resources`, roots); len(c) != 0 {
		t.Fatalf("строка формата: %+v", c)
	}
	if c := splitDepPath(`..\..\etc\passwd`, roots); len(c) != 0 && c[0].rel != "" {
		for _, x := range c {
			if strings.Contains(x.rel, "..") {
				t.Fatalf("выход за корень: %+v", c)
			}
		}
	}
}

// Зависимости, которые уже есть у получателя, не передаются; общие для двух одновременных передач —
// едут только с одной; если та сорвалась — вторая запрашивается ещё раз.
func TestDepsDedupe(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfgA, cfgB := &Config{Server: hubURL}, &Config{Server: hubURL}
	if _, err := cfgA.Hub().Register("sender", "uzbek", "tokA"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfgB.Hub().Register("receiver", "uzbek", "tokB"); err != nil {
		t.Fatal(err)
	}
	homeA, homeB := filepath.Join(root, "A"), filepath.Join(root, "B")
	shared := filepath.Join(".local", "share", "Roland Cloud", "Shared")
	for _, f := range []string{"a.bin", "b.bin", "c.bin"} {
		writeF(t, filepath.Join(homeA, shared, f), "data-"+f)
	}
	writeF(t, filepath.Join(homeB, shared, "b.bin"), "mine") // это у получателя уже есть

	rs, err := cfgB.Hub().RequestPlugins("sender", []ReqItem{{Plugin: "JUNO-106", Format: "VST3"}, {Plugin: "D-50", Format: "VST3"}})
	if err != nil || len(rs) != 2 {
		t.Fatal(err, rs)
	}
	for _, r := range rs {
		if err := cfgA.Hub().Approve(r.ID, "plugin", r.Plugin+".vst3", "", "VST3"); err != nil {
			t.Fatal(err)
		}
	}
	mk := func() []PluginData { return []PluginData{{Root: "LOCALSHARE", Rel: "Roland Cloud/Shared"}} }
	data1, data2 := mk(), mk()
	t.Setenv("HOME", homeA)
	plug := filepath.Join(homeA, "x.vst3")
	for i, d := range [][]PluginData{data1, data2} {
		if _, err := cfgA.Hub().PutOffer(rs[i].ID, buildOffer("VST3", plug, "Roland Cloud", d)); err != nil {
			t.Fatal(err)
		}
	}
	// получатель отвечает (по порядку: первая передача закрепляет общие файлы за собой)
	t.Setenv("HOME", homeB)
	in, err := cfgB.Hub().Inbox()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]PluginRequest{}
	for _, r := range in.Ready {
		if r.OfferSeq == 0 {
			t.Fatalf("нет предложения: %+v", r)
		}
		byID[r.ID] = r
	}
	for _, r := range rs {
		rr := byID[r.ID]
		if err := answerOffer(cfgB, &rr); err != nil {
			t.Fatal(err)
		}
	}
	// отправитель получает ответы
	t.Setenv("HOME", homeA)
	var h1, h2 depHave
	if _, ok, err := cfgA.Hub().GetHave(rs[0].ID, &h1); !ok || err != nil {
		t.Fatal("нет ответа 1", err)
	}
	if _, ok, err := cfgA.Hub().GetHave(rs[1].ID, &h2); !ok || err != nil {
		t.Fatal("нет ответа 2", err)
	}
	if n, _ := applyHave(data1, h1); n != 1 {
		t.Fatalf("первая передача: уже есть только b.bin, а помечено %d (%v)", n, h1)
	}
	if n, _ := applyHave(data2, h2); n != 3 {
		t.Fatalf("вторая передача: всё уже есть или придёт с первой, а помечено %d (%v)", n, h2)
	}
	names := func(data []PluginData) []string {
		zf := filepath.Join(root, "z.zip")
		if err := zipPlugin(plug0(t, homeA), zf, nil, data, nil); err != nil {
			t.Fatal(err)
		}
		zr, _ := zip.OpenReader(zf)
		defer zr.Close()
		var out []string
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, dataPrefix) {
				out = append(out, filepath.Base(f.Name))
			}
		}
		return out
	}
	if got := names(data1); strings.Join(got, ",") != "a.bin,c.bin" {
		t.Fatalf("первая передача везёт %v", got)
	}
	if got := names(data2); len(got) != 0 {
		t.Fatalf("вторая передача не должна везти общие файлы: %v", got)
	}

	// первая передача сорвалась, вторая установилась → вторую запрашиваем ещё раз
	t.Setenv("HOME", homeB)
	cfgB.Hub().DoneRequest(rs[0].ID, false, "оборвалось")
	cfgB.Hub().DoneRequest(rs[1].ID, true, "")
	s := &Server{cfg: cfgB}
	s.reconcileClaims(nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, _ := cfgB.Hub().Request(rs[1].ID)
		if r.Status == ReqApproved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("вторая передача не перезапрошена: %s", r.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func plug0(t *testing.T, home string) string {
	p := filepath.Join(home, "x.vst3")
	writeF(t, filepath.Join(p, "Contents", "x86_64-linux", "x.so"), "bin")
	return p
}

// Пока получатель не подключился, данные не уходят (Expect: 100-continue) — прогресс не «застревает на 0,5 МБ».
func TestSendWaitsForReceiver(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	s, err := hub.New(hub.Options{DataDir: filepath.Join(root, "hub-data"), GitHubAPI: gh.Srv.URL, Code: "uzbek", StreamWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	cfgA, cfgB := &Config{Server: srv.URL}, &Config{Server: srv.URL}
	cfgA.Hub().Register("sender", "uzbek", "tokA")
	cfgB.Hub().Register("receiver", "uzbek", "tokB")
	rs, _ := cfgB.Hub().RequestPlugins("sender", []ReqItem{{Plugin: "Pro-Q 4", Format: "VST3"}})
	cfgA.Hub().Approve(rs[0].ID, "plugin", "Pro-Q 4.vst3", "", "VST3")
	big := filepath.Join(root, "big.zip")
	os.WriteFile(big, make([]byte, 8<<20), 0o644)
	j := &Job{}
	if err := cfgA.Hub().StreamSend(j, rs[0].ID, big); err != errNotOnline {
		t.Fatalf("ждали «не в сети», а %v", err)
	}
	if j.Progress == nil || !strings.HasPrefix(j.Progress.Label, "Жду") {
		t.Fatalf("данные не должны уходить, пока получатель не подключился: %+v", j.Progress)
	}
}

// Ошибки приложения уходят в журнал сервера; при выключенной галочке — нет.
func TestErrorReports(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	s, err := hub.New(hub.Options{DataDir: filepath.Join(root, "hub-data"), GitHubAPI: gh.Srv.URL, Code: "uzbek", AdminKey: "adm"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	cfg := &Config{Server: srv.URL, AdminKey: "adm"}
	if _, err := cfg.Hub().Register("kiryuha", "uzbek", "tokA"); err != nil {
		t.Fatal(err)
	}
	DevLog("job", 0, true, "■ Получаю JUNO-106 — ошибка: Access is denied token=ghp_SECRET123456")
	time.Sleep(50 * time.Millisecond)
	flushErrorReports(cfg)
	var out struct {
		Entries []struct{ Level, Src, Text string } `json:"entries"`
	}
	if err := cfg.Hub().admin("GET", "/api/admin/logs?since=0", nil, &out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range out.Entries {
		if e.Level == "client" && strings.HasPrefix(e.Src, "kiryuha") && strings.Contains(e.Text, "JUNO-106") {
			found = true
			if strings.Contains(e.Text, "SECRET") {
				t.Fatal("токен утёк в журнал")
			}
		}
	}
	if !found {
		t.Fatalf("ошибка не дошла: %+v", out.Entries)
	}
}

// .flp разбирается заново, только если изменился; кэш переживает перезапуск; установленные плагины берутся из прошлого сканирования.
func TestScanCaches(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { scanMu.Lock(); scanCache = nil; scanTime = time.Time{}; scanMu.Unlock() })
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	p := filepath.Join(root, "Track.flp")
	os.WriteFile(p, synthFLP("vox.wav"), 0o644)
	info, err := parseFLPCached(p)
	if err != nil || len(info.Plugins) == 0 {
		t.Fatal(err, info)
	}
	flpCacheMu.Lock()
	flpCache[p].Info.FLVersion = "из кэша"
	flpCacheMu.Unlock()
	if info, _ := parseFLPCached(p); info.FLVersion != "из кэша" {
		t.Fatal("неизменённый .flp должен браться из кэша")
	}
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(p, append(synthFLP("vox.wav"), 0), 0o644) // изменился
	if info, _ := parseFLPCached(p); info.FLVersion == "из кэша" {
		t.Fatal("изменённый .flp нужно разобрать заново")
	}
	time.Sleep(2500 * time.Millisecond)
	if !exists(flpCachePath()) {
		t.Fatal("кэш .flp не сохранился на диск")
	}

	// плагины: сканирование сохраняется и при «перезапуске» берётся с диска сразу
	plug := filepath.Join(root, "home", ".vst3", "Serum.vst3")
	writeF(t, filepath.Join(plug, "Contents", "x86_64-linux", "Serum.so"), "bin")
	cfg := &Config{}
	if FindInstalled(cfg, "Serum", "VST3") == nil {
		ScanInstalled(cfg, true)
	}
	time.Sleep(200 * time.Millisecond)
	scanMu.Lock()
	scanCache = nil // как будто приложение перезапустили
	scanMu.Unlock()
	os.RemoveAll(filepath.Join(root, "home", ".vst3", "Serum.vst3", "Contents")) // путь ещё есть
	got := ScanInstalled(cfg, false)
	found := false
	for _, ip := range got {
		if ip.Name == "Serum" && ip.Path == plug {
			found = true
		}
	}
	if !found {
		t.Fatalf("плагины должны взяться из прошлого сканирования: %+v", got)
	}
}

// Переустановка: файлы данных заменяются версией отправителя, старые уходят в резервную копию.
func TestReinstallOverwritesData(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	send, recv := filepath.Join(root, "send"), filepath.Join(root, "recv")
	t.Setenv("HOME", send)
	plug := filepath.Join(send, ".vst3", "TR-909.vst3")
	writeF(t, filepath.Join(plug, "Contents", "x86_64-linux", "TR-909.so"), "bin")
	writeF(t, filepath.Join(send, ".local", "share", "Roland Cloud", "TR-909", "kit.bin"), "NEW")
	data, _ := findPluginData("TR-909", "Roland", plug)
	zf := filepath.Join(root, "p.zip")
	if err := zipPlugin(plug, zf, nil, data, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", recv)
	kit := filepath.Join(recv, ".local", "share", "Roland Cloud", "TR-909", "kit.bin")
	writeF(t, kit, "OLD-BROKEN")
	os.MkdirAll(filepath.Join(recv, ".vst3"), 0o755)
	// обычная установка свои файлы не трогает
	if _, err := InstallPluginZipOpt(&Config{}, &Job{}, zf, "VST3", "Roland", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kit); string(b) != "OLD-BROKEN" {
		t.Fatal("обычная установка не должна заменять файлы")
	}
	// переустановка — заменяет, старое в резервной копии
	if _, err := InstallPluginZipOpt(&Config{}, &Job{}, zf, "VST3", "Roland", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kit); string(b) != "NEW" {
		t.Fatalf("переустановка должна заменить файл: %q", b)
	}
	bk, _ := filepath.Glob(filepath.Join(ConfigDir(), "plugin-backups", "*-данные", "*", "Roland Cloud", "TR-909", "kit.bin"))
	if len(bk) != 1 {
		t.Fatalf("старый файл должен уйти в резервную копию: %v", bk)
	}
	if b, _ := os.ReadFile(bk[0]); string(b) != "OLD-BROKEN" {
		t.Fatal("в резервной копии не тот файл")
	}
}

func TestActivationHints(t *testing.T) {
	if a := findVendorApp("Roland Cloud"); a == nil || a.name != "Roland Cloud Manager" {
		t.Fatal("Roland → Roland Cloud Manager")
	}
	if w := activationWarning("TR-909", "Roland"); !strings.Contains(w, "Roland Cloud Manager") {
		t.Fatal(w)
	}
	if findVendorApp("Xfer Records") != nil {
		t.Fatal("у Xfer нет программы-менеджера")
	}
}

func TestCheckPluginLocal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Cleanup(func() { scanMu.Lock(); scanCache = nil; scanMu.Unlock() })
	plug := filepath.Join(home, ".vst3", "Roland Cloud", "JUNO-106.vst3")
	missing := filepath.Join(home, "Roland Cloud", "JUNO-106", "Resources")
	writeF(t, filepath.Join(plug, "Contents", "MacOS", "JUNO-106"), "\x00\x00"+missing+"\x00\x00")
	cfg := &Config{}
	ScanInstalled(cfg, true)
	rep, err := CheckPlugin(cfg, &Job{}, "JUNO-106", "VST3")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range rep.Warnings {
		if strings.Contains(w.Text, missing) {
			found = true
		}
	}
	if !found {
		t.Fatalf("проверка должна найти отсутствующую папку %s: %+v", missing, rep.Warnings)
	}
}

// Отправитель после передачи ждёт итога у получателя; частичная установка — предупреждение, а не ошибка.
func TestSenderSeesReceiverResult(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfgA, cfgB := &Config{Server: hubURL}, &Config{Server: hubURL}
	cfgA.Hub().Register("sender", "uzbek", "tokA")
	cfgB.Hub().Register("receiver", "uzbek", "tokB")
	rs, _ := cfgB.Hub().RequestPlugins("sender", []ReqItem{{Plugin: "TR-909", Format: "VST3"}})
	cfgA.Hub().Approve(rs[0].ID, "plugin", "TR-909.vst3", "", "VST3")
	go func() {
		time.Sleep(500 * time.Millisecond)
		cfgB.Hub().DonePartial(rs[0].ID, "не всё из данных легло")
	}()
	j := &Job{}
	j.ctx, j.cancel = context.WithCancel(context.Background())
	r := rs[0]
	r.FromNick = "receiver"
	waitReceiverResult(cfgA, j, &r)
	if !strings.Contains(strings.Join(j.Steps, "\n"), "частично") {
		t.Fatalf("отправитель должен узнать о частичной установке: %v", j.Steps)
	}
}

func TestPluginDiagAndGuessVendor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Cleanup(func() { scanMu.Lock(); scanCache = nil; scanMu.Unlock() })
	plug := filepath.Join(home, ".vst3", "Arturia", "DX7 V.vst3")
	writeF(t, filepath.Join(plug, "Contents", "MacOS", "DX7 V"), "bin")
	writeF(t, filepath.Join(plug, "Contents", "Resources", "moduleinfo.json"), `{"Name":"DX7 V","Factory Info":{"Vendor":"Arturia","URL":""}}`)
	writeF(t, filepath.Join(home, ".local", "share", "Arturia", "DX7 V", "resources", "gui_xml", "gui-activation-generated.xml"), "<xml/>")
	cfg := &Config{}
	ScanInstalled(cfg, true)
	if v := guessVendor(cfg, plug); v != "Arturia" {
		t.Fatalf("производитель: %q", v)
	}
	// без производителя в базе папка ресурсов всё равно находится (по угаданному производителю)
	data, _ := findPluginData("DX7 V", "", plug)
	if len(data) != 1 || data[0].Rel != "Arturia/DX7 V" {
		t.Fatalf("данные: %+v", data)
	}
	txt, err := PluginDiag(cfg, &Job{}, "DX7 V", "VST3")
	if err != nil || !strings.Contains(txt, "Arturia/DX7 V") || !strings.Contains(txt, "Производитель: Arturia") {
		t.Fatalf("%v\n%s", err, txt)
	}
}

// Как на ноутбуке с несколькими плагинами Arturia: уезжает папка самого плагина (и его пресеты),
// а не общая папка Arturia и не папки других инструментов.
func TestArturiaOwnFolderOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Cleanup(func() { scanMu.Lock(); scanCache = nil; scanMu.Unlock() })
	share := filepath.Join(home, ".local", "share", "Arturia")
	writeF(t, filepath.Join(share, "Jup-8 V4", "resources", "gui_xml", "gui-activation-generated.xml"), "<x/>")
	writeF(t, filepath.Join(share, "DX7 V", "resources", "a.xml"), "<x/>")
	writeF(t, filepath.Join(share, "Presets", "Jup-8 V4", "p.preset"), "p")
	writeF(t, filepath.Join(share, "Presets", "DX7 V", "p.preset"), "p")
	writeF(t, filepath.Join(home, ".vst3", "DX7 V.vst3", "Contents", "MacOS", "DX7 V"), "bin")
	plug := filepath.Join(home, ".vst3", "Jup-8 V4.vst3")
	writeF(t, filepath.Join(plug, "Contents", "MacOS", "Jup-8 V4"), "\x00Arturia/NoSuchFolder/x\x00")
	ScanInstalled(&Config{}, true)
	take, skip := findPluginData("Jup-8 V4", "Arturia", plug)
	got := []string{}
	for _, d := range take {
		got = append(got, d.Rel)
	}
	if strings.Join(got, ",") != "Arturia/Jup-8 V4,Arturia/Presets/Jup-8 V4" {
		t.Fatalf("уезжает %v (пропущено %v)", got, skip)
	}
}

func TestRegistryOwnValues(t *testing.T) {
	keys := []string{normName("Jup-8 V4")}
	if !regForPlugin(RegValue{Key: `SOFTWARE\Arturia\Jup-8 V4`, Name: "installdir", Data: `C:\ProgramData\Arturia\Jup-8 V4`}, keys) {
		t.Fatal("ключ плагина")
	}
	if regForPlugin(RegValue{Key: `SOFTWARE\Arturia\DX7 V`, Name: "installdir", Data: `C:\ProgramData\Arturia\DX7 V`}, keys) {
		t.Fatal("чужой продукт")
	}
	if !regVendorRoot(`SOFTWARE\Arturia`) || regVendorRoot(`SOFTWARE\Arturia\DX7 V`) {
		t.Fatal("корень производителя")
	}
}

// Проект начали отдельно на двух компьютерах и подключили к одному репозиторию: FruityHub спрашивает,
// что оставить — версию с GitHub, свою или обе.
func TestReceiveUnrelatedHistories(t *testing.T) {
	setup := func(t *testing.T) (string, *Project, func(dir string, args ...string) string) {
		root := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
		t.Setenv("HOME", filepath.Join(root, "home"))
		run := func(dir string, args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
			return string(out)
		}
		remote := filepath.Join(root, "remote.git")
		os.MkdirAll(remote, 0o755)
		run(remote, "init", "-q", "--bare", "-b", "main")
		a := filepath.Join(root, "A")
		writeF(t, filepath.Join(a, "Track.flp"), "FLP-A")
		writeF(t, filepath.Join(a, "Audio", "vox.wav"), "RIFF")
		writeF(t, filepath.Join(a, "Audio", "only-github.wav"), "GH")
		run(a, "init", "-q", "-b", "main")
		run(a, "add", "-A")
		run(a, "commit", "-q", "-m", "A")
		run(a, "remote", "add", "origin", remote)
		run(a, "push", "-q", "origin", "main")
		b := filepath.Join(root, "B")
		writeF(t, filepath.Join(b, "Track.flp"), "FLP-B")
		writeF(t, filepath.Join(b, "Audio", "vox.wav"), "RIFF")
		writeF(t, filepath.Join(b, "Audio", "only-mac.wav"), "MAC")
		run(b, "init", "-q", "-b", "main")
		run(b, "add", "-A")
		run(b, "commit", "-q", "-m", "B")
		run(b, "remote", "add", "origin", remote)
		return b, &Project{Dir: b, Cfg: &Config{UserName: "mac"}, Job: &Job{}}, run
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	t.Run("спросить", func(t *testing.T) {
		_, P, _ := setup(t)
		if _, err := P.fetchMerge(); !errors.Is(err, errNeedChoice) {
			t.Fatalf("должен спросить, а не %v", err)
		}
	})
	t.Run("обе", func(t *testing.T) {
		b, P, _ := setup(t)
		P.Resolve = "both"
		conf, err := P.fetchMerge()
		if err != nil || len(conf) != 1 || read(filepath.Join(b, "Track.flp")) != "FLP-A" || read(filepath.Join(b, filepath.FromSlash(conf[0]))) != "FLP-B" {
			t.Fatalf("%v %v", err, conf)
		}
		// потом по файлу выбираем свою
		if err := P.KeepConflictVersion(conf[0], "mine"); err != nil || read(filepath.Join(b, "Track.flp")) != "FLP-B" || exists(filepath.Join(b, filepath.FromSlash(conf[0]))) {
			t.Fatalf("оставить свою: %v", err)
		}
	})
	t.Run("с GitHub", func(t *testing.T) {
		b, P, _ := setup(t)
		P.Resolve = "theirs"
		if _, err := P.fetchMerge(); err != nil {
			t.Fatal(err)
		}
		if read(filepath.Join(b, "Track.flp")) != "FLP-A" || !exists(filepath.Join(b, "Audio", "only-github.wav")) || exists(filepath.Join(b, "Audio", "only-mac.wav")) {
			t.Fatal("проект должен стать как на GitHub")
		}
		bk, _ := filepath.Glob(filepath.Join(b, "Backup", "FruityHub*", "Track.flp"))
		mac, _ := filepath.Glob(filepath.Join(b, "Backup", "FruityHub*", "Audio", "only-mac.wav"))
		if len(bk) != 1 || read(bk[0]) != "FLP-B" || len(mac) != 1 {
			t.Fatalf("своя версия должна остаться в Backup: %v %v", bk, mac)
		}
	})
	t.Run("моя", func(t *testing.T) {
		b, P, run := setup(t)
		P.Resolve = "mine"
		if _, err := P.fetchMerge(); err != nil {
			t.Fatal(err)
		}
		if read(filepath.Join(b, "Track.flp")) != "FLP-B" || exists(filepath.Join(b, "Audio", "only-github.wav")) || !exists(filepath.Join(b, "Audio", "only-mac.wav")) {
			t.Fatal("должна остаться своя версия")
		}
		// и её можно отправить без конфликта (история с GitHub внутри)
		run(b, "push", "-q", "origin", "HEAD:main")
	})
}
