package app

import (
	"os"
	"os/exec"
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

func TestRegistryAndInvites(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := newFakeGitHub()
	defer gh.srv.Close()
	ghAPIBase = gh.srv.URL
	invalidateRegistry()

	cfgA := &Config{UserName: "tetramino", Token: "tokA", Registry: "alogin"}
	cfgB := &Config{UserName: "friend", Token: "tokB", Registry: "alogin"}
	// the friend can't register before the owner created the directory
	if _, err := RegisterNick(cfgB); err == nil {
		t.Fatal("registry should not exist yet")
	}
	// with no directory configured, the first user becomes its owner
	first := &Config{UserName: "solo", Token: "tokA"}
	note, err := RegisterNick(first)
	must(t, err)
	if first.Registry != "alogin" || !strings.Contains(note, "alogin") {
		t.Fatalf("first user should own the directory: %+v %q", first, note)
	}
	gh.issues, gh.repos = nil, map[string]bool{}
	invalidateRegistry()
	_, err = RegisterNick(cfgA)
	must(t, err)
	if !cfgA.Registered || cfgA.GitHubLogin != "alogin" || !gh.repos["alogin/programma-uzbeka-users"] {
		t.Fatalf("owner registration: %+v", cfgA)
	}
	_, err = RegisterNick(cfgB)
	must(t, err)
	if _, err := RegisterNick(&Config{UserName: "FRIEND", Token: "tok", Registry: "alogin"}); err == nil || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("nick must be unique, got %v", err)
	}
	u, err := LookupNick(cfgA, "Friend")
	must(t, err)
	if u.Login != "blogin" {
		t.Fatalf("lookup: %+v", u)
	}
	if _, err := LookupNick(cfgA, "nobody"); err == nil {
		t.Fatal("unknown nick found")
	}

	// invite by nick → friend sees it in the app → accepts
	if _, err := InviteToProject(cfgA, "https://github.com/o/r.git", "friend"); err != nil {
		t.Fatal(err)
	}
	invs, err := PendingInvitations(cfgB)
	must(t, err)
	if len(invs) != 1 || invs[0].InviterNick != "tetramino" || invs[0].CloneURL != "https://github.com/o/r.git" {
		t.Fatalf("invites: %+v", invs)
	}
	ps, _ := Participants(cfgA, "https://github.com/o/r.git")
	if len(ps) != 2 || !ps[1].Pending || ps[1].Nick != "friend" {
		t.Fatalf("participants before accept: %+v", ps)
	}
	must(t, AcceptInvitation(cfgB, invs[0].ID))
	ps, _ = Participants(cfgA, "https://github.com/o/r.git")
	if len(ps) != 2 || ps[1].Pending || ps[1].Nick != "friend" {
		t.Fatalf("participants after accept: %+v", ps)
	}

	// renaming frees the old nick
	cfgB.UserName = "friend2"
	_, err = RegisterNick(cfgB)
	must(t, err)
	invalidateRegistry()
	if _, err := LookupNick(cfgA, "friend"); err == nil {
		t.Fatal("old nick still registered")
	}
	if u, _ := LookupNick(cfgA, "friend2"); u == nil || u.Login != "blogin" {
		t.Fatal("new nick not found")
	}
}

func TestCrossOSTransfer(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := newFakeGitHub()
	defer gh.srv.Close()
	ghAPIBase = gh.srv.URL
	repoSlug = func(string) (string, string, bool) { return "o", "r", true }
	homeA, homeB := filepath.Join(root, "A-home"), filepath.Join(root, "B-home")

	remote := filepath.Join(root, "remote.git")
	must(t, exec.Command("git", "init", "--bare", "-b", "main", remote).Run())
	aDir := filepath.Join(root, "A", "Трек")
	os.MkdirAll(aDir, 0o755)
	os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP("/nowhere/kick.wav"), 0o644)
	bundle := filepath.Join(root, "A-plugins", "Serum.vst3")
	os.MkdirAll(filepath.Join(bundle, "Contents", "x86_64-linux"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "x86_64-linux", "Serum.so"), []byte("bin"), 0o755)
	cfgA := &Config{UserName: "tetramino", Token: "tok", ExtraPluginDirs: []string{filepath.Dir(bundle)}}
	cfgB := &Config{UserName: "friend", Token: "tok"}
	os.Setenv("HOME", homeA)
	ScanInstalled(cfgA, true)
	A := &Project{Dir: aDir, Cfg: cfgA, Job: &Job{}}
	must(t, A.Init(remote))
	_, err := A.Send("start", false)
	must(t, err)
	os.MkdirAll(filepath.Join(root, "B"), 0o755)
	bDir, err := Clone(cfgB, &Job{}, remote, filepath.Join(root, "B"))
	must(t, err)
	B := &Project{Dir: bDir, Cfg: cfgB, Job: &Job{}}

	req, err := B.RequestPlugin("Serum", "VST3", "Xfer Records", "tetramino")
	must(t, err)
	req.FromOS = "Windows" // pretend the friend is on Windows
	must(t, B.saveRequest(req, "test"))

	os.Setenv("HOME", homeA)
	in, err := A.Inspect(req.ID)
	must(t, err)
	if !in.Found || in.Compatible || !strings.Contains(in.Reason, "установщик") {
		t.Fatalf("linux-only bundle must not be sendable to Windows: %+v", in)
	}
	// a multi-platform bundle is fine
	os.MkdirAll(filepath.Join(bundle, "Contents", "x86_64-win"), 0o755)
	in, _ = A.Inspect(req.ID)
	if !in.Compatible {
		t.Fatalf("bundle with a Windows binary must be sendable: %+v", in)
	}
	// an installer in Downloads is offered
	inst := filepath.Join(homeA, "Downloads", "Serum_x64_Setup.exe")
	os.MkdirAll(filepath.Dir(inst), 0o755)
	os.WriteFile(inst, []byte("MZ installer"), 0o644)
	os.WriteFile(filepath.Join(homeA, "Downloads", "Other plugin.exe"), []byte("MZ"), 0o644)
	in, _ = A.Inspect(req.ID)
	if len(in.Installers) != 1 || in.Installers[0].Path != inst {
		t.Fatalf("installers: %+v", in.Installers)
	}
	must(t, A.ApproveRequest(req.ID, inst, true))
	if len(cfgA.TrustedRequesters) != 1 || cfgA.TrustedRequesters[0] != "friend" {
		t.Fatal("trust not saved")
	}

	os.Setenv("HOME", homeB)
	dst, err := B.InstallRequest(req.ID)
	must(t, err)
	if b, _ := os.ReadFile(dst); string(b) != "MZ installer" || filepath.Dir(dst) != filepath.Join(homeB, "Downloads") {
		t.Fatalf("installer not delivered: %s", dst)
	}
	r, _ := B.request(req.ID)
	if r.Status != ReqInstalled || r.Kind != "installer" || len(gh.assets) != 0 {
		t.Fatalf("final: %+v, assets left %d", r, len(gh.assets))
	}
}
