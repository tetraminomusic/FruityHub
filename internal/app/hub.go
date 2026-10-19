package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultServer is the address of "прога узбека" server baked in at build time.
var DefaultServer = ""

// Hub is the client of our own server: users (one device = one user),
// projects, invitations, plugin transfers. FL projects themselves live on
// GitHub; the server hands out short-lived access tokens for them.
type Hub struct {
	cfg *Config
	cl  *http.Client

	mu       sync.Mutex
	projects []HubProject
	projAt   time.Time
	tokens   map[string]hubToken // project id -> token
	inbox    *Inbox
	invSig   string
	invAt    time.Time
}

type hubToken struct {
	tok string
	exp time.Time
}

type HubUser struct {
	ID       string `json:"id"`
	Nick     string `json:"nick"`
	OS       string `json:"os"`
	LastSeen int64  `json:"lastSeen"`
	Online   bool   `json:"online"`
	Sharing  string `json:"sharing"`
}

type HubInvite struct {
	ID      string  `json:"id"`
	Project string  `json:"project"`
	Name    string  `json:"name"`
	From    HubUser `json:"from"`
	To      HubUser `json:"to"`
	Created int64   `json:"created"`
}

type HubProject struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Repo     string      `json:"repo"`
	CloneURL string      `json:"cloneUrl"`
	Owner    HubUser     `json:"owner"`
	Members  []HubUser   `json:"members"`
	Invited  []HubInvite `json:"invited"`
	IsOwner  bool        `json:"isOwner"`
}

// PluginRequest is a plugin transfer between two users (through the server).
type PluginRequest struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	FromOS   string `json:"fromOs"`
	Plugin   string `json:"plugin"`
	Format   string `json:"format"`
	Vendor   string `json:"vendor,omitempty"`
	Status   string `json:"status"`
	Kind     string `json:"kind,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Note     string `json:"note,omitempty"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	FromNick string `json:"fromNick"`
	ToNick   string `json:"toNick"`
	ToOS     string `json:"toOs"`
}

// Statuses
const (
	ReqPending   = "pending"
	ReqApproved  = "approved" // uploaded, waiting for the requester to install
	ReqDeclined  = "declined"
	ReqInstalled = "installed"
	ReqFailed    = "failed"
	ReqCancelled = "cancelled"
)

type Inbox struct {
	Invites  []HubInvite     `json:"invites"`
	Incoming []PluginRequest `json:"incoming"`
	Ready    []PluginRequest `json:"ready"`
	Me       HubUser         `json:"me"`
}

var hubsMu sync.Mutex

// Hub returns the server client of this configuration.
func (c *Config) Hub() *Hub {
	hubsMu.Lock()
	defer hubsMu.Unlock()
	if c.hub == nil {
		c.hub = &Hub{cfg: c, cl: &http.Client{Timeout: 30 * time.Second}, tokens: map[string]hubToken{}}
	}
	return c.hub
}

