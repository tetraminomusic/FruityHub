package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func assertGitState(t *testing.T, g *Git) gitWorkingState {
	t.Helper()
	wantChanges, err := g.Status()
	if err != nil {
		t.Fatal(err)
	}
	wantBranch := g.Branch()
	wantAhead, wantBehind := g.AheadBehind()
	got, err := g.workingState()
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != wantBranch || got.Ahead != wantAhead || got.Behind != wantBehind || !reflect.DeepEqual(got.Changes, wantChanges) {
		t.Fatalf("state differs: %+v, want branch=%s ahead=%d behind=%d changes=%+v", got, wantBranch, wantAhead, wantBehind, wantChanges)
	}
	return got
}

func TestGitWorkingStateChanges(t *testing.T) {
	_, g := gitLatencyFixture(t)
	assertGitState(t, g)
	writeF(t, filepath.Join(g.Dir, "delete.txt"), "remove me")
	gitFixtureRun(t, g.Dir, "add", "delete.txt")
	gitFixtureRun(t, g.Dir, "commit", "-m", "Deletion fixture")
	gitFixtureRun(t, g.Dir, "mv", "Track.flp", "renamed project.flp")
	if err := os.Remove(filepath.Join(g.Dir, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(g.Dir, "new file.wav"), "sample")
	gitFixtureRun(t, g.Dir, "add", "new file.wav")
	writeF(t, filepath.Join(g.Dir, "кириллица файл.FLP"), "untracked")
	writeF(t, filepath.Join(g.Dir, ".flsync", "hidden.txt"), "internal")
	if len(assertGitState(t, g).Changes) != 5 {
		t.Fatal("lost changes")
	}
	// Same Git object, no wait: mutations outside FruityHub must be visible.
	writeF(t, filepath.Join(g.Dir, "new file.wav"), "modified sample")
	state := assertGitState(t, g)
	for _, e := range state.Changes {
		if e.Path == "new file.wav" && e.Code != "AM" {
			t.Fatal(e)
		}
	}
}

func TestGitWorkingStateBranches(t *testing.T) {
	_, g := gitLatencyFixture(t)
	base := gitFixtureRun(t, g.Dir, "rev-parse", "HEAD")
	gitFixtureRun(t, g.Dir, "commit", "--allow-empty", "-m", "Local")
	if state := assertGitState(t, g); state.Ahead != 1 || state.Behind != 0 {
		t.Fatal(state)
	}
	tree := gitFixtureRun(t, g.Dir, "rev-parse", "HEAD^{tree}")
	remote := gitFixtureRun(t, g.Dir, "commit-tree", tree, "-p", base, "-m", "Remote")
	gitFixtureRun(t, g.Dir, "update-ref", "refs/remotes/origin/main", remote)
	if state := assertGitState(t, g); state.Ahead != 1 || state.Behind != 1 {
		t.Fatal(state)
	}
	// A different configured upstream must not change comparison with origin.
	gitFixtureRun(t, g.Dir, "remote", "add", "other", "https://example.invalid/other.git")
	gitFixtureRun(t, g.Dir, "update-ref", "refs/remotes/other/main", "HEAD")
	gitFixtureRun(t, g.Dir, "config", "branch.main.remote", "other")
	if state := assertGitState(t, g); state.Ahead != 1 || state.Behind != 1 {
		t.Fatal(state)
	}
	gitFixtureRun(t, g.Dir, "update-ref", "-d", "refs/remotes/origin/main")
	if state := assertGitState(t, g); state.Ahead != -1 || state.Behind != 0 {
		t.Fatal(state)
	}
	gitFixtureRun(t, g.Dir, "update-ref", "refs/heads/origin/main", "HEAD")
	if state := assertGitState(t, g); state.Ahead != -1 {
		t.Fatal("local branch was mistaken for origin:", state)
	}
	gitFixtureRun(t, g.Dir, "checkout", "-b", "feature/ветка")
	if state := assertGitState(t, g); state.Branch != "feature/ветка" {
		t.Fatal(state)
	}
	gitFixtureRun(t, g.Dir, "checkout", "--detach", "HEAD")
	if state := assertGitState(t, g); state.Branch != "main" {
		t.Fatal(state)
	}
}

func TestGitWorkingStateInitial(t *testing.T) {
	dir := t.TempDir()
	gitFixtureRun(t, dir, "init")
	gitFixtureRun(t, dir, "symbolic-ref", "HEAD", "refs/heads/empty")
	writeF(t, filepath.Join(dir, "initial.flp"), "untracked")
	state := assertGitState(t, &Git{Dir: dir})
	if state.Branch != "empty" || state.Ahead != -1 || len(state.Changes) != 1 {
		t.Fatal(state)
	}
}

func TestGitWorkingStateConflictAndWorktree(t *testing.T) {
	_, g := gitLatencyFixture(t)
	writeF(t, filepath.Join(g.Dir, "conflict.txt"), "base\n")
	gitFixtureRun(t, g.Dir, "add", "conflict.txt")
	gitFixtureRun(t, g.Dir, "commit", "-m", "Base")
	gitFixtureRun(t, g.Dir, "checkout", "-b", "side")
	writeF(t, filepath.Join(g.Dir, "conflict.txt"), "side\n")
	gitFixtureRun(t, g.Dir, "commit", "-am", "Side")
	gitFixtureRun(t, g.Dir, "checkout", "main")
	writeF(t, filepath.Join(g.Dir, "conflict.txt"), "main\n")
	gitFixtureRun(t, g.Dir, "commit", "-am", "Main")
	// merge-tree plumbing creates an unmerged index without running a merge or
	// any hooks, and without contacting a remote.
	base := gitFixtureRun(t, g.Dir, "merge-base", "main", "side")
	gitFixtureRun(t, g.Dir, "read-tree", "-m", base, "main", "side")
	state := assertGitState(t, g)
	found := false
	for _, e := range state.Changes {
		if e.Path == "conflict.txt" && e.Kind == "conflict" {
			found = true
		}
	}
	if !found {
		t.Fatal("lost merge conflict")
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitFixtureRun(t, g.Dir, "worktree", "add", "-b", "linked", linked, "HEAD")
	assertGitState(t, &Git{Dir: linked})
	p := &Project{Dir: linked}
	if !p.IsAdopted() {
		t.Fatal("missing shared adoption setting")
	}
	gitFixtureRun(t, g.Dir, "config", "uzbek.adopted", "false")
	if p.IsAdopted() {
		t.Fatal("cached linked-worktree configuration")
	}
}

func TestGitWorkingStateSubmodule(t *testing.T) {
	_, g := gitLatencyFixture(t)
	source := t.TempDir()
	gitFixtureRun(t, source, "init")
	writeF(t, filepath.Join(source, "sample.txt"), "base")
	gitFixtureRun(t, source, "add", ".")
	gitFixtureRun(t, source, "commit", "-m", "Submodule")
	// File transport is enabled only for this synthetic local clone.
	gitFixtureRun(t, g.Dir, "-c", "protocol.file.allow=always", "submodule", "add", source, "module")
	gitFixtureRun(t, g.Dir, "commit", "-am", "Add submodule")
	writeF(t, filepath.Join(g.Dir, "module", "sample.txt"), "changed")
	assertGitState(t, g)
	gitFixtureRun(t, filepath.Join(g.Dir, "module"), "checkout", "--", "sample.txt")
	writeF(t, filepath.Join(g.Dir, "module", "untracked.txt"), "new")
	assertGitState(t, g)
}

func TestGitAdoptionCacheExternalChanges(t *testing.T) {
	_, g := gitLatencyFixture(t)
	p := &Project{Dir: g.Dir}
	if !p.IsAdopted() {
		t.Fatal("missing adoption flag")
	}
	gitFixtureRun(t, g.Dir, "config", "uzbek.adopted", "false")
	if p.IsAdopted() {
		t.Fatal("stale adoption flag")
	}
	gitFixtureRun(t, g.Dir, "config", "uzbek.adopted", "true")
	if !p.IsAdopted() {
		t.Fatal("stale adoption flag")
	}
}

func TestGitWorkingStateNotifications(t *testing.T) {
	s, g := gitLatencyFixture(t)
	visible := filepath.Join(g.Dir, "new sample.wav")
	writeF(t, visible, "sample")
	writeF(t, filepath.Join(g.Dir, ".flsync", "hidden.txt"), "internal")
	n := s.computeNotes(g.Dir)
	if !n.Remote || n.Ahead != 0 || n.Behind != 0 || n.Changed != 1 {
		t.Fatalf("notes: %+v", n)
	}
	if err := os.Remove(visible); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, g.Dir, "commit", "--allow-empty", "-m", "Local update")
	n = s.computeNotes(g.Dir)
	if n.Ahead != 1 || n.Behind != 0 || n.Changed != 0 {
		t.Fatalf("stale notes: %+v", n)
	}
}

func TestGitWorkingStateRejectsInvalidRecords(t *testing.T) {
	g := &Git{Dir: t.TempDir()}
	for _, raw := range []string{"", "# branch.head main\x00! ignored\x00", "# branch.head main\x001 invalid\x00", "# branch.head main\x002 R. N... 0 0 0 x x R100 new"} {
		if _, err := g.parseWorkingState(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	state, err := g.parseWorkingState("# branch.head main\x00# future.header value\x00? # branch.head file\x00? spaces\tand\nlines.flp\x00")
	if err != nil || len(state.Changes) != 2 || !strings.Contains(state.Changes[1].Path, "\n") {
		t.Fatalf("NUL paths: %+v %v", state, err)
	}
}
