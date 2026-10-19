package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The nick directory: a small public GitHub repo whose issues are
// registrations ("nick:<name>"). GitHub itself vouches for the author of an
// issue, so a nick always points to the real GitHub account that took it.
// The first (oldest) registration of a nick wins.

// DefaultRegistry is "owner/repo", baked in at build time (build.sh REGISTRY=…).
var DefaultRegistry = ""

const registryRepoName = "programma-uzbeka-users"

var ErrNoRegistry = errors.New("справочник ников не настроен — укажи его в ⚙ Настройках (владелец/репозиторий)")

func (c *Config) RegistryRepo() string {
	if r := strings.Trim(strings.TrimSpace(c.Registry), "/"); r != "" {
		if !strings.Contains(r, "/") { // just an owner login
			r += "/" + registryRepoName
		}
		return r
	}
	return DefaultRegistry
}

type RegUser struct {
	Nick  string `json:"nick"`
	Login string `json:"login"`
	OS    string `json:"os,omitempty"`
	Issue int    `json:"issue"`
}

func GitHubLogin(token string) (string, error) {
	if token == "" {
		return "", errors.New("нужен GitHub-токен")
	}
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

func normNick(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validNick(n string) error {
	n = strings.TrimSpace(n)
	if len([]rune(n)) < 2 || len([]rune(n)) > 32 {
		return errors.New("ник должен быть от 2 до 32 символов")
	}
	if strings.ContainsAny(n, `<>:"/\|?*`) {
		return errors.New(`в нике нельзя использовать символы < > : " / \ | ? *`)
	}
	return nil
}

var (
	regMu    sync.Mutex
	regCache map[string]RegUser
	regTime  time.Time
	regRepo  string
)

func invalidateRegistry() {
	regMu.Lock()
	regCache = nil
	regMu.Unlock()
}

// RegistryUsers returns nick -> user (lowercase nick keys).
func RegistryUsers(cfg *Config) (map[string]RegUser, error) {
	repo := cfg.RegistryRepo()
	if repo == "" {
		return nil, ErrNoRegistry
	}
	regMu.Lock()
	if regCache != nil && regRepo == repo && time.Since(regTime) < 2*time.Minute {
		c := regCache
		regMu.Unlock()
		return c, nil
	}
	regMu.Unlock()
	type issue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		PR any `json:"pull_request"`
	}
	var all []issue
	for page := 1; page <= 20; page++ {
		var batch []issue
		if err := ghAPI(cfg.Token, "GET", fmt.Sprintf("/repos/%s/issues?state=open&per_page=100&page=%d", repo, page), nil, &batch); err != nil {
			if strings.Contains(err.Error(), "404") {
				return nil, fmt.Errorf("справочник %s не найден. Его создаст владелец при первой регистрации", repo)
			}
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Number < all[j].Number })
	users := map[string]RegUser{}
	for _, is := range all {
		if is.PR != nil || !strings.HasPrefix(is.Title, "nick:") {
			continue
		}
		nick := strings.TrimSpace(strings.TrimPrefix(is.Title, "nick:"))
		k := normNick(nick)
		if k == "" {
			continue
		}
		if _, taken := users[k]; taken {
			continue // first registration wins
		}
		u := RegUser{Nick: nick, Login: is.User.Login, Issue: is.Number}
		var body RegUser
		if json.Unmarshal([]byte(is.Body), &body) == nil {
			u.OS = body.OS
		}
		users[k] = u
	}
	regMu.Lock()
	regCache, regTime, regRepo = users, time.Now(), repo
	regMu.Unlock()
	return users, nil
}

// ensureRegistry creates the public directory repo when the owner registers first.
func ensureRegistry(cfg *Config, login string) error {
	repo := cfg.RegistryRepo()
	err := ghAPI(cfg.Token, "GET", "/repos/"+repo, nil, nil)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "404") {
		return err
	}
	owner, name, _ := strings.Cut(repo, "/")
	if !strings.EqualFold(owner, login) {
		return fmt.Errorf("справочник %s ещё не создан — его создаст %s при своей первой регистрации", repo, owner)
	}
	return ghAPI(cfg.Token, "POST", "/user/repos", map[string]any{"name": name, "private": false, "has_issues": true,
		"description": "Справочник ников «Программы Узбека»: каждая заявка (issue) — регистрация ника. Не закрывай чужие заявки."}, nil)
}

