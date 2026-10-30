package app

import (
	"bufio"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixtureRun(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.autocrlf=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimRight(string(out), "\r\n")
}

func gitLatencyFixture(t testing.TB) (*Server, *Git) {
	t.Helper()
	s := latencyServer(t, 1)
	dir := s.cfg.Projects[0].Path
	gitFixtureRun(t, dir, "init")
	gitFixtureRun(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	gitFixtureRun(t, dir, "config", "uzbek.adopted", "true")
	gitFixtureRun(t, dir, "add", ".")
	gitFixtureRun(t, dir, "commit", "-m", "Fixture")
	gitFixtureRun(t, dir, "remote", "add", "origin", "https://example.invalid/fixture.git")
	gitFixtureRun(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitFixtureRun(t, dir, "config", "branch.main.remote", "origin")
	gitFixtureRun(t, dir, "config", "branch.main.merge", "refs/heads/main")
	return s, &Git{Dir: dir, Cfg: s.cfg}
}

func TestGitOpeningTrace(t *testing.T) {
	s, g := gitLatencyFixture(t)
	for _, label := range []string{"first", "repeat"} {
		commands := traceGitOpening(t, label, func() {
			w := latencyRequest(t, s.Handler(0), "/api/project?path="+url.QueryEscape(g.Dir))
			var summary ProjectSummary
			if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil || summary.Branch != "main" || summary.Ahead != 0 || summary.Behind != 0 {
				t.Fatalf("summary: %+v %v", summary, err)
			}
		})
		statuses := 0
		for _, command := range commands {
			if strings.Contains(command, "status --porcelain=v2 ") {
				statuses++
			}
			if strings.Contains(command, "status --porcelain=v1 ") || strings.Contains(command, "rev-parse --abbrev-ref HEAD") {
				t.Fatal("legacy fallback on normal repository:", command)
			}
		}
		if statuses != 1 {
			t.Fatalf("expected one status process, got %d", statuses)
		}
	}
}

func traceGitOpening(t *testing.T, label string, request func()) []string {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	request()
	f, err := os.Open(trace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	commands := []string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var row struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row.Event == "start" {
			commands = append(commands, strings.Join(row.Argv, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Git launches (%s): %d\n%s", label, len(commands), strings.Join(commands, "\n"))
	if len(commands) == 0 {
		t.Fatal("trace did not capture Git")
	}
	return commands
}

func BenchmarkGitProjectOpening(b *testing.B) {
	for _, mode := range []string{"Warm", "ColdReads"} {
		b.Run(mode, func(b *testing.B) {
			s, g := gitLatencyFixture(b)
			h := s.Handler(0)
			target := "/api/project?path=" + url.QueryEscape(g.Dir)
			latencyRequest(b, h, target)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "ColdReads" {
					gitReads.Lock()
					clear(gitReads.values)
					gitReads.Unlock()
				}
				latencyRequest(b, h, target)
			}
		})
	}
}