func (c *Config) ServerURL() string {
	if v := os.Getenv("UZBEK_SERVER"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if c.Server != "" {
		return strings.TrimRight(c.Server, "/")
	}
	return strings.TrimRight(DefaultServer, "/")
}

// Registered: this device has an account on the server.
func (h *Hub) Registered() bool { return h.cfg.DeviceToken != "" && h.cfg.ServerURL() != "" }

type HubError struct {
	Code int
	Msg  string
}

func (e *HubError) Error() string { return e.Msg }

func (h *Hub) do(method, path string, body any, out any) error {
	base := h.cfg.ServerURL()
	if base == "" {
		return errors.New("не указан адрес сервера (⚙ Настройки → Сервер)")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return err
	}
	if h.cfg.DeviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.cl.Do(req)
	if err != nil {
		return fmt.Errorf("сервер недоступен (%s) — проверь интернет", hostOf(base))
	}
	defer resp.Body.Close()
	return readHubResp(resp, out)
}

func readHubResp(resp *http.Response, out any) error {
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = "сервер ответил " + resp.Status
		}
		return &HubError{resp.StatusCode, e.Error}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func hostOf(u string) string {
	if x, err := url.Parse(u); err == nil && x.Host != "" {
		return x.Host
	}
	return u
}

// ----- account -----

func (h *Hub) Ping(server string) error {
	resp, err := h.cl.Get(strings.TrimRight(server, "/") + "/api/ping")
	if err != nil {
		return fmt.Errorf("сервер %s не отвечает", hostOf(server))
	}
	defer resp.Body.Close()
	var r struct {
		Service string `json:"service"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	if r.Service != "prog-uzbeka" {
		return fmt.Errorf("по адресу %s не сервер проги узбека", hostOf(server))
	}
	return nil
}

func (h *Hub) Register(nick string) (*HubUser, error) {
	host, _ := os.Hostname()
	var r struct {
		Token string  `json:"token"`
		User  HubUser `json:"user"`
	}
	if err := h.do("POST", "/api/register", map[string]string{"nick": strings.TrimSpace(nick), "os": OSName(), "device": host}, &r); err != nil {
		return nil, err
	}
	h.cfg.DeviceToken, h.cfg.UserID, h.cfg.UserName = r.Token, r.User.ID, r.User.Nick
	return &r.User, h.cfg.Save()
}

func (h *Hub) Me() (*HubUser, error) {
	var u HubUser
	return &u, h.do("GET", "/api/me", nil, &u)
}

func (h *Hub) UpdateMe(nick, sharing *string) (*HubUser, error) {
	var u HubUser
	if err := h.do("PATCH", "/api/me", map[string]any{"nick": nick, "sharing": sharing}, &u); err != nil {
		return nil, err
	}
	h.cfg.UserName, h.cfg.Sharing = u.Nick, u.Sharing
	h.cfg.Save()
	return &u, nil
}

func (h *Hub) Users() ([]HubUser, error) {
	var u []HubUser
	return u, h.do("GET", "/api/users", nil, &u)
}

func (h *Hub) UserByNick(nick string) *HubUser {
	us, _ := h.Users()
	for _, u := range us {
		if strings.EqualFold(u.Nick, strings.TrimSpace(nick)) {
			return &u
		}
	}
	return nil
}

func (h *Hub) PutInventory(items []InstalledPlugin) error {
	type it struct {
		Name    string `json:"name"`
		Format  string `json:"format"`
		Version string `json:"version,omitempty"`
		Vendor  string `json:"vendor,omitempty"`
	}
	out := []it{}
	for _, p := range items {
		out = append(out, it{p.Name, p.Format, p.Version, p.Vendor})
	}
	b, _ := json.Marshal(out)
	sig := fmt.Sprintf("%x", len(b)) + string(b[:min(len(b), 64)]) + fmt.Sprint(hashBytes(b))
	h.mu.Lock()
	same := sig == h.invSig && time.Since(h.invAt) < 6*time.Hour
	h.mu.Unlock()
	if same {
		return nil
	}
	if err := h.do("PUT", "/api/inventory", json.RawMessage(b), nil); err != nil {
		return err
	}
	h.mu.Lock()
	h.invSig, h.invAt = sig, time.Now()
	h.mu.Unlock()
	return nil
}

func hashBytes(b []byte) uint64 {
	var x uint64 = 1469598103934665603
	for _, c := range b {
		x = (x ^ uint64(c)) * 1099511628211
	}
	return x
}

type Inventory struct {
	User    HubUser           `json:"user"`
	Updated int64             `json:"updated"`
	Plugins []InstalledPlugin `json:"plugins"`
}

func (h *Hub) Inventory(userID string) (*Inventory, error) {
	var inv Inventory
	return &inv, h.do("GET", "/api/users/"+url.PathEscape(userID)+"/inventory", nil, &inv)
}

// ----- projects -----

func (h *Hub) Projects(maxAge time.Duration) ([]HubProject, error) {
	h.mu.Lock()
	if h.projects != nil && time.Since(h.projAt) < maxAge {
		p := h.projects
		h.mu.Unlock()
		return p, nil
	}
	h.mu.Unlock()
	var ps []HubProject
	if err := h.do("GET", "/api/projects", nil, &ps); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.projects, h.projAt = ps, time.Now()
	h.mu.Unlock()
	return ps, nil
}

func (h *Hub) forget() {
	h.mu.Lock()
	h.projAt = time.Time{}
	h.mu.Unlock()
}

// normRemote makes remote URLs comparable.
func normRemote(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "@"); i >= 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:]
	}
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	return strings.ReplaceAll(u, `\`, "/")
}

// ProjectForRemote finds the server project a git remote belongs to.
func (h *Hub) ProjectForRemote(remote string) *HubProject {
	if remote == "" || !h.Registered() {
		return nil
	}
	n := normRemote(remote)
	for _, age := range []time.Duration{time.Minute, 5 * time.Second} {
		ps, err := h.Projects(age)
		if err != nil {
			return nil
		}
		for i := range ps {
			if normRemote(ps[i].CloneURL) == n {
				return &ps[i]
			}
		}
	}
	return nil
}

func (h *Hub) Project(id string) (*HubProject, error) {
	for _, age := range []time.Duration{time.Minute, 0} {
		ps, err := h.Projects(age)
		if err != nil {
			return nil, err
		}
		for i := range ps {
			if ps[i].ID == id {
				return &ps[i], nil
			}
		}
	}
	return nil, errors.New("проект не найден на сервере")
}

func (h *Hub) CreateProject(name string) (*HubProject, error) {
	var p HubProject
	err := h.do("POST", "/api/projects", map[string]string{"name": name}, &p)
	h.forget()
	return &p, err
}

func (h *Hub) Invite(projectID, nick string) (*HubInvite, error) {
	var i HubInvite
	err := h.do("POST", "/api/projects/"+url.PathEscape(projectID)+"/invite", map[string]string{"nick": nick}, &i)
	h.forget()
	return &i, err
}

func (h *Hub) RemoveMember(projectID, userID string) error {
	err := h.do("DELETE", "/api/projects/"+url.PathEscape(projectID)+"/members/"+url.PathEscape(userID), nil, nil)
	h.forget()
	return err
}

func (h *Hub) AcceptInvite(id string) (*HubProject, error) {
	var p HubProject
	err := h.do("POST", "/api/invites/"+url.PathEscape(id)+"/accept", nil, &p)
	h.forget()
	return &p, err
}

func (h *Hub) DeclineInvite(id string) error {
	err := h.do("POST", "/api/invites/"+url.PathEscape(id)+"/decline", nil, nil)
	h.forget()
	return err
}

// RepoToken: short-lived GitHub access to one project (cached).
func (h *Hub) RepoToken(projectID string) (string, error) {
	h.mu.Lock()
	if t, ok := h.tokens[projectID]; ok && time.Until(t.exp) > 10*time.Minute {
		h.mu.Unlock()
		return t.tok, nil
	}
	h.mu.Unlock()
	var r struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := h.do("GET", "/api/projects/"+url.PathEscape(projectID)+"/token", nil, &r); err != nil {
		return "", err
	}
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = time.Now().Add(time.Hour)
	}
	h.mu.Lock()
	h.tokens[projectID] = hubToken{r.Token, r.ExpiresAt}
	h.mu.Unlock()
	return r.Token, nil
}

// ----- inbox & plugin transfers -----

func (h *Hub) Inbox() (*Inbox, error) {
	var in Inbox
	if err := h.do("GET", "/api/inbox", nil, &in); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.inbox = &in
	h.mu.Unlock()
	if in.Me.Nick != "" && (in.Me.Nick != h.cfg.UserName || in.Me.Sharing != h.cfg.Sharing) {
		h.cfg.UserName, h.cfg.Sharing = in.Me.Nick, in.Me.Sharing
		h.cfg.Save()
	}
	return &in, nil
}

// LastInbox is the most recent inbox (nil before the first successful poll).
func (h *Hub) LastInbox() *Inbox {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inbox
}

type ReqItem struct {
	Plugin string `json:"plugin"`
	Format string `json:"format"`
	Vendor string `json:"vendor,omitempty"`
}

func (h *Hub) RequestPlugins(toNick string, items []ReqItem) ([]PluginRequest, error) {
	var out []PluginRequest
	err := h.do("POST", "/api/plugin-requests", map[string]any{"to": toNick, "items": items}, &out)
	return out, err
}

func (h *Hub) Requests() ([]PluginRequest, error) {
	var out []PluginRequest
	return out, h.do("GET", "/api/plugin-requests", nil, &out)
}

func (h *Hub) Request(id string) (*PluginRequest, error) {
	var r PluginRequest
	return &r, h.do("GET", "/api/plugin-requests/"+url.PathEscape(id), nil, &r)
}

func (h *Hub) DeclineRequest(id, reason string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/decline", map[string]string{"reason": reason}, nil)
}

func (h *Hub) CancelRequest(id string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/cancel", nil, nil)
}

func (h *Hub) DoneRequest(id string, ok bool, note string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/done", map[string]any{"ok": ok, "note": note}, nil)
}

// UploadFile sends the plugin (zip) or an installer for a request.
func (h *Hub) UploadFile(job *Job, id, path, kind, name, note, format string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	q := url.Values{"kind": {kind}, "name": {name}, "note": {note}, "format": {format}}
	req, err := http.NewRequest("PUT", h.cfg.ServerURL()+"/api/plugin-requests/"+url.PathEscape(id)+"/file?"+q.Encode(),
		&progressReader{r: f, total: st.Size(), job: job, verb: "Отправка на сервер"})
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("не удалось отправить файл на сервер: %v", err)
	}
	defer resp.Body.Close()
	return readHubResp(resp, nil)
}

func (h *Hub) DownloadFile(job *Job, r *PluginRequest, dst string) error {
	req, _ := http.NewRequest("GET", h.cfg.ServerURL()+"/api/plugin-requests/"+url.PathEscape(r.ID)+"/file", nil)
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("сервер недоступен: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return readHubResp(resp, nil)
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, &progressReader{r: resp.Body, total: r.Size, job: job, verb: "Скачивание"})
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ---------- git access ----------

var remoteCache sync.Map // dir -> remoteEntry

type remoteEntry struct {
	url string
	at  time.Time
}

func forgetRemote(dir string) { remoteCache.Delete(filepath.Clean(dir)) }

// repoToken: what to give git as the password for this remote.
func repoToken(cfg *Config, remote string) string {
	if cfg == nil || remote == "" {
		return ""
	}
	if h := cfg.Hub(); h.Registered() {
		if p := h.ProjectForRemote(remote); p != nil {
			if t, err := h.RepoToken(p.ID); err == nil {
				return t
			}
		}
	}
	return cfg.Token // a personal GitHub token from the old version, if any
}
