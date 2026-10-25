package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Неиспользуемый дубль, уже лежащий на GitHub, убирается только с этого компьютера: у друга и на GitHub остаётся.
func TestTrashLocal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", root)
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	must(t, func() error { _, err := cfg.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
	dir := filepath.Join(root, "A", "Beat")
	os.MkdirAll(filepath.Join(dir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(dir, "Track.flp"), synthFLP(filepath.Join(dir, "Audio", "kick.wav")), 0o644)
	os.WriteFile(filepath.Join(dir, "Audio", "kick.wav"), bytes.Repeat([]byte{1}, 4096), 0o644)
	take := bytes.Repeat([]byte{7}, 300<<10)
	os.WriteFile(filepath.Join(dir, "Audio", "take 1.wav"), take, 0o644)
	sh := DefaultShared()
	sh.AlwaysInclude = []string{"Audio/take 1.wav"}
	os.MkdirAll(filepath.Join(dir, ".flsync"), 0o755)
	writeJSON(filepath.Join(dir, ".flsync", "settings.json"), sh)
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}
	if _, err := p.CreateOnGitHub("Beat", nil); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	n, freed, err := p.TrashLocal([]string{"Audio/take 1.wav"})
	if err != nil || n != 1 || freed < int64(len(take)) {
		t.Fatalf("%d %d %v", n, freed, err)
	}
	if exists(filepath.Join(dir, "Audio", "take 1.wav")) {
		t.Fatal("файл остался")
	}
	if h := p.HiddenLocal(); len(h) != 1 || h[0] != "Audio/take 1.wav" {
		t.Fatalf("скрытые: %v", h)
	}
	// отправка не удаляет его с GitHub
	if _, err := p.Send("ещё", false); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	if out, _ := p.git().RunQuiet("ls-tree", "-r", "--name-only", "origin/main"); !strings.Contains(out, "take 1.wav") {
		if out2, _ := p.git().RunQuiet("ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(out2, "take 1.wav") {
			t.Fatalf("файл пропал из репозитория:\n%s", out2)
		}
	}
	if err := p.RestoreLocal([]string{"Audio/take 1.wav"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Audio", "take 1.wav")); !bytes.Equal(b, take) {
		t.Fatalf("не вернулся (%d байт)", len(b))
	}
}