// RegisterNick verifies the token, stores the GitHub login and publishes the
// nick in the directory. Returns a note for the user when only local saving was possible.
func RegisterNick(cfg *Config) (string, error) {
	nick := strings.TrimSpace(cfg.UserName)
	if err := validNick(nick); err != nil {
		return "", err
	}
	login, err := GitHubLogin(cfg.Token)
	if err != nil {
		return "", err
	}
	cfg.GitHubLogin = login
	note := ""
	if cfg.RegistryRepo() == "" {
		// nobody configured a directory: this user becomes its owner
		cfg.Registry = login
		note = "Справочник ников создан на твоём GitHub. Скажи другу при регистрации вписать в поле «Справочник ников» твой логин: " + login
	}
	if err := ensureRegistry(cfg, login); err != nil {
		return "", err
	}
	invalidateRegistry()
	users, err := RegistryUsers(cfg)
	if err != nil {
		return "", err
	}
	if u, ok := users[normNick(nick)]; ok {
		if !strings.EqualFold(u.Login, login) {
			return "", fmt.Errorf("ник «%s» уже занят (@%s). Выбери другой", u.Nick, u.Login)
		}
		cfg.Registered = true
		return note, nil
	}
	body, _ := json.Marshal(RegUser{Nick: nick, Login: login, OS: OSName()})
	var created struct {
		Number int `json:"number"`
	}
	if err := ghAPI(cfg.Token, "POST", "/repos/"+cfg.RegistryRepo()+"/issues",
		map[string]any{"title": "nick:" + nick, "body": string(body)}, &created); err != nil {
		return "", err
	}
	// release my previous nicks
	for _, u := range users {
		if strings.EqualFold(u.Login, login) {
			ghAPI(cfg.Token, "PATCH", fmt.Sprintf("/repos/%s/issues/%d", cfg.RegistryRepo(), u.Issue), map[string]any{"state": "closed"}, nil)
		}
	}
	invalidateRegistry()
	cfg.Registered = true
	return note, nil
}

// knownMachines: participants of all my projects (they publish nick + GitHub login).
func knownMachines(cfg *Config) []Machine {
	var out []Machine
	for _, lp := range cfg.Projects {
		ents, _ := os.ReadDir(filepath.Join(lp.Path, ".flsync", "machines"))
		for _, e := range ents {
			var m Machine
			if readJSON(filepath.Join(lp.Path, ".flsync", "machines", e.Name()), &m) == nil && m.User != "" {
				out = append(out, m)
			}
		}
	}
	return out
}

// LookupNick finds the GitHub login for an in-app nick.
func LookupNick(cfg *Config, nick string) (*RegUser, error) {
	k := normNick(strings.TrimPrefix(nick, "@"))
	if k == "" {
		return nil, errors.New("пустой ник")
	}
	users, regErr := RegistryUsers(cfg)
	if u, ok := users[k]; ok {
		return &u, nil
	}
	for _, m := range knownMachines(cfg) {
		if normNick(m.User) == k && m.GitHub != "" {
			return &RegUser{Nick: m.User, Login: m.GitHub, OS: m.OS}, nil
		}
	}
	if regErr != nil && !errors.Is(regErr, ErrNoRegistry) {
		return nil, regErr
	}
	return nil, fmt.Errorf("пользователь с ником «%s» не найден. Пусть он запустит программу и зарегистрируется", strings.TrimSpace(nick))
}

// NickForLogin is the reverse lookup (empty if unknown).
func NickForLogin(cfg *Config, login string) string {
	if users, err := RegistryUsers(cfg); err == nil {
		for _, u := range users {
			if strings.EqualFold(u.Login, login) {
				return u.Nick
			}
		}
	}
	for _, m := range knownMachines(cfg) {
		if strings.EqualFold(m.GitHub, login) {
			return m.User
		}
	}
	return ""
}

// ---------- GitHub invitations ----------

