package app

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Удаление репозиториев проектов с GitHub — чтобы не копились старые и недоделанные.
// Для этого у GitHub-токена должна стоять галочка delete_repo (Tokens (classic)).

type MyRepo struct {
	FullName string `json:"fullName"`
	Name     string `json:"name"`
	CloneURL string `json:"cloneUrl"`
	SizeKB   int64  `json:"sizeKb"`
	PushedAt string `json:"pushedAt"`
	Private  bool   `json:"private"`
	Local    string `json:"local,omitempty"`   // папка на этом компьютере
	Server   string `json:"server,omitempty"`  // название проекта на сервере FruityHub
	Members  int    `json:"members,omitempty"` // участников на сервере
}

// MyProjectRepos — репозитории на моём GitHub, созданные FruityHub (тема programma-uzbeka),
// и где каждый из них используется: на этом компьютере, на сервере.
func MyProjectRepos(cfg *Config) ([]MyRepo, error) {
	if cfg.Token == "" {
		return nil, errors.New("нужен GitHub-токен (Настройки)")
	}
	var out []MyRepo
	for page := 1; page <= 10; page++ {
		var rs []struct {
			FullName string   `json:"full_name"`
			Name     string   `json:"name"`
			CloneURL string   `json:"clone_url"`
			Size     int64    `json:"size"`
			PushedAt string   `json:"pushed_at"`
			Private  bool     `json:"private"`
			Topics   []string `json:"topics"`
		}
		if err := ghAPI(cfg.Token, "GET", fmt.Sprintf("/user/repos?affiliation=owner&per_page=100&page=%d", page), nil, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			mine := false
			for _, t := range r.Topics {
				mine = mine || t == appTopic
			}
			if mine {
				out = append(out, MyRepo{FullName: r.FullName, Name: r.Name, CloneURL: r.CloneURL, SizeKB: r.Size, PushedAt: r.PushedAt, Private: r.Private})
			}
		}
		if len(rs) < 100 {
			break
		}
	}
	local := map[string]string{}
	for _, lp := range cfg.Projects {
		if r := (&Git{Dir: lp.Path, Cfg: cfg}).Remote(); r != "" {
			local[normRemote(r)] = lp.Path
			if o, n, ok := repoSlug(r); ok {
				local[strings.ToLower(o+"/"+n)] = lp.Path
			}
		}
	}
	var srv []HubProject
	if cfg.Hub().Registered() {
		srv, _ = cfg.Hub().Projects(0)
	}
	for i := range out {
		r := &out[i]
		if p, ok := local[strings.ToLower(r.FullName)]; ok {
			r.Local = p
		} else if p, ok := local[normRemote(r.CloneURL)]; ok {
			r.Local = p
		}
		for _, hp := range srv {
			if strings.EqualFold(hp.Repo, r.FullName) {
				r.Server, r.Members = hp.Name, len(hp.Members)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PushedAt > out[j].PushedAt })
	return out, nil
}

// DeleteGitHubRepo удаляет репозиторий с GitHub и убирает его проект с сервера FruityHub.
// Файлы на компьютерах (и у участников) остаются; у папки на этом компьютере снимается адрес репозитория.
func DeleteGitHubRepo(cfg *Config, full string) error {
	full = strings.Trim(strings.TrimSpace(full), "/")
	owner, _, ok := strings.Cut(full, "/")
	if !ok {
		return errors.New("неверное имя репозитория")
	}
	if cfg.GitHubLogin != "" && !strings.EqualFold(owner, cfg.GitHubLogin) {
		return fmt.Errorf("репозиторий %s не твой — удалить его может только владелец", full)
	}
	err := ghAPI(cfg.Token, "DELETE", "/repos/"+full, nil, nil)
	if err != nil && !strings.Contains(err.Error(), "404") {
		if strings.Contains(err.Error(), "403") || strings.Contains(strings.ToLower(err.Error()), "admin rights") {
			return errors.New("GitHub не дал удалить репозиторий: у токена нет галочки delete_repo. Создай новый токен (Tokens (classic)) с галочками repo и delete_repo и впиши его в Настройках")
		}
		return err
	}
	DevLog("info", 0, false, "репозиторий %s удалён с GitHub", full)
	// с сервера FruityHub
	if cfg.Hub().Registered() {
		if ps, err := cfg.Hub().Projects(0); err == nil {
			for _, hp := range ps {
				if strings.EqualFold(hp.Repo, full) && hp.IsOwner {
					if err := cfg.Hub().DeleteProject(hp.ID); err != nil {
						DevLog("info", 0, true, "проект %s не убран с сервера: %v", hp.Name, err)
					}
				}
			}
		}
	}
	// у папки на этом компьютере — убрать адрес, чтобы она не ссылалась на пустоту
	for _, lp := range cfg.Projects {
		g := &Git{Dir: lp.Path, Cfg: cfg}
		r := g.Remote()
		if r == "" {
			continue
		}
		if o, n, ok := repoSlug(r); ok && strings.EqualFold(o+"/"+n, full) {
			g.RunQuiet("remote", "remove", "origin")
			g.RunQuiet("branch", "--unset-upstream")
		}
	}
	return nil
}

func (h *Hub) DeleteProject(id string) error {
	err := h.do("DELETE", "/api/projects/"+url.PathEscape(id), nil, nil)
	h.forget()
	return err
}
