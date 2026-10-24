package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Папку проекта переименовали внутри «папки с проектами» — FruityHub находит её по .flp и сам меняет путь.
func TestRelocateProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	music := filepath.Join(root, "Music")
	old := filepath.Join(music, "Трек")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "Ночной дрифт.flp"), synthFLP(""), 0o644)
	os.MkdirAll(filepath.Join(music, "Другой"), 0o755)
	os.WriteFile(filepath.Join(music, "Другой", "Другой.flp"), synthFLP(""), 0o644)
	cfg := &Config{ProjectRoots: []string{music}, Projects: []LocalProject{{Path: old, Name: "Мой трек", Group: "g1"}}}
	s := &Server{cfg: cfg, notes: map[string]*ProjectNotes{}}
	if !s.rememberProject(&cfg.Projects[0]) || cfg.Projects[0].MainFLP != "Ночной дрифт.flp" {
		t.Fatalf("не запомнил .flp: %+v", cfg.Projects[0])
	}
	moved := filepath.Join(music, "2026", "Дрифт (финал)")
	os.MkdirAll(filepath.Dir(moved), 0o755)
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	c := s.locateCandidates(&cfg.Projects[0])
	if len(c) != 1 || c[0].Path != moved || c[0].Match != "flp" {
		t.Fatalf("кандидаты: %+v", c)
	}
	if err := s.relocateProject(old, moved); err != nil {
		t.Fatal(err)
	}
	lp := cfg.Project(moved)
	if lp == nil || lp.Name != "Мой трек" || lp.Group != "g1" {
		t.Fatalf("после переезда: %+v", cfg.Projects)
	}
	if err := s.relocateProject(moved, filepath.Join(root, "нет такой")); err == nil {
		t.Fatal("несуществующую папку принимать нельзя")
	}
	// артист в списке — как ввели, кириллицей (в .flp он латиницей)
	SaveTrackMeta(moved, TrackMeta{Artist: "Кирюха"})
	if _, a := projectMeta(moved); a != "Кирюха" {
		t.Fatalf("артист в списке: %q", a)
	}
	found := scanProjectRoots(cfg, cfg.ProjectRoots)
	if len(found) != 2 {
		t.Fatalf("в папках с проектами: %+v", found)
	}
}

func TestConflictCopyInFlsync(t *testing.T) {
	cp := conflictName("Sub/Трек.flp", "tetramino")
	if !strings.HasPrefix(cp, ".flsync/conflicts/Sub/Трек (конфликт — tetramino ") {
		t.Fatal(cp)
	}
	if o, ok := conflictOrig(cp); !ok || o != "Sub/Трек.flp" {
		t.Fatal(o, ok)
	}
	if cp := conflictName("Audio/vox.wav", "x"); strings.HasPrefix(cp, ".flsync") {
		t.Fatal("аудио остаётся рядом:", cp)
	}
}