type Invitation struct {
	ID          int64  `json:"id"`
	Repo        string `json:"repo"`
	Name        string `json:"name"`
	CloneURL    string `json:"cloneUrl"`
	Inviter     string `json:"inviter"`
	InviterNick string `json:"inviterNick"`
}

func PendingInvitations(cfg *Config) ([]Invitation, error) {
	if cfg.Token == "" {
		return nil, nil
	}
	var raw []struct {
		ID         int64 `json:"id"`
		Repository struct {
			FullName string `json:"full_name"`
			Name     string `json:"name"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
		Inviter struct {
			Login string `json:"login"`
		} `json:"inviter"`
	}
	if err := ghAPI(cfg.Token, "GET", "/user/repository_invitations", nil, &raw); err != nil {
		return nil, err
	}
	var out []Invitation
	for _, r := range raw {
		out = append(out, Invitation{ID: r.ID, Repo: r.Repository.FullName, Name: r.Repository.Name,
			CloneURL: r.Repository.HTMLURL + ".git", Inviter: r.Inviter.Login, InviterNick: NickForLogin(cfg, r.Inviter.Login)})
	}
	return out, nil
}

func AcceptInvitation(cfg *Config, id int64) error {
	return ghAPI(cfg.Token, "PATCH", fmt.Sprintf("/user/repository_invitations/%d", id), nil, nil)
}

// InviteToProject adds a participant (by nick) to the project's GitHub repo.
func InviteToProject(cfg *Config, remote, nick string) (*RegUser, error) {
	owner, repo, ok := repoSlug(remote)
	if !ok {
		return nil, errors.New("приглашать по нику можно только в проект на GitHub")
	}
	u, _, err := ResolvePerson(cfg, nick)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(u.Login, cfg.GitHubLogin) {
		return nil, errors.New("это ты сам 🙂")
	}
	err = ghAPI(cfg.Token, "PUT", fmt.Sprintf("/repos/%s/%s/collaborators/%s", owner, repo, u.Login), map[string]any{"permission": "push"}, nil)
	return u, err
}

type Participant struct {
	Login    string `json:"login"`
	Nick     string `json:"nick"`
	Pending  bool   `json:"pending"` // invited, not accepted yet
	Admin    bool   `json:"admin"`
	InviteID int64  `json:"inviteId,omitempty"`
	Me       bool   `json:"me"`
}

// Participants lists collaborators of the project's GitHub repo with their nicks.
func Participants(cfg *Config, remote string) ([]Participant, error) {
	owner, repo, ok := repoSlug(remote)
	if !ok {
		return nil, errors.New("список участников доступен для проектов на GitHub")
	}
	var cols []struct {
		Login       string `json:"login"`
		Permissions struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := ghAPI(cfg.Token, "GET", fmt.Sprintf("/repos/%s/%s/collaborators?per_page=100", owner, repo), nil, &cols); err != nil {
		return nil, err
	}
	var out []Participant
	for _, c := range cols {
		out = append(out, Participant{Login: c.Login, Nick: NickForLogin(cfg, c.Login), Admin: c.Permissions.Admin || strings.EqualFold(c.Login, owner),
			Me: strings.EqualFold(c.Login, cfg.GitHubLogin)})
	}
	var inv []struct {
		ID      int64 `json:"id"`
		Invitee struct {
			Login string `json:"login"`
		} `json:"invitee"`
	}
	if ghAPI(cfg.Token, "GET", fmt.Sprintf("/repos/%s/%s/invitations", owner, repo), nil, &inv) == nil {
		for _, i := range inv {
			out = append(out, Participant{Login: i.Invitee.Login, Nick: NickForLogin(cfg, i.Invitee.Login), Pending: true, InviteID: i.ID})
		}
	}
	return out, nil
}

// RemoveParticipant removes a collaborator (or cancels a pending invitation).
func RemoveParticipant(cfg *Config, remote, login string, inviteID int64) error {
	owner, repo, ok := repoSlug(remote)
	if !ok {
		return errors.New("участниками можно управлять только в проекте на GitHub")
	}
	if strings.EqualFold(login, owner) {
		return errors.New("владельца проекта убрать нельзя")
	}
	if inviteID != 0 {
		return ghAPI(cfg.Token, "DELETE", fmt.Sprintf("/repos/%s/%s/invitations/%d", owner, repo, inviteID), nil, nil)
	}
	err := ghAPI(cfg.Token, "DELETE", fmt.Sprintf("/repos/%s/%s/collaborators/%s", owner, repo, login), nil, nil)
	if err != nil && strings.Contains(err.Error(), "403") {
		return errors.New("убирать участников может только владелец проекта")
	}
	return err
}

// ---------- projects available to download ----------

const appTopic = "programma-uzbeka"

type AvailableRepo struct {
	FullName  string `json:"fullName"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	OwnerNick string `json:"ownerNick"`
	CloneURL  string `json:"cloneUrl"`
	Pushed    string `json:"pushed"`
	SizeKB    int64  `json:"sizeKb"`
	IsApp     bool   `json:"isApp"`
	Mine      bool   `json:"mine"`
	Group     string `json:"group,omitempty"` // from the synced profile
}

func slugKey(remote string) string {
	o, r, ok := repoSlug(remote)
	if !ok {
		return ""
	}
	return strings.ToLower(o + "/" + r)
}

// AvailableRepos lists GitHub repos you own or collaborate on that are not
// downloaded on this computer yet.
func AvailableRepos(cfg *Config, prof *Profile) ([]AvailableRepo, error) {
	if cfg.Token == "" {
		return nil, errors.New("нужен GitHub-токен")
	}
	local := map[string]bool{}
	for _, lp := range cfg.Projects {
		if r := (&Git{Dir: lp.Path, Cfg: cfg}).Remote(); r != "" {
			local[slugKey(r)] = true
		}
	}
	groups := map[string]string{}
	if prof != nil {
		for _, pp := range prof.Projects {
			groups[strings.ToLower(pp.Repo)] = pp.Group
		}
	}
	type ghRepo struct {
		FullName    string   `json:"full_name"`
		Name        string   `json:"name"`
		CloneURL    string   `json:"clone_url"`
		PushedAt    string   `json:"pushed_at"`
		Size        int64    `json:"size"`
		Description string   `json:"description"`
		Topics      []string `json:"topics"`
		Owner       struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	out := []AvailableRepo{}
	for page := 1; page <= 5; page++ {
		var batch []ghRepo
		if err := ghAPI(cfg.Token, "GET", fmt.Sprintf("/user/repos?affiliation=owner,collaborator,organization_member&sort=pushed&per_page=100&page=%d", page), nil, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			k := strings.ToLower(r.FullName)
			if local[k] || strings.EqualFold(r.Name, registryRepoName) || strings.EqualFold(r.Name, profileRepoName) {
				continue
			}
			a := AvailableRepo{FullName: r.FullName, Name: r.Name, Owner: r.Owner.Login, CloneURL: r.CloneURL, Pushed: r.PushedAt,
				SizeKB: r.Size, Mine: strings.EqualFold(r.Owner.Login, cfg.GitHubLogin), Group: groups[k]}
			a.IsApp = strings.Contains(r.Description, "Программа Узбека") || a.Group != ""
			for _, t := range r.Topics {
				a.IsApp = a.IsApp || t == appTopic
			}
			if !a.Mine {
				a.OwnerNick = NickForLogin(cfg, a.Owner)
			}
			out = append(out, a)
		}
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// ResolvePerson: a nick from the directory, or else an existing GitHub login
// (for friends who haven't installed the app yet). registered=false then.
func ResolvePerson(cfg *Config, s string) (*RegUser, bool, error) {
	u, err := LookupNick(cfg, s)
	if err == nil {
		return u, true, nil
	}
	login := strings.TrimPrefix(strings.TrimSpace(s), "@")
	var gu struct {
		Login string `json:"login"`
	}
	if login == "" || strings.ContainsAny(login, " /") || ghAPI(cfg.Token, "GET", "/users/"+login, nil, &gu) != nil {
		return nil, false, fmt.Errorf("«%s» не найден ни среди ников программы, ни на GitHub", strings.TrimSpace(s))
	}
	if gu.Login == "" {
		gu.Login = login
	}
	return &RegUser{Nick: gu.Login, Login: gu.Login}, false, nil
}
