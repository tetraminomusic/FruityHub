package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flsync/internal/flp"
)

// multiFLP builds a project that uses several samples.
func multiFLP(samples ...string) []byte {
	u16 := func(s string) []byte {
		var b []byte
		for _, r := range s {
			b = append(b, byte(r), byte(r>>8))
		}
		return append(b, 0, 0)
	}
	ev := []flp.Event{{ID: 199, Data: []byte("21.2.3.4004\x00")}}
	for i, s := range samples {
		ev = append(ev, flp.Event{ID: 64, Data: []byte{byte(i), 0}}, flp.Event{ID: 201, Data: u16("Sampler")}, flp.Event{ID: 196, Data: u16(s)})
	}
	return (&flp.File{Channels: uint16(len(samples)), PPQ: 96, Events: ev}).Bytes()
}

func TestLayout(t *testing.T) {
	root := t.TempDir()
	os.Setenv("HOME", filepath.Join(root, "home"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	dir := filepath.Join(root, "Track")
	os.MkdirAll(filepath.Join(dir, "Renders"), 0o755)
	for _, n := range []string{"kick.wav", "hat.wav", "snare.wav", "unused.wav"} {
		os.WriteFile(filepath.Join(dir, n), []byte("RIFF "+n), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "Renders", "old mix.wav"), []byte("RIFF old"), 0o644)
	os.WriteFile(filepath.Join(dir, "Track.flp"), multiFLP(filepath.Join(dir, "kick.wav"), filepath.Join(dir, "hat.wav")), 0o644)
	os.WriteFile(filepath.Join(dir, "Other.flp"), multiFLP(filepath.Join(dir, "snare.wav"), filepath.Join(dir, "hat.wav")), 0o644)
	os.WriteFile(filepath.Join(dir, "Track (конфликт — friend 2026-10-19 10-00).flp"), multiFLP(), 0o644)
	cfg := &Config{UserName: "tetramino", Projects: []LocalProject{{Path: dir}}}
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}

	// 1. audio from the root goes to Audio/, paths in .flp follow
	moved, err := p.OrganizeAudio()
	must(t, err)
	if len(moved) != 4 || exists(filepath.Join(dir, "kick.wav")) || !exists(filepath.Join(dir, "Audio", "kick.wav")) {
		t.Fatalf("organize: %v", moved)
	}
	n, err := p.Relink()
	must(t, err)
	f, _ := flp.ParseFile(filepath.Join(dir, "Track.flp"))
	if n == 0 || !strings.HasPrefix(f.Info().Samples[0], filepath.Join(dir, "Audio")) {
		t.Fatalf("relink after organize: %d %v", n, f.Info().Samples)
	}

	// 2. two real projects in one folder are detected (copies are not)
	extra := p.ExtraFLPs()
	if len(extra) != 2 || p.MainFLPGuess(extra) != "Track.flp" {
		t.Fatalf("extra flps: %v main=%s", extra, p.MainFLPGuess(extra))
	}

	// 3. split: Other.flp gets its own folder; its own audio moves, shared audio is copied
	res, err := p.Split([]string{"Other.flp"}, nil, "grp")
	must(t, err)
	other := filepath.Join(root, "Other")
	if len(res.NewProjects) != 1 || res.NewProjects[0] != other || res.Moved != 1 || res.Copied != 1 {
		t.Fatalf("split: %+v", res)
	}
	if !exists(filepath.Join(other, "Other.flp")) || !exists(filepath.Join(other, "Audio", "snare.wav")) || !exists(filepath.Join(other, "Audio", "hat.wav")) {
		t.Fatal("split files missing")
	}
	if exists(filepath.Join(dir, "Audio", "snare.wav")) || !exists(filepath.Join(dir, "Audio", "hat.wav")) {
		t.Fatal("split: wrong files left in the original project")
	}
	of, _ := flp.ParseFile(filepath.Join(other, "Other.flp"))
	for _, s := range of.Info().Samples {
		if !strings.HasPrefix(s, filepath.Join(other, "Audio")) {
			t.Fatalf("new project not relinked: %v", of.Info().Samples)
		}
	}
	if lp := cfg.Project(other); lp == nil || lp.Group != "grp" {
		t.Fatal("new project not registered in the same folder of the list")
	}
	if p.ExtraFLPs() != nil {
		t.Fatal("still reports several projects")
	}

	// 4. "it's just a version" is remembered
	os.WriteFile(filepath.Join(dir, "Track_2.flp"), multiFLP(), 0o644)
	if len(p.ExtraFLPs()) != 2 {
		t.Fatal("new version not detected")
	}
	_, err = p.Split(nil, []string{"Track_2.flp"}, "")
	must(t, err)
	if p.ExtraFLPs() != nil {
		t.Fatal("kept version still reported")
	}

	// 5. deleting unused audio goes to the trash; used audio is protected
	if _, err := p.TrashUnused([]string{"Audio/kick.wav"}); err == nil {
		t.Fatal("used audio must not be deleted")
	}
	n2, err := p.TrashUnused([]string{"Audio/unused.wav", "Renders/old mix.wav"})
	must(t, err)
	trash := filepath.Join(root, "home", ".local", "share", "Trash", "files")
	if n2 != 2 || exists(filepath.Join(dir, "Audio", "unused.wav")) || !exists(filepath.Join(trash, "unused.wav")) {
		t.Fatalf("trash: %d", n2)
	}
}
