package app

import (
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// Activity is the app's simple event log ("Журнал"): what happened, in words.
type Activity struct {
	Time    int64  `json:"time"`
	Project string `json:"project,omitempty"` // project folder name
	Path    string `json:"path,omitempty"`
	Kind    string `json:"kind"` // ok, error, info
	Text    string `json:"text"`
}

var (
	actMu  sync.Mutex
	actLog []Activity
	actOK  bool
)

func activityFile() string { return filepath.Join(ConfigDir(), "activity.json") }

func loadActivity() {
	if !actOK {
		readJSON(activityFile(), &actLog)
		actOK = true
	}
}

// LogActivity adds an entry to the journal.
func LogActivity(path, kind, text string) {
	actMu.Lock()
	defer actMu.Unlock()
	loadActivity()
	a := Activity{Time: time.Now().Unix(), Path: path, Kind: kind, Text: text}
	if path != "" {
		a.Project = filepath.Base(path)
	}
	actLog = append(actLog, a)
	if len(actLog) > 500 {
		actLog = actLog[len(actLog)-500:]
	}
	writeJSON(activityFile(), actLog)
}

func Activities() []Activity {
	actMu.Lock()
	defer actMu.Unlock()
	loadActivity()
	out := make([]Activity, len(actLog))
	for i := range actLog {
		out[i] = actLog[len(actLog)-1-i]
	}
	return out
}

// errDetails extracts technical details from an error, if any.
func errDetails(err error) string {
	var ge *GitError
	if errors.As(err, &ge) {
		return ge.Details()
	}
	return ""
}
