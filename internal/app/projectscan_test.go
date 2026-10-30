package app

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"flsync/internal/flp"
)

// Original two-pass enumeration, kept independent of the new walker.
func legacyProjectFiles(root string) (flps []string, audio []AudioFile) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		low := strings.ToLower(rel)
		if strings.HasSuffix(low, ".flp") && !strings.Contains(low, "autosave") && !strings.Contains(low, "overwritten") && !strings.HasPrefix(low, ".flsync/") {
			flps = append(flps, rel)
		}
		if flp.IsAudio(rel) {
			audio = append(audio, AudioFile{Rel: rel, Size: info.Size()})
		}
		return nil
	})
	sort.Strings(flps)
	return
}

func TestProjectScanCompatibility(t *testing.T) {
	// A project itself may be called Backup; only descendant folders skip.
	root := filepath.Join(t.TempDir(), "Backup")
	for _, rel := range []string{
		"Main.FLP", "draft/version.flp", "track (версия abc).flp", "nested/.flsync/keep.flp",
		"autosave.flp", "autoSave-dir/song.flp", "overwritten.flp", ".flsync/hidden.flp",
		".git/hidden.flp", "nested/BaCkUp/hidden.flp", ".FLsync-LOCAL/hidden.wav", "NODE_MODULES/hidden.flp",
		"Audio/kick.WAV", "Samples/voice.flac", "other.txt", ".flsync/internal.wav", "presets/patch.fst",
	} {
		writeF(t, filepath.Join(root, filepath.FromSlash(rel)), rel)
	}
	p := &Project{Dir: root}
	wantFLPs := []string{"Main.FLP", "draft/version.flp", "nested/.flsync/keep.flp", "track (версия abc).flp"}
	if got := p.FLPs(); !reflect.DeepEqual(got, wantFLPs) {
		t.Fatalf("FLP exclusions or ordering changed: %q", got)
	}
	wantF, wantA := legacyProjectFiles(root)
	gotF, gotA := p.scanProjectFiles(true)
	if !reflect.DeepEqual(gotF, wantF) || !reflect.DeepEqual(gotA, wantA) {
		t.Fatalf("snapshot differs: FLPs=%q/%q audio=%+v/%+v", gotF, wantF, gotA, wantA)
	}
	if got := (&Project{Dir: filepath.Join(t.TempDir(), "absent")}).Analyze(); got.FLPs == nil || got.Audio == nil || len(got.FLPs)+len(got.Audio) != 0 {
		t.Fatal("missing project must return empty JSON arrays")
	}
}

