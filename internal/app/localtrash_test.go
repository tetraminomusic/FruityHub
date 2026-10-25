package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	writeJSON(filepath.Join(dir, ".flsync", "settings.json"), DefaultShared()) // сняли «всегда отправлять»
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

// FL удалила сэмпл синта — перед отправкой FruityHub это видит и возвращает его из прошлой версии.
func TestLostSamples(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", root)
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	must(t, func() error { _, err := cfg.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
	dir := filepath.Join(root, "A", "Beat")
	os.MkdirAll(filepath.Join(dir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(dir, "Track.flp"), synthFLP(filepath.Join(dir, "Audio", "pad.wav")), 0o644)
	pad := bytes.Repeat([]byte{3}, 200<<10)
	os.WriteFile(filepath.Join(dir, "Audio", "pad.wav"), pad, 0o644)
	other := bytes.Repeat([]byte{4}, 1000)
	os.WriteFile(filepath.Join(dir, "Audio", "serum wt.wav"), other, 0o644)
	sh := DefaultShared()
	sh.AlwaysInclude = []string{"Audio/serum wt.wav"}
	os.MkdirAll(filepath.Join(dir, ".flsync"), 0o755)
	writeJSON(filepath.Join(dir, ".flsync", "settings.json"), sh)
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}
	if _, err := p.CreateOnGitHub("Beat", nil); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	// «Отправлять» защищает от удаления
	if _, err := p.TrashUnused([]string{"Audio/serum wt.wav"}); err == nil {
		t.Fatal("отмеченное «всегда отправлять» удалять нельзя")
	}
	// FL удалила оба файла
	os.Remove(filepath.Join(dir, "Audio", "pad.wav"))
	os.Remove(filepath.Join(dir, "Audio", "serum wt.wav"))
	lost := p.CheckLost().Lost
	if len(lost) != 2 {
		t.Fatalf("пропавшие: %+v", lost)
	}
	n, err := p.RestoreLost(lost)
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Audio", "pad.wav")); !bytes.Equal(b, pad) {
		t.Fatal("pad.wav не вернулся")
	}
	if l := p.CheckLost().Lost; len(l) != 0 {
		t.Fatalf("после возврата: %+v", l)
	}
	_ = strings.Contains
}

// Сменил ник — старые комментарии и статус показываются под новым ником и остаются «своими».
func TestRenameNickComments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	must(t, func() error { _, err := cfg.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
	s := &Server{cfg: cfg}
	nick := "Кирюха"
	if _, err := cfg.Hub().UpdateMe(&nick, nil, nil); err != nil {
		t.Fatal(err)
	}
	usersCache.Lock()
	usersCache.at = time.Time{}
	usersCache.Unlock()
	name := s.nickResolver()
	if got := name("", "tetramino"); got != "Кирюха" {
		t.Fatalf("старый комментарий без id: %q", got)
	}
	if got := name(cfg.UserID, "tetramino"); got != "Кирюха" {
		t.Fatalf("по id: %q", got)
	}
	cfg.OldNicks = []string{"tetramino"}
	if !cfg.isMe("", "tetramino") || cfg.isMe("someone-else", "tetramino") {
		t.Fatal("свой старый комментарий")
	}
}

// Сэмпл, на который ссылается проект, пропал и даже удалён из последней версии — Prepare достаёт его из старой.
func TestMissingFromHistory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", root)
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfg := &Config{Server: hubURL}
	must(t, func() error { _, err := cfg.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
	dir := filepath.Join(root, "A", "Beat")
	os.MkdirAll(filepath.Join(dir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(dir, "Track.flp"), synthFLP(filepath.Join(dir, "Audio", "pad.wav")), 0o644)
	pad := bytes.Repeat([]byte{5}, 100<<10)
	os.WriteFile(filepath.Join(dir, "Audio", "pad.wav"), pad, 0o644)
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}
	if _, err := p.CreateOnGitHub("Beat", nil); err != nil {
		t.Fatal(err, dump(p.Job))
	}
	g := p.git()
	g.Run("rm", "-q", "Audio/pad.wav")
	g.Run("commit", "-q", "-m", "удалил")
	if exists(filepath.Join(dir, "Audio", "pad.wav")) {
		t.Fatal("должен был пропасть")
	}
	if _, err := p.Prepare(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Audio", "pad.wav")); !bytes.Equal(b, pad) {
		t.Fatalf("не достал из истории:\n%s", dump(p.Job))
	}
}

// Автообновление идёт — «Обновить и перезапустить» не запускает второе скачивание.
func TestSingleUpdateJob(t *testing.T) {
	s := &Server{cfg: &Config{}, jobs: map[string]*Job{}}
	block := make(chan struct{})
	var calls atomic.Int32
	fn := func(j *Job) (any, error) { calls.Add(1); <-block; return "ok", nil }
	a := s.startUpdateJob("авто", fn)
	b := s.startUpdateJob("кнопка", fn)
	if a != b || s.updatingJob() != a.ID {
		t.Fatal("второе обновление не должно начинаться")
	}
	close(block)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("скачиваний: %d", calls.Load())
	}
	updJob = nil
}
