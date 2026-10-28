package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"flsync/internal/flp"
)

// Время в FL по истории: разница счётчика .flp между версиями — автору коммита; «Сохранить как»
// не удваивает время; несохранённое в историю — моё.
func TestStatsFromHistory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", root)
	dir := filepath.Join(root, "Трек")
	os.MkdirAll(dir, 0o755)
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	write := func(name string, spent time.Duration) {
		os.WriteFile(filepath.Join(dir, name), flp.WithTimestampForTest(synthFLP("Audio/a.wav"), spent, created), 0o644)
	}
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatal(args, err, string(out))
		}
	}
	commit := func(who string) {
		git("add", "-A")
		git("-c", "user.name="+who, "-c", "user.email=x@x", "commit", "-q", "-m", who)
	}
	git("init", "-q")
	write("Track.flp", 2*time.Hour) // до FruityHub — 2 часа: первая запись в историю, не считается
	commit("tetramino")
	write("Track.flp", 3*time.Hour) // +1ч друг
	commit("kirill")
	os.Remove(filepath.Join(dir, "Track.flp"))
	write("Track v2.flp", 3*time.Hour+30*time.Minute) // «Сохранить как» + 30 мин, не 3ч30
	write("Track (версия 01.09).flp", 9*time.Hour)    // копия из истории — не считается
	commit("tetramino")
	write("Track v2.flp", 4*time.Hour) // не отправлено: +30 мин мне

	cfg := &Config{UserName: "tetramino"}
	cfg.Projects = []LocalProject{{Path: dir}}
	s := &Server{cfg: cfg}
	usersCache.Lock()
	usersCache.list, usersCache.at = nil, time.Now()
	usersCache.Unlock()
	statsCache.result, statsCache.blobs = nil, nil
	st := s.computeStats()
	if len(st.Projects) != 1 {
		t.Fatalf("%+v", st)
	}
	p := st.Projects[0]
	if p.Total != 2*3600 || p.Me != 3600 || st.All != 3600 || p.FL != 4*3600 {
		t.Fatalf("%+v", p)
	}
	if len(p.People) != 2 || p.People[0].Sec != 3600 || p.People[1].Sec != 3600 {
		t.Fatalf("%+v", p.People)
	}
	if st.Week != 3600 || st.Weeks[len(st.Weeks)-1].Others != 3600 {
		t.Fatalf("неделя: %+v", st)
	}
	// второй раз — из кеша
	statsCache.result = nil
	if st2 := s.computeStats(); st2.All != st.All {
		t.Fatal("кеш")
	}
}

// Проект ещё не записан в историю — накопленное в .flp время не считается.
func TestStatsNoHistory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	dir := filepath.Join(root, "Новый")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "A.flp"), flp.WithTimestampForTest(synthFLP("Audio/a.wav"), 5*time.Hour, time.Now()), 0o644)
	c := exec.Command("git", "init", "-q")
	c.Dir = dir
	c.Run()
	cfg := &Config{UserName: "tetramino"}
	cfg.Projects = []LocalProject{{Path: dir}}
	statsCache.result, statsCache.blobs = nil, nil
	st := (&Server{cfg: cfg}).computeStats()
	if st.All != 0 || len(st.Projects) != 1 || st.Projects[0].FL != 5*3600 {
		t.Fatalf("%+v", st)
	}
}