func TestProjectAnalysisSnapshotRefresh(t *testing.T) {
	root := t.TempDir()
	writeF(t, filepath.Join(root, "Main.flp"), string(synthFLP("kick.wav")))
	writeF(t, filepath.Join(root, "Broken.flp"), "bad")
	for _, rel := range []string{"kick.wav", "Audio/kick.wav", "Samples/kick.wav", "Audio/unused.wav", "Audio/tracked.wav", "Exports/render.wav"} {
		writeF(t, filepath.Join(root, filepath.FromSlash(rel)), rel)
	}
	shared := DefaultShared()
	shared.AlwaysInclude = []string{"Exports/"}
	if err := writeJSON(filepath.Join(root, ".flsync", "settings.json"), shared); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"add", "Audio/tracked.wav"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	p := &Project{Dir: root}
	a := p.Analyze()
	if len(a.FLPs) != 2 || a.FLPs[0].Rel != "Broken.flp" || a.FLPs[0].Error == "" || a.FLPs[1].Rel != "Main.flp" {
		t.Fatalf("FLPs: %+v", a.FLPs)
	}
	if len(a.Samples) != 1 || a.Samples[0].Status != "project" || a.Samples[0].Local != "Audio/kick.wav" || !reflect.DeepEqual(a.Samples[0].FLPs, []string{"Main.flp"}) {
		t.Fatalf("sample resolution changed: %+v", a.Samples)
	}
	if len(a.Audio) != 6 {
		t.Fatal(a.Audio)
	}
	for _, af := range a.Audio {
		wantUsed := flp.BaseName(af.Rel) == "kick.wav"
		wantTracked := af.Rel == "Audio/tracked.wav"
		wantIncluded := af.Rel == "Exports/render.wav"
		if af.Size != int64(len(af.Rel)) || af.Used != wantUsed || af.Tracked != wantTracked || af.Included != wantIncluded || af.Ignored != (!wantUsed && !wantTracked && !wantIncluded) {
			t.Fatalf("audio flags or size changed: %+v", af)
		}
	}
	if err := os.Remove(filepath.Join(root, "Broken.flp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "Audio", "kick.wav"), filepath.Join(root, "Audio", "renamed.wav")); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(root, "New.flp"), string(synthFLP("new.wav")))
	writeF(t, filepath.Join(root, "Audio", "new.wav"), "new")
	a = p.Analyze()
	if len(a.FLPs) != 2 || a.FLPs[0].Rel != "Main.flp" || a.FLPs[1].Rel != "New.flp" || len(a.Samples) != 2 || a.Samples[0].Local != "Samples/kick.wav" || a.Samples[1].Local != "Audio/new.wav" {
		t.Fatalf("stale snapshot after add/rename/delete: %+v", a)
	}
	for _, af := range a.Audio {
		if af.Rel == "Audio/kick.wav" {
			t.Fatal("renamed file survived in snapshot")
		}
	}
}

func TestProjectScanSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "Outside.flp")
	writeF(t, target, string(synthFLP("hit.wav")))
	for name, dst := range map[string]string{"Linked.flp": target, "Missing.flp": filepath.Join(outside, "missing.flp"), "Directory": outside} {
		if err := os.Symlink(dst, filepath.Join(root, name)); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
	}
	p := &Project{Dir: root}
	if got := p.FLPs(); !reflect.DeepEqual(got, []string{"Linked.flp", "Missing.flp"}) {
		t.Fatal("followed directory link or lost file link:", got)
	}
	a := p.Analyze()
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.FLPs) != 2 || a.FLPs[0].Error != "" || a.FLPs[0].Size != st.Size() || a.FLPs[1].Error == "" {
		t.Fatalf("symlink metadata/errors changed: %+v", a.FLPs)
	}
}

func TestProjectRootSelection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Song")
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, rel := range []string{"Song.flp", "newer.FLP", "copy (версия 1).flp", "Sub/Nested.flp", "Folder.flp/Inside.flp", "autosave.flp", "overwritten.flp"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		writeF(t, path, rel)
		at := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	p := &Project{Dir: root}
	all, _ := legacyProjectFiles(root)
	var wantRoot []string
	for _, rel := range all {
		if !strings.Contains(rel, "/") {
			wantRoot = append(wantRoot, rel)
		}
	}
	if got := p.rootFLPs(); !reflect.DeepEqual(got, wantRoot) {
		t.Fatalf("root candidates differ: %q/%q", got, wantRoot)
	}
	if p.MainFLP() != "Song.flp" {
		t.Fatal("folder-name preference changed")
	}
	// The metadata helper historically includes version/conflict copies and
	// chooses newest, while MainFLP excludes copies and prefers the folder name.
	if got := mainFLP(root); got != filepath.Join(root, "copy (версия 1).flp") {
		t.Fatal("metadata selection changed:", got)
	}
	if err := os.Remove(filepath.Join(root, "Song.flp")); err != nil {
		t.Fatal(err)
	}
	if p.MainFLP() != "newer.FLP" {
		t.Fatal("newest root project was not selected")
	}
	writeF(t, filepath.Join(root, "Added.flp"), "new project")
	if p.MainFLP() != "Added.flp" {
		t.Fatal("new file was not visible immediately")
	}
	if got := (&Project{Dir: filepath.Join(root, "absent")}).MainFLP(); got != "" {
		t.Fatal("missing folder has a main project:", got)
	}
}
