package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// LocalProject is a project folder registered in this app instance.
type LocalProject struct {
	Path            string   `json:"path"`
	RelinkOnReceive bool     `json:"relinkOnReceive"`
	SeenComments    int64    `json:"seenComments"` // newest comment time the user has seen
	Group           string   `json:"group,omitempty"`
	KeepFLPs        []string `json:"keepFlps,omitempty"` // extra .flp the user said are just versions
}

// Group is a folder in the project list (local only).
type Group struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Collapsed bool   `json:"collapsed"`
}

// Config is the per-machine app configuration (never committed).
type Config struct {
	UserName        string         `json:"userName"` // nick on the server (= in the app)
	UserID          string         `json:"userId,omitempty"`
	GitHubLogin     string         `json:"githubLogin,omitempty"`
	Server          string         `json:"server,omitempty"`      // server address (empty = built-in)
	DeviceToken     string         `json:"deviceToken,omitempty"` // this device's key on the server
	Sharing         string         `json:"sharing,omitempty"`     // who may download plugins from me: off|ask|members|all
	Token           string         `json:"token,omitempty"`       // GitHub token: projects are the user's own repos
	ExtraPluginDirs []string       `json:"extraPluginDirs"`
	Projects        []LocalProject `json:"projects"`
	Groups          []Group        `json:"groups,omitempty"`
	LastParent      string         `json:"lastParent,omitempty"`

	hub *Hub
}

var cfgMu sync.Mutex

const AppName = "прога узбека"

func ConfigDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d, _ = os.UserHomeDir()
	}
	p := filepath.Join(d, "ProgrammaUzbeka")
	if _, err := os.Stat(p); err != nil {
		// migrate settings of the first version (FL Sync)
		if _, err := os.Stat(filepath.Join(d, "FLSync")); err == nil {
			os.Rename(filepath.Join(d, "FLSync"), p)
		}
	}
	os.MkdirAll(p, 0o755)
	return p
}

func LoadConfig() *Config {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := &Config{}
	b, err := os.ReadFile(filepath.Join(ConfigDir(), "config.json"))
	if err == nil {
		json.Unmarshal(b, c)
	}
	return c
}

func (c *Config) Save() error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := filepath.Join(ConfigDir(), "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(ConfigDir(), "config.json"))
}

func (c *Config) Project(path string) *LocalProject {
	for i := range c.Projects {
		if samePath(c.Projects[i].Path, path) {
			return &c.Projects[i]
		}
	}
	return nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// MachineID is how this participant is named in shared files.
func (c *Config) MachineID() string {
	n := strings.TrimSpace(c.UserName)
	if n == "" {
		n, _ = os.Hostname()
	}
	return safeName(n)
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func OSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	return runtime.GOOS
}

// SharedSettings live in the repo (.flsync/settings.json) so both
// participants follow the same rules.
type SharedSettings struct {
	IgnoreUnused   bool     `json:"ignoreUnused"`
	CollectSamples bool     `json:"collectSamples"`
	OrganizeAudio  bool     `json:"organizeAudio"` // keep the root clean: audio goes to Audio/
	AlwaysInclude  []string `json:"alwaysInclude"` // rel paths; trailing "/" = whole folder
	LFSExtensions  []string `json:"lfsExtensions"`
}

func DefaultShared() SharedSettings {
	return SharedSettings{
		IgnoreUnused:   true,
		CollectSamples: true,
		OrganizeAudio:  true,
		LFSExtensions: []string{"wav", "mp3", "flac", "ogg", "aif", "aiff", "m4a", "wv", "rx2", "rex", "sf2",
			"opus", "zip", "mp4", "mov", "dwp", "nmsv", "fxp", "fxb", "vstpreset"},
	}
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.MarshalIndent(v, "", "  ")
	b = append(b, '\n')
	if old, err := os.ReadFile(path); err == nil && string(old) == string(b) {
		return nil // unchanged: avoid noise in git
	}
	return os.WriteFile(path, b, 0o644)
}

func LoadShared(dir string) SharedSettings {
	s := DefaultShared()
	readJSON(filepath.Join(dir, ".flsync", "settings.json"), &s)
	if len(s.LFSExtensions) == 0 {
		s.LFSExtensions = DefaultShared().LFSExtensions
	}
	return s
}
