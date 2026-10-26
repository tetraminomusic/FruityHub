package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Полный откат к версии из истории: файлы как тогда, появившиеся позже — удалены, несохранённое — в истории,
// и откат можно отменить откатом к версии «до».
func TestRollback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", root)
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	must(t, func() error { _, err := cfg.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
	dir := filepath.Join(root, "A", "Beat")
	os.MkdirAll(filepath.Join(dir, "Audio"), 0o755)
	kick := bytes.Repeat([]byte{1}, 5000)
	v1 := synthFLP(filepath.Join(dir, "Audio", "kick.wav"))
	os.WriteFile(filepath.Join(dir, "Track.flp"), v1, 0o644)
	os.WriteFile(filepath.Join(dir, "Audio", "kick.wav"), kick, 0o644)
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}
	if _, err := p.CreateOnGitHub("Beat", nil); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	c1, _ := p.git().RunQuiet("rev-parse", "HEAD")

	v2 := synthFLP(filepath.Join(dir, "Audio", "snare.wav"))
	os.WriteFile(filepath.Join(dir, "Track.flp"), v2, 0o644)
	os.WriteFile(filepath.Join(dir, "Audio", "snare.wav"), bytes.Repeat([]byte{2}, 7000), 0o644)
	if _, err := p.Send("снейр", false); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	// несохранённая правка
	v3 := append(append([]byte{}, v2...), 0)
	os.WriteFile(filepath.Join(dir, "Track.flp"), v3, 0o644)
	// только что достанная копия старой версии — откат её не трогает
	cp, err := p.RestoreVersion(strings.TrimSpace(c1), "Track.flp")
	if err != nil {
		t.Fatal(err)
	}

	res, err := p.RollbackTo(strings.TrimSpace(c1))
	if err != nil {
		t.Fatal(err, dump(p.Job))
	}
	if !res.Saved || res.Changed == 0 || res.Removed == 0 {
		t.Fatalf("%+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Track.flp")); !bytes.Equal(b, v1) {
		t.Fatal(".flp не стал как в первой версии")
	}
	if exists(filepath.Join(dir, "Audio", "snare.wav")) {
		t.Fatal("snare.wav появился позже — должен уйти")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Audio", "kick.wav")); !bytes.Equal(b, kick) {
		t.Fatal("kick.wav не тот")
	}
	if !exists(filepath.Join(dir, filepath.FromSlash(cp))) {
		t.Fatal("откат удалил только что достанную копию")
	}
	if msg, _ := p.git().RunQuiet("log", "-1", "--format=%s"); !strings.Contains(msg, "откат к версии") {
		t.Fatalf("коммит: %s", msg)
	}
	if st, _ := p.git().Status(); len(st) != 0 {
		t.Fatalf("после отката остались незаписанные изменения: %v", st)
	}
	// отменить откат
	if _, err := p.RollbackTo(res.Before); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Track.flp")); !bytes.Equal(b, v3) {
		t.Fatal("отмена отката не вернула несохранённую правку")
	}
	if !exists(filepath.Join(dir, "Audio", "snare.wav")) {
		t.Fatal("snare.wav не вернулся")
	}
	// отправка доходит до GitHub
	if _, err := p.Send("после отката", false); err != nil {
		t.Fatal(err, dump(p.Job))
	}
}
