package app

import (
	"errors"
	"fmt"
	"strings"
)

// Projects live in the owner's own GitHub account: one FL project = one
// private repository. Everybody uses their own GitHub token; access to a
// project is a GitHub collaborator invitation, which the app sends and
// accepts by itself when people invite each other through our server.

const appTopic = "programma-uzbeka"

func GitHubLogin(token string) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := ghAPI(token, "GET", "/user", nil, &u); err != nil {
		if strings.Contains(err.Error(), "401") {
			return "", errors.New("GitHub не принял токен — проверь, что он скопирован целиком и не истёк")
		}
		return "", err
	}
	return u.Login, nil
}

func repoNameFor(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		default:
			b.WriteString(translit(r))
		}
	}
	n := strings.Trim(b.String(), "-.")
	if n == "" {
		n = "fl-project"
	}
	return n
}

// GitHubCreateRepo makes a private repo for a project on the user's account.
func GitHubCreateRepo(cfg *Config, name string) (fullName, cloneURL string, err error) {
	if cfg.Token == "" {
		return "", "", errors.New("нужен GitHub-токен (Настройки)")
	}
	base := repoNameFor(name)
	repo := base
	for i := 2; ; i++ {
		var r struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		}
		err = ghAPI(cfg.Token, "POST", "/user/repos", map[string]any{"name": repo, "private": true,
			"description": "FL Studio проект «" + strings.TrimSpace(name) + "» (FruityHub)", "has_issues": false, "has_wiki": false}, &r)
		if err == nil {
			ghAPI(cfg.Token, "PUT", "/repos/"+r.FullName+"/topics", map[string]any{"names": []string{appTopic}}, nil)
			return r.FullName, r.CloneURL, nil
		}
		if !strings.Contains(err.Error(), "already exists") || i > 30 {
			return "", "", err
		}
		repo = fmt.Sprintf("%s-%d", base, i)
	}
}

// ghAddCollaborator sends a GitHub invitation (only the repo owner can).
func ghAddCollaborator(cfg *Config, repo, login string) error {
	if login == "" {
		return errors.New("у этого пользователя не указан GitHub")
	}
	err := ghAPI(cfg.Token, "PUT", "/repos/"+repo+"/collaborators/"+login, map[string]any{"permission": "push"}, nil)
	if err != nil && (strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "404")) {
		return errors.New("звать людей в проект может только его владелец на GitHub")
	}
	return err
}

func ghRemoveCollaborator(cfg *Config, repo, login string) error {
	if login == "" {
		return nil
	}
	return ghAPI(cfg.Token, "DELETE", "/repos/"+repo+"/collaborators/"+login, nil, nil)
}

// ghAcceptInvitation accepts the pending GitHub invitation to repo, if there is one.
func ghAcceptInvitation(cfg *Config, repo string) error {
	var raw []struct {
		ID         int64 `json:"id"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := ghAPI(cfg.Token, "GET", "/user/repository_invitations", nil, &raw); err != nil {
		return err
	}
	for _, r := range raw {
		if strings.EqualFold(r.Repository.FullName, repo) {
			return ghAPI(cfg.Token, "PATCH", fmt.Sprintf("/user/repository_invitations/%d", r.ID), nil, nil)
		}
	}
	return nil // already a collaborator (or the owner)
}

func ghCollaborators(cfg *Config, repo string) ([]string, error) {
	var cols []struct {
		Login string `json:"login"`
	}
	if err := ghAPI(cfg.Token, "GET", "/repos/"+repo+"/collaborators?per_page=100", nil, &cols); err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cols {
		out = append(out, c.Login)
	}
	return out, nil
}

// ghRepoInfo: does the repo exist for me, and am I its admin?
func ghRepoInfo(cfg *Config, repo string) (admin bool, cloneURL string, err error) {
	var r struct {
		CloneURL    string `json:"clone_url"`
		Permissions struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := ghAPI(cfg.Token, "GET", "/repos/"+repo, nil, &r); err != nil {
		return false, "", err
	}
	return r.Permissions.Admin, r.CloneURL, nil
}
