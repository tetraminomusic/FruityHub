package app

import (
	"bytes"
	"os"
	"os/exec"
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

// «История» показывает и коммиты, которые уже есть на сервере, но ещё не получены.
func TestHistoryIncoming(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	bare := filepath.Join(root, "srv.git")
	run(root, "init", "-q", "--bare", "-b", "main", bare)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	run(root, "clone", "-q", bare, a)
	os.WriteFile(filepath.Join(a, "t.flp"), []byte("1"), 0o644)
	run(a, "add", "-A")
	run(a, "commit", "-q", "-m", "первая")
	run(a, "push", "-q", "origin", "HEAD:main")
	run(root, "clone", "-q", bare, b)
	os.WriteFile(filepath.Join(b, "t.flp"), []byte("2"), 0o644)
	run(b, "commit", "-q", "-am", "от друга")
	run(b, "push", "-q", "origin", "HEAD:main")
	g := &Git{Dir: a}
	if len(g.Incoming(10)) != 0 {
		t.Fatal("до проверки сервера нового быть не должно")
	}
	run(a, "fetch", "-q", "origin")
	in := g.Incoming(10)
	if len(in) != 1 || !in[0].Incoming || in[0].Subject != "от друга" || len(in[0].Files) != 1 {
		t.Fatalf("%+v", in)
	}
	if l := g.Log(10); len(l) != 1 || l[0].Subject != "первая" {
		t.Fatalf("%+v", l)
	}
}
