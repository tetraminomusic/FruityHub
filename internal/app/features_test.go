package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flsync/internal/flp"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.7.10", "0.7.9", true}, {"0.7.0", "0.7.0", false}, {"0.6.9", "0.7.0", false}, {"1.0", "0.9.9", true}, {"0.7.0", "dev", false}, {"", "0.1", false}} {
		if newerVersion(c.a, c.b) != c.want {
			t.Errorf("newerVersion(%q,%q) != %v", c.a, c.b, c.want)
		}
	}
}

// Название проекта: своё только на этом компьютере; в Project info .flp — по кнопке и автоматически при добавлении.
func TestProjectNameAndFLTitle(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	dir := filepath.Join(root, "Ночной дрифт")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Track.flp"), synthFLP("Audio/vox.wav"), 0o644)
	cfg := &Config{}
	cfg.Projects = []LocalProject{{Path: dir}}
	lp := cfg.Project(dir)
	if lp.DisplayName() != "Ночной дрифт" {
		t.Fatal(lp.DisplayName())
	}
	lp.Name = "Дрифт v2"
	if cfg.Project(dir).DisplayName() != "Дрифт v2" {
		t.Fatal("своё название")
	}
	p := &Project{Dir: dir, Cfg: cfg}
	if p.MainFLP() != "Track.flp" {
		t.Fatal(p.MainFLP())
	}
	if changed, err := p.SetFLTitle("Дрифт v2"); err != nil || !changed {
		t.Fatal(changed, err)
	}
	f, _ := flp.ParseFile(filepath.Join(dir, "Track.flp"))
	if f.Title() != "Drift v2" || len(f.Info().Plugins) == 0 { // FL не понимает кириллицу — пишем латиницей
		t.Fatalf("title %q, плагины %d", f.Title(), len(f.Info().Plugins))
	}
	if got, _ := p.FLTitle(); got != "Drift v2" {
		t.Fatal(got)
	}
	if c := p.FLCompat(); c.Saved != "21.2.3.4004" {
		t.Fatalf("версия: %+v", c)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, ".flsync-local", "backup")); len(ents) != 1 {
		t.Fatal("перед правкой .flp должна остаться копия")
	}
}

// Перетащенная папка находится по имени и содержимому.
func TestLocateDropped(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	want := filepath.Join(root, "Music", "Биты", "Трек 5")
	os.MkdirAll(filepath.Join(want, "Audio"), 0o755)
	os.WriteFile(filepath.Join(want, "Трек 5.flp"), []byte("x"), 0o644)
	other := filepath.Join(root, "Desktop", "Трек 5") // такое же имя, другое содержимое
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "notes.txt"), []byte("x"), 0o644)
	s := &Server{cfg: &Config{}}
	got := s.LocateDropped(DroppedFolder{Name: "Трек 5", Children: []string{"Audio", "Трек 5.flp"}})
	if len(got) != 1 || got[0] != want {
		t.Fatalf("нашёл: %v", got)
	}
}

func TestMaskSecrets(t *testing.T) {
	in := "GET api.github.com/user?token=abc123 Authorization: Bearer ghp_abcdefghijklmnop key=zzz"
	out := maskSecrets(in)
	for _, bad := range []string{"abc123", "ghp_abcdefghijklmnop", "zzz"} {
		if contains(out, bad) {
			t.Fatalf("секрет в журнале: %s", out)
		}
	}
}

// Файл к ссылке плагина: загружается в хранилище проекта и скачивается другим участником.
func TestLinkFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	if _, err := cfg.Hub().Register("tetramino", "uzbek", "tokA"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "A", "Трек")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Track.flp"), synthFLP(""), 0o644)
	j := &Job{}
	p := &Project{Dir: dir, Cfg: cfg, Job: j}
	if _, err := p.CreateOnGitHub("Трек", nil); err != nil {
		t.Fatal(err, dump(j))
	}
	src := filepath.Join(root, "Serum presets.zip")
	os.WriteFile(src, []byte("PK-presets"), 0o644)
	if _, err := p.AttachLinkFile(j, "Serum", src); err != nil {
		t.Fatal(err, dump(j))
	}
	if fs := p.Catalog()[normName("Serum")].Files; len(fs) != 1 || fs[0].Name != "Serum presets.zip" {
		t.Fatalf("каталог: %+v", fs)
	}
	dst, err := p.FetchLinkFile(j, "Serum", "Serum presets.zip")
	if err != nil {
		t.Fatal(err, dump(j))
	}
	if b, _ := os.ReadFile(dst); string(b) != "PK-presets" {
		t.Fatalf("скачан не тот файл: %q", b)
	}
	if err := p.RemoveLinkFile("Serum", "Serum presets.zip"); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Catalog()[normName("Serum")]; ok {
		t.Fatal("пустая запись каталога должна удалиться")
	}
}

// Все адреса локального API регистрируются без конфликтов (иначе приложение падает при запуске).
func TestHandlerRoutes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := NewServer([]byte("<html>"))
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("конфликт маршрутов: %v", r)
		}
	}()
	s.Handler(47811)
}

