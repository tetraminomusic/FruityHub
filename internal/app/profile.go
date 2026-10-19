package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// The profile lets you sign in with the same account on another computer:
// nick, nick directory, folders of the project list (and which project sits
// where), and "trusted" friends live in a private repo of your GitHub account.

const profileRepoName = "programma-uzbeka-profile"

type ProfileProject struct {
	Repo  string `json:"repo"` // owner/name
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
}

type Profile struct {
	Nick     string           `json:"nick"`
	Registry string           `json:"registry,omitempty"`
	Groups   []Group          `json:"groups"`
	Projects []ProfileProject `json:"projects"`
	Trusted  []string         `json:"trusted,omitempty"`
	Updated  int64            `json:"updated"`
	Device   string           `json:"device,omitempty"`
}

func profilePath(login string) string {
	return fmt.Sprintf("/repos/%s/%s/contents/profile.json", login, profileRepoName)
}

// LoadProfile returns nil (no error) when the account has no profile yet.
func LoadProfile(cfg *Config) (*Profile, string, error) {
	if cfg.Token == "" || cfg.GitHubLogin == "" {
		return nil, "", errors.New("сначала войди через GitHub-токен")
	}
	var file struct {
		SHA     string `json:"sha"`
		Content string `json:"content"`
	}
	if err := ghAPI(cfg.Token, "GET", profilePath(cfg.GitHubLogin), nil, &file); err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, "", nil
		}
		return nil, "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, "", err
	}
	var p Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "", err
	}
	return &p, file.SHA, nil
}

// BuildProfile captures the shareable part of this computer's settings.
func BuildProfile(cfg *Config, prev *Profile) *Profile {
	host, _ := os.Hostname()
	p := &Profile{Nick: cfg.UserName, Registry: cfg.Registry, Groups: cfg.Groups, Trusted: cfg.TrustedRequesters,
		Updated: time.Now().Unix(), Device: host + " (" + OSName() + ")"}
	seen := map[string]bool{}
	for _, lp := range cfg.Projects {
		r := (&Git{Dir: lp.Path, Cfg: cfg}).Remote()
		k := slugKey(r)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		o, n, _ := repoSlug(r)
		p.Projects = append(p.Projects, ProfileProject{Repo: o + "/" + n, Name: n, Group: lp.Group})
	}
	// keep projects known from other computers that aren't downloaded here
	if prev != nil {
		for _, pp := range prev.Projects {
			if !seen[strings.ToLower(pp.Repo)] {
				p.Projects = append(p.Projects, pp)
			}
		}
	}
	if p.Groups == nil {
		p.Groups = []Group{}
	}
	return p
}

// SaveProfile uploads the profile (creates the private repo on first use).
func SaveProfile(cfg *Config) error {
	if cfg.Token == "" || cfg.GitHubLogin == "" || cfg.UserName == "" {
		return nil
	}
	prev, sha, err := LoadProfile(cfg)
	if err != nil {
		return err
	}
	if prev == nil {
		err := ghAPI(cfg.Token, "POST", "/user/repos", map[string]any{"name": profileRepoName, "private": true, "auto_init": true,
			"description": "Настройки «Программы Узбека» для входа с другого компьютера. Не удаляй."}, nil)
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	p := BuildProfile(cfg, prev)
	b, _ := json.MarshalIndent(p, "", "  ")
	body := map[string]any{"message": "профиль: " + p.Device, "content": base64.StdEncoding.EncodeToString(b)}
	if sha != "" {
		body["sha"] = sha
	}
	if err := ghAPI(cfg.Token, "PUT", profilePath(cfg.GitHubLogin), body, nil); err != nil {
		return err
	}
	cfg.ProfileUpdated = p.Updated
	return cfg.Save()
}

// ApplyProfile takes the synced settings into this computer's config.
func ApplyProfile(cfg *Config, p *Profile) {
	if p == nil {
		return
	}
	if p.Nick != "" {
		cfg.UserName = p.Nick
	}
	if p.Registry != "" {
		cfg.Registry = p.Registry
	}
	cfg.Groups = p.Groups
	cfg.TrustedRequesters = p.Trusted
	byRepo := map[string]string{}
	for _, pp := range p.Projects {
		byRepo[strings.ToLower(pp.Repo)] = pp.Group
	}
	for i := range cfg.Projects {
		if g, ok := byRepo[slugKey((&Git{Dir: cfg.Projects[i].Path, Cfg: cfg}).Remote())]; ok {
			cfg.Projects[i].Group = g
		}
	}
	cfg.ProfileUpdated = p.Updated
}

type LoginResult struct {
	Login      string `json:"login"`
	Nick       string `json:"nick"`
	HasProfile bool   `json:"hasProfile"`
	Projects   int    `json:"projects"`
	Device     string `json:"device,omitempty"`
}

// Login signs this computer into an existing account using only the token.
func Login(cfg *Config, token, registry string) (*LoginResult, error) {
	login, err := GitHubLogin(token)
	if err != nil {
		return nil, err
	}
	cfg.Token, cfg.GitHubLogin = token, login
	if registry != "" {
		cfg.Registry = registry
	}
	res := &LoginResult{Login: login}
	prof, _, err := LoadProfile(cfg)
	if err != nil {
		return nil, err
	}
	if prof != nil {
		ApplyProfile(cfg, prof)
		res.HasProfile, res.Projects, res.Device = true, len(prof.Projects), prof.Device
	} else if nick := NickForLogin(cfg, login); nick != "" {
		cfg.UserName = nick
	}
	res.Nick = cfg.UserName
	if cfg.UserName != "" {
		if _, err := RegisterNick(cfg); err == nil {
			// already registered → this just confirms it
		}
	}
	return res, cfg.Save()
}
