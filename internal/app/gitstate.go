package app

import (
	"fmt"
	"strings"
)

type gitWorkingState struct {
	Branch        string
	Changes       []StatusEntry
	Ahead, Behind int
	headOID       string
}

// workingState always reads the current index/worktree and branch in one Git
// process. Ahead/behind deliberately compares origin/<branch>, not an arbitrary
// configured upstream; status' own upstream counts are therefore disabled.
func (g *Git) workingState() (gitWorkingState, error) {
	out, err := g.RunQuiet("status", "--porcelain=v2", "--branch", "--no-ahead-behind", "-z", "--untracked-files=all")
	if err != nil {
		return gitWorkingState{}, err
	}
	state, err := g.parseWorkingState(out)
	if err != nil {
		return state, err
	}
	if state.headOID == "(initial)" {
		if !g.RemoteBranchExists(state.Branch) {
			state.Ahead = -1
		}
		return state, nil
	}
	head := state.headOID
	if head == "" {
		head = "HEAD"
	}
	// Using a fully qualified ref also avoids accidentally comparing a local
	// branch named origin/main when the remote-tracking branch is absent.
	counts, countErr := g.RunQuiet("rev-list", "--left-right", "--count", head+"...refs/remotes/origin/"+state.Branch)
	if countErr == nil {
		_, err = fmt.Sscanf(counts, "%d %d", &state.Ahead, &state.Behind)
		if err != nil {
			return state, err
		}
	} else if !g.RemoteBranchExists(state.Branch) {
		state.Ahead = -1
	}
	return state, nil
}

func (g *Git) parseWorkingState(out string) (gitWorkingState, error) {
	var state gitWorkingState
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		r := records[i]
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "# ") {
			if oid, ok := strings.CutPrefix(r, "# branch.oid "); ok {
				state.headOID = oid
			}
			if branch, ok := strings.CutPrefix(r, "# branch.head "); ok {
				state.Branch = branch
				if branch == "(detached)" {
					state.Branch = "main"
				} // existing Branch() fallback
			}
			continue
		}
		var code, path string
		switch r[0] {
		case '1', '2', 'u':
			fields := 9
			if r[0] == '2' {
				fields = 10
			}
			if r[0] == 'u' {
				fields = 11
			}
			parts := strings.SplitN(r, " ", fields)
			if len(parts) != fields || len(parts[1]) != 2 {
				return state, fmt.Errorf("invalid Git status record")
			}
			code, path = strings.ReplaceAll(parts[1], ".", " "), parts[fields-1]
			if r[0] == '2' {
				i++ // original name is a separate NUL record, not another status
				if i >= len(records) || records[i] == "" {
					return state, fmt.Errorf("missing Git rename source")
				}
			}
		case '?':
			if !strings.HasPrefix(r, "? ") {
				return state, fmt.Errorf("invalid untracked Git record")
			}
			code, path = "??", r[2:]
		default:
			return state, fmt.Errorf("unknown Git status record")
		}
		if path == "" {
			return state, fmt.Errorf("empty Git status path")
		}
		state.Changes = append(state.Changes, g.statusEntry(code, path))
	}
	if state.Branch == "" {
		return state, fmt.Errorf("missing Git branch header")
	}
	return state, nil
}