func TestWindowsUpdateScripts(t *testing.T) {
	inner, outer := windowsUpdateScripts(`C:\Users\Public\Desktop\прога узбека.exe`, `C:\Users\user\AppData\Local\Temp\fruityhub-update-1\update.exe`, false, 1234)
	if !contains(inner, `FruityHub.exe`) || !contains(outer, "-Verb RunAs") || !contains(outer, "$p = 1234") {
		t.Fatalf("скрипты:\n%s\n%s", inner, outer)
	}
	if d := os.Getenv("PS_DUMP"); d != "" {
		os.WriteFile(filepath.Join(d, "inner.ps1"), []byte(inner), 0o644)
		os.WriteFile(filepath.Join(d, "outer.ps1"), []byte(outer), 0o644)
	}
}

// Сценарий обновления на Mac: подменяет бандл и откатывается, если не вышло.
func TestMacUpdateScript(t *testing.T) {
	d := t.TempDir()
	old := filepath.Join(d, "прога узбека.app")
	newApp := filepath.Join(d, "tmp", "FruityHub.app")
	os.MkdirAll(filepath.Join(old, "Contents"), 0o755)
	os.MkdirAll(filepath.Join(newApp, "Contents"), 0o755)
	os.WriteFile(filepath.Join(newApp, "Contents", "v"), []byte("new"), 0o644)
	script := macUpdateScript(old, newApp, filepath.Join(d, "FruityHub.app"), 999999)
	// без xattr/open (их нет на Linux) — проверяем саму подмену
	script = strings.ReplaceAll(strings.ReplaceAll(script, `xattr -dr com.apple.quarantine "$DEST" 2>/dev/null`, "true"), `open "$DEST"`, "true")
	sh := filepath.Join(d, "u.sh")
	os.WriteFile(sh, []byte(script), 0o755)
	if out, err := exec.Command("/bin/sh", sh).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if b, _ := os.ReadFile(filepath.Join(d, "FruityHub.app", "Contents", "v")); string(b) != "new" || exists(old) {
		t.Fatal("бандл не подменился")
	}
}

// Создание проекта упало после того, как репозиторий уже создан и записан на сервер:
// повтор продолжает с ним, а не делает второй репозиторий.
func TestCreateResumes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	if _, err := cfg.Hub().Register("rmaly", "uzbek", "tokA"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "cover fex")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "cover fex.flp"), synthFLP(""), 0o644)
	cfg.Projects = []LocalProject{{Path: dir}}
	// «упавшая» попытка: репозиторий и запись на сервере есть, в папке — git без адреса
	full, clone, err := GitHubCreateRepo(cfg, "FEX - Subways")
	must(t, err)
	_, err = cfg.Hub().AddProject("FEX - Subways", full, clone)
	must(t, err)
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}
	p.git().Run("init")
	if _, err := p.CreateOnGitHub("FEX - Subways", nil); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	if !strings.Contains(p.git().Remote(), strings.Split(full, "/")[1]) || strings.Contains(p.git().Remote(), "-2") {
		t.Fatalf("должен использоваться уже созданный репозиторий %s, а не новый: %s", full, p.git().Remote())
	}
	if ps, _ := cfg.Hub().Projects(0); len(ps) != 1 {
		t.Fatalf("на сервере должен остаться один проект: %d", len(ps))
	}
}

// Удаление репозитория проекта: список «моих» репозиториев, нет галочки delete_repo — понятная ошибка,
// после удаления — нет ни на GitHub, ни на сервере, у папки снят адрес.
func TestDeleteRepo(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	must(t, func() error { _, err := cfg.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
	dir := filepath.Join(root, "Трек")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Трек.flp"), synthFLP(""), 0o644)
	cfg.Projects = []LocalProject{{Path: dir}}
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}
	if _, err := p.CreateOnGitHub("Трек", nil); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	rs, err := MyProjectRepos(cfg)
	must(t, err)
	if len(rs) != 1 || rs[0].Local != dir || rs[0].Server != "Трек" {
		t.Fatalf("список репозиториев: %+v", rs)
	}
	gh.NoDeleteScope["alogin"] = true
	if err := DeleteGitHubRepo(cfg, rs[0].FullName); err == nil || !strings.Contains(err.Error(), "delete_repo") {
		t.Fatalf("без галочки delete_repo должна быть понятная ошибка: %v", err)
	}
	delete(gh.NoDeleteScope, "alogin")
	must(t, DeleteGitHubRepo(cfg, rs[0].FullName))
	if rs, _ := MyProjectRepos(cfg); len(rs) != 0 {
		t.Fatal("репозиторий должен исчезнуть с GitHub")
	}
	if ps, _ := cfg.Hub().Projects(0); len(ps) != 0 {
		t.Fatal("проект должен уйти с сервера")
	}
	if p.git().Remote() != "" || !exists(filepath.Join(dir, "Трек.flp")) {
		t.Fatal("у папки снимается адрес, файлы остаются")
	}
}

func TestInviteList(t *testing.T) {
	got := inviteList([]any{"Кирюха на MSI", " кирюха на маке ", ""})
	if len(got) != 2 || got[0] != "Кирюха на MSI" || got[1] != "кирюха на маке" {
		t.Fatalf("список: %q", got)
	}
	if got := inviteList("Кирюха на MSI, tetramino"); len(got) != 2 || got[0] != "Кирюха на MSI" {
		t.Fatalf("строка: %q", got)
	}
}
