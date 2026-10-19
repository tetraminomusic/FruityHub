package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAccountSyncAndAvailable(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := newFakeGitHub()
	defer gh.srv.Close()
	ghAPIBase = gh.srv.URL
	repoSlug = func(string) (string, string, bool) { return "o", "r", true }
	invalidateRegistry()

	// computer 1: registered, has a project in a folder "Альбом"
	proj := filepath.Join(root, "pc1", "Трек")
	os.MkdirAll(proj, 0o755)
	must(t, exec.Command("git", "-C", proj, "init", "-q").Run())
	must(t, exec.Command("git", "-C", proj, "remote", "add", "origin", "https://github.com/o/r.git").Run())
	pc1 := &Config{UserName: "tetramino", Token: "tokA", Registry: "alogin",
		Groups: []Group{{ID: "g1", Name: "Альбом"}}, Projects: []LocalProject{{Path: proj, Group: "g1"}}, TrustedRequesters: []string{"friend"}}
	_, err := RegisterNick(pc1)
	must(t, err)
	must(t, SaveProfile(pc1))

	// computer 2: only the token
	pc2 := &Config{}
	res, err := Login(pc2, "tokA", "")
	must(t, err)
	if !res.HasProfile || res.Nick != "tetramino" || pc2.UserName != "tetramino" || pc2.Registry != "alogin" ||
		len(pc2.Groups) != 1 || pc2.Groups[0].Name != "Альбом" || len(pc2.TrustedRequesters) != 1 || res.Projects != 1 {
		t.Fatalf("login: %+v cfg=%+v", res, pc2)
	}
	// the project isn't downloaded on pc2 → it is offered, in its folder
	prof, _, _ := LoadProfile(pc2)
	av, err := AvailableRepos(pc2, prof)
	must(t, err)
	var found *AvailableRepo
	for i := range av {
		if av[i].FullName == "o/r" {
			found = &av[i]
		}
		if av[i].Name == registryRepoName || av[i].Name == profileRepoName {
			t.Fatal("service repos must be hidden")
		}
	}
	if found == nil || !found.IsApp || found.Group != "g1" || !found.Mine {
		t.Fatalf("available: %+v", av)
	}
	// on pc1 it is already local → not offered
	av1, _ := AvailableRepos(pc1, prof)
	for _, a := range av1 {
		if a.FullName == "o/r" {
			t.Fatal("local project offered again")
		}
	}

	// a user without a profile but with a registered nick also signs in
	friend := &Config{UserName: "friend", Token: "tokB", Registry: "alogin"}
	_, err = RegisterNick(friend)
	must(t, err)
	pc3 := &Config{Registry: "alogin"}
	res, err = Login(pc3, "tokB", "")
	must(t, err)
	if res.HasProfile || res.Nick != "friend" {
		t.Fatalf("login via directory: %+v", res)
	}

	// participants: invite by nick, by GitHub login, then remove
	_, err = InviteToProject(pc1, "https://github.com/o/r.git", "friend")
	must(t, err)
	_, err = InviteToProject(pc1, "https://github.com/o/r.git", "some-github-user")
	must(t, err)
	ps, _ := Participants(pc1, "https://github.com/o/r.git")
	if len(ps) != 3 {
		t.Fatalf("participants: %+v", ps)
	}
	for _, p := range ps {
		if p.Pending && p.Login == "some-github-user" {
			must(t, RemoveParticipant(pc1, "https://github.com/o/r.git", p.Login, p.InviteID))
		}
	}
	if err := RemoveParticipant(pc1, "https://github.com/o/r.git", "o", 0); err == nil {
		t.Fatal("owner must not be removable")
	}
	ps, _ = Participants(pc1, "https://github.com/o/r.git")
	if len(ps) != 2 {
		t.Fatalf("after cancel: %+v", ps)
	}
}
