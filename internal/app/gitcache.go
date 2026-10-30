package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type gitRead struct {
	value string
	at    time.Time
}

var gitReads = struct {
	sync.Mutex
	values map[string]gitRead
}{values: map[string]gitRead{}}

// Только короткие неизменяющие команды. Статус файлов и операции записи
// всегда выполняются заново; кэш ограничен по времени и количеству элементов.
func cachedGitRead(g *Git, args ...string) (string, error) {
	command := strings.Join(args, "\x00")
	switch command {
	case "remote\x00get-url\x00origin", "rev-parse\x00--abbrev-ref\x00HEAD", "symbolic-ref\x00--short\x00HEAD", "config\x00--get\x00uzbek.adopted":
	default:
		return g.run(nil, false, args...)
	}
	if command == "config\x00--get\x00uzbek.adopted" {
		// Linked worktrees use a .git file and shared configuration elsewhere.
		// Read this setting fresh when the local config stamp is unavailable.
		st, err := os.Stat(filepath.Join(g.Dir, ".git"))
		if err != nil || !st.IsDir() || len(g.Env) != 0 {
			return g.run(nil, false, args...)
		}
	}
	key := g.Dir + "\x00" + command
	for _, name := range []string{"HEAD", "config", "packed-refs"} {
		if info, err := os.Stat(filepath.Join(g.Dir, ".git", name)); err == nil {
			key += fmt.Sprint(info.ModTime().UnixNano(), info.Size())
		}
	}
	gitReads.Lock()
	v, ok := gitReads.values[key]
	gitReads.Unlock()
	if ok && time.Since(v.at) < time.Second {
		return v.value, nil
	}
	out, err := g.run(nil, false, args...)
	if err == nil {
		gitReads.Lock()
		if len(gitReads.values) >= 512 {
			clear(gitReads.values)
		}
		gitReads.values[key] = gitRead{out, time.Now()}
		gitReads.Unlock()
	}
	return out, err
}
