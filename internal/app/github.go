package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func ghAPI(token, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ghAPIBase+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		json.Unmarshal(b, &e)
		msg := e.Message
		for _, x := range e.Errors {
			msg += "; " + x.Message
		}
		return fmt.Errorf("GitHub: %s (%s)", msg, resp.Status)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// GitHubCreate creates a private repo and invites the given nicks.
func GitHubCreate(cfg *Config, job *Job, name string, nicks []string) (any, error) {
	if cfg.Token == "" {
		return nil, errors.New("сначала вставь GitHub-токен в настройках")
	}
	// resolve nicks first, so a typo doesn't leave a half-created project
	var users []*RegUser
	for _, n := range nicks {
		if strings.TrimSpace(n) == "" {
			continue
		}
		u, _, err := ResolvePerson(cfg, n)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		default:
			b.WriteString(translit(r))
		}
	}
	repoName := strings.Trim(b.String(), "-.")
	if repoName == "" {
		repoName = "fl-project"
	}
	var repo struct {
		CloneURL string `json:"clone_url"`
		HTMLURL  string `json:"html_url"`
		FullName string `json:"full_name"`
	}
	err := ghAPI(cfg.Token, "POST", "/user/repos", map[string]any{"name": repoName, "private": true,
		"description": "FL Studio проект (Программа Узбека)"}, &repo)
	if err != nil {
		if strings.Contains(err.Error(), "name already exists") {
			return nil, fmt.Errorf("у тебя на GitHub уже есть репозиторий «%s» — выбери другое название", repoName)
		}
		return nil, err
	}
	job.Logf("Создан приватный проект на GitHub")
	// mark it, so it shows up in "Доступные" for everyone invited
	ghAPI(cfg.Token, "PUT", "/repos/"+repo.FullName+"/topics", map[string]any{"names": []string{appTopic}}, nil)
	for _, u := range users {
		if err := ghAPI(cfg.Token, "PUT", "/repos/"+repo.FullName+"/collaborators/"+u.Login, map[string]any{"permission": "push"}, nil); err != nil {
			job.Logf("⚠ Не удалось пригласить %s: %v", u.Nick, err)
		} else {
			job.Logf("Приглашение отправлено: %s — он увидит его в программе", u.Nick)
		}
	}
	return map[string]string{"cloneUrl": repo.CloneURL, "url": repo.HTMLURL}, nil
}

func translit(r rune) string {
	m := map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y",
		'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h",
		'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ы': "y", 'э': "e", 'ю': "yu", 'я': "ya"}
	lr := []rune(strings.ToLower(string(r)))[0]
	if s, ok := m[lr]; ok {
		return s
	}
	return ""
}
