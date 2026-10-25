package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultServer is the address of "FruityHub" server baked in at build time.
var DefaultServer = ""

// DefaultServerCode is the registration code baked in at build time.
var DefaultServerCode = ""

// Hub is the client of our own server: users (one device = one user),
// projects and members, invitations, plugin transfers. FL projects
// themselves live on the owners' GitHub (one repo per project).
type Hub struct {
	cfg *Config
	cl  *http.Client

	mu       sync.Mutex
	projects []HubProject
	projAt   time.Time
	inbox    *Inbox
	invSig   string
	invAt    time.Time
	discAt   time.Time
	locks    map[string]*HubLock // id проекта → замок (nil — свободен); nil-карта — сервер замков не знает
}

// HubLock — проект «в работе» у участника (замок на сервере).
type HubLock struct {
	UserID string `json:"userId"`
	User   string `json:"user"`
	OS     string `json:"os,omitempty"`
	Since  int64  `json:"since"`
	Note   string `json:"note,omitempty"`
}

type HubUser struct {
	ID       string   `json:"id"`
	Nick     string   `json:"nick"`
	GitHub   string   `json:"github"`
	OS       string   `json:"os"`
	LastSeen int64    `json:"lastSeen"`
	Online   bool     `json:"online"`
	Sharing  string   `json:"sharing"`
	OldNicks []string `json:"oldNicks,omitempty"`
}

type HubInvite struct {
	ID       string  `json:"id"`
	Project  string  `json:"project"`
	Name     string  `json:"name"`
	Repo     string  `json:"repo"`
	CloneURL string  `json:"cloneUrl"`
	From     HubUser `json:"from"`
	To       HubUser `json:"to"`
	Created  int64   `json:"created"`
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
	Lock     *HubLock    `json:"lock"`
	// Downloading — этот проект сейчас скачивается на этот компьютер (заполняет приложение)
	Downloading bool `json:"downloading,omitempty"`
}

// PluginRequest is a plugin transfer between two users (through the server).
type PluginRequest struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	FromOS     string `json:"fromOs"`
	Plugin     string `json:"plugin"`
	Format     string `json:"format"`
	Vendor     string `json:"vendor,omitempty"`
	Status     string `json:"status"`
	Kind       string `json:"kind,omitempty"`
	FileName   string `json:"fileName,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Note       string `json:"note,omitempty"`
	Created    int64  `json:"created"`
	Updated    int64  `json:"updated"`
	FromNick   string `json:"fromNick"`
	FromOnline bool   `json:"fromOnline"`
	ToNick     string `json:"toNick"`
	ToOS       string `json:"toOs"`
	ToOnline   bool   `json:"toOnline"`
	OfferSeq   int    `json:"offerSeq,omitempty"`
	Reinstall  bool   `json:"reinstall,omitempty"`
	Partial    bool   `json:"partial,omitempty"`
	Project    string `json:"project,omitempty"`
	FromCaps   string `json:"fromCaps,omitempty"` // что умеет приложение того, кто просит: zstd, resume
}

// Statuses
const (
	ReqPending   = "pending"
	ReqApproved  = "approved"  // allowed: the owner's app sends it when both are online
	ReqStreaming = "streaming" // the owner's app is connected and sending
	ReqDeclined  = "declined"
	ReqInstalled = "installed"
	ReqFailed    = "failed"
	ReqCancelled = "cancelled"
)

type Inbox struct {
	Invites  []HubInvite         `json:"invites"`
	Incoming []PluginRequest     `json:"incoming"` // asked from me, waiting for my answer
	ToSend   []PluginRequest     `json:"toSend"`   // I allowed: my app sends them
	Ready    []PluginRequest     `json:"ready"`    // my requests that were allowed: my app receives them
	Me       HubUser             `json:"me"`
	MoveTo   string              `json:"moveTo,omitempty"` // сервер переехал
	Update   string              `json:"update,omitempty"` // последняя версия приложения на сервере
	Locks    map[string]*HubLock `json:"locks"`            // замки проектов (сервер 0.7.4+)
	Fetches  []FetchView         `json:"fetches"`          // участники просят аудио проектов (сервер 0.9.1+)
}

var hubsMu sync.Mutex

// Hub returns the server client of this configuration.
func (c *Config) Hub() *Hub {
	hubsMu.Lock()
	defer hubsMu.Unlock()
	if c.hub == nil {
		c.hub = &Hub{cfg: c, cl: &http.Client{Timeout: 30 * time.Second}}
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
		return errors.New("не указан адрес сервера (Настройки → Сервер)")
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
		return fmt.Errorf("по адресу %s не сервер FruityHub", hostOf(server))
	}
	return nil
}

func (h *Hub) Register(nick, code, githubToken string) (*HubUser, error) {
	host, _ := os.Hostname()
	var r struct {
		Token string  `json:"token"`
		User  HubUser `json:"user"`
	}
	if err := h.do("POST", "/api/register", map[string]string{"nick": strings.TrimSpace(nick), "os": OSName(), "device": host,
		"code": strings.TrimSpace(code), "githubToken": strings.TrimSpace(githubToken)}, &r); err != nil {
		return nil, err
	}
	h.cfg.DeviceToken, h.cfg.UserID, h.cfg.UserName, h.cfg.GitHubLogin = r.Token, r.User.ID, r.User.Nick, r.User.GitHub
	h.cfg.Token = strings.TrimSpace(githubToken)
	return &r.User, h.cfg.Save()
}

func (h *Hub) Me() (*HubUser, error) {
	var u HubUser
	return &u, h.do("GET", "/api/me", nil, &u)
}

func (h *Hub) UpdateMe(nick, sharing, githubToken *string) (*HubUser, error) {
	var u HubUser
	if err := h.do("PATCH", "/api/me", map[string]any{"nick": nick, "sharing": sharing, "githubToken": githubToken}, &u); err != nil {
		return nil, err
	}
	if githubToken != nil {
		h.cfg.Token = strings.TrimSpace(*githubToken)
	}
	h.cfg.UserName, h.cfg.Sharing, h.cfg.GitHubLogin = u.Nick, u.Sharing, u.GitHub
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

// Discover просит сервер добавить меня во все проекты, к репозиториям которых у моего GitHub уже есть доступ
// (тот же GitHub-аккаунт на другом устройстве или коллаборатор). Не чаще раза в 30 секунд, если не force.
func (h *Hub) Discover(force bool) ([]HubProject, error) {
	if h.cfg.Token == "" || !h.Registered() {
		return nil, nil
	}
	h.mu.Lock()
	if !force && time.Since(h.discAt) < 30*time.Second {
		h.mu.Unlock()
		return nil, nil
	}
	h.discAt = time.Now()
	h.mu.Unlock()
	var added []HubProject
	err := h.do("POST", "/api/projects/discover", map[string]string{"githubToken": h.cfg.Token}, &added)
	if len(added) > 0 {
		h.forget()
	}
	return added, err
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
	o, r, isGH := repoSlug(remote)
	for _, age := range []time.Duration{time.Minute, 5 * time.Second} {
		ps, err := h.Projects(age)
		if err != nil {
			return nil
		}
		for i := range ps {
			if normRemote(ps[i].CloneURL) == n || (isGH && strings.EqualFold(ps[i].Repo, o+"/"+r)) {
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

// AddProject registers a GitHub repo as a project on the server.
func (h *Hub) AddProject(name, repo, cloneURL string) (*HubProject, error) {
	var p HubProject
	err := h.do("POST", "/api/projects", map[string]string{"name": name, "repo": repo, "cloneUrl": cloneURL}, &p)
	h.forget()
	return &p, err
}

// SyncMembers: people who already have access on GitHub become members.
func (h *Hub) SyncMembers(projectID string, logins []string) (*HubProject, error) {
	var p HubProject
	err := h.do("POST", "/api/projects/"+url.PathEscape(projectID)+"/sync-members", map[string]any{"logins": logins}, &p)
	h.forget()
	return &p, err
}

func (h *Hub) Invite(projectID, nick string) (*HubInvite, error) {
	var i HubInvite
	err := h.do("POST", "/api/projects/"+url.PathEscape(projectID)+"/invite", map[string]string{"nick": nick}, &i)
	h.forget()
	return &i, err
}

// RemoveMember returns the GitHub login of the removed person.
func (h *Hub) RemoveMember(projectID, userID string) (string, error) {
	var r struct {
		GitHub string `json:"github"`
	}
	err := h.do("DELETE", "/api/projects/"+url.PathEscape(projectID)+"/members/"+url.PathEscape(userID), nil, &r)
	h.forget()
	return r.GitHub, err
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

// ----- inbox & plugin transfers -----

func (h *Hub) Inbox() (*Inbox, error) {
	var in Inbox
	if err := h.do("GET", "/api/inbox?caps="+appCaps, nil, &in); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.inbox = &in
	if in.Locks != nil {
		h.locks = in.Locks
	}
	h.mu.Unlock()
	if in.Me.Nick != "" && (in.Me.Nick != h.cfg.UserName || in.Me.Sharing != h.cfg.Sharing) {
		h.cfg.UserName, h.cfg.Sharing = in.Me.Nick, in.Me.Sharing
		h.cfg.Save()
	}
	return &in, nil
}

// FollowMove: сервер сообщил, что переехал. Переключаемся, только если новый сервер отвечает
// и узнаёт это устройство (туда восстановлен бэкап).
func (h *Hub) FollowMove(to string) error {
	to = strings.TrimRight(strings.TrimSpace(to), "/")
	if to == "" || to == h.cfg.ServerURL() {
		return nil
	}
	if err := h.Ping(to); err != nil {
		return err
	}
	req, _ := http.NewRequest("GET", to+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	resp, err := h.cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := readHubResp(resp, nil); err != nil {
		return fmt.Errorf("новый сервер не узнаёт это устройство: %w", err)
	}
	h.cfg.Server = to
	h.forget()
	return h.cfg.Save()
}

// LastInbox is the most recent inbox (nil before the first successful poll).
func (h *Hub) LastInbox() *Inbox {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inbox
}

type ReqItem struct {
	Plugin    string `json:"plugin"`
	Format    string `json:"format"`
	Vendor    string `json:"vendor,omitempty"`
	Reinstall bool   `json:"reinstall,omitempty"`
	Project   string `json:"project,omitempty"`
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

var errCancelled = errors.New("передача отменена")

// errStopped — задачу (скачивание проекта) отменили кнопкой «Отменить».
var errStopped = errors.New("отменено")

// ----- замки проектов -----

var errNoHubLocks = errors.New("сервер не умеет замки проектов")

// LocksKnown: сервер присылает замки (иначе — старый сервер, замок по-старому через git).
func (h *Hub) LocksKnown() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.locks != nil
}

// ProjectLock — замок проекта из последнего опроса сервера.
func (h *Hub) ProjectLock(id string) (*HubLock, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locks == nil {
		return nil, false
	}
	return h.locks[id], true
}

func (h *Hub) setLock(id string, l *HubLock) {
	h.mu.Lock()
	if h.locks == nil {
		h.locks = map[string]*HubLock{}
	}
	h.locks[id] = l
	h.mu.Unlock()
}

func (h *Hub) TakeLock(id string, force bool, note string) (*HubLock, error) {
	var l HubLock
	err := h.do("POST", "/api/projects/"+url.PathEscape(id)+"/lock", map[string]any{"force": force, "note": note, "os": OSName()}, &l)
	if he, ok := err.(*HubError); ok && (he.Code == 404 || he.Code == 405) && !strings.Contains(he.Msg, "проект") {
		return nil, errNoHubLocks
	}
	if err != nil {
		return nil, err
	}
	h.setLock(id, &l)
	return &l, nil
}

func (h *Hub) ReleaseLock(id string, force bool) error {
	q := ""
	if force {
		q = "?force=1"
	}
	err := h.do("DELETE", "/api/projects/"+url.PathEscape(id)+"/lock"+q, nil, nil)
	if he, ok := err.(*HubError); ok && (he.Code == 404 || he.Code == 405) && !strings.Contains(he.Msg, "проект") {
		return errNoHubLocks
	}
	if err == nil {
		h.setLock(id, nil)
	}
	return err
}

// ----- какие зависимости плагина уже есть у получателя -----

var errNoNego = errors.New("сервер не умеет сверять зависимости")

func (h *Hub) PutOffer(id string, offer any) (int, error) {
	var out struct {
		Seq int `json:"seq"`
	}
	err := h.do("PUT", "/api/plugin-requests/"+url.PathEscape(id)+"/offer", offer, &out)
	if he, ok := err.(*HubError); ok && (he.Code == 404 || he.Code == 405) {
		return 0, errNoNego
	}
	return out.Seq, err
}

func (h *Hub) GetOffer(id string) (int, json.RawMessage, error) {
	var out struct {
		Seq   int             `json:"seq"`
		Offer json.RawMessage `json:"offer"`
	}
	err := h.do("GET", "/api/plugin-requests/"+url.PathEscape(id)+"/offer", nil, &out)
	return out.Seq, out.Offer, err
}

func (h *Hub) PutHave(id string, seq int, have any) error {
	return h.do("PUT", "/api/plugin-requests/"+url.PathEscape(id)+"/have", map[string]any{"seq": seq, "have": have}, nil)
}

// GetHave ждёт ответа получателя (сервер держит запрос до 20 секунд). ok=false — ещё не ответил.
func (h *Hub) GetHave(id string, out any) (seq int, ok bool, err error) {
	var r struct {
		Seq  int             `json:"seq"`
		Have json.RawMessage `json:"have"`
	}
	err = h.do("GET", "/api/plugin-requests/"+url.PathEscape(id)+"/have", nil, &r)
	if he, isHE := err.(*HubError); isHE && he.Code == 408 {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return r.Seq, true, json.Unmarshal(r.Have, out)
}

func (h *Hub) RetryRequest(id string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/retry", nil, nil)
}

// RetryReinstall — передать ещё раз начисто (все зависимости, с заменой файлов) или обычным повтором.
func (h *Hub) RetryReinstall(id string, reinstall bool) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/retry", map[string]bool{"reinstall": reinstall}, nil)
}

func (h *Hub) CancelRequest(id string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/cancel", nil, nil)
}

func (h *Hub) DoneRequest(id string, ok bool, note string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/done", map[string]any{"ok": ok, "note": note}, nil)
}

// DonePartial — плагин встал, но не всё из его данных легло.
func (h *Hub) DonePartial(id, note string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/done", map[string]any{"ok": true, "partial": true, "note": note}, nil)
}

// Approve: yes, and this is what will come (kind "plugin" or "installer").
func (h *Hub) Approve(id, kind, name, note, format string) error {
	return h.do("POST", "/api/plugin-requests/"+url.PathEscape(id)+"/approve",
		map[string]any{"kind": kind, "name": name, "note": note, "format": format}, nil)
}

var errNotOnline = errors.New("получатель не в сети")

// StreamSend pushes the file through the server straight to the requester.
// The server keeps nothing: it waits for the other side and pipes the bytes.
var errStalled = errors.New("связь оборвалась: полторы минуты не шли данные (сеть, сон ноутбука?) — попробую ещё раз")

const stallAfter = 90 * time.Second

func (h *Hub) relaySend(job *Job, id, path string) error {
	return h.relaySendCtx(job.Ctx(), job, id, path, nil)
}

// relaySendCtx: first — вызывается перед тем, как пойдут данные (получатель подключился к серверу);
// false — данные уже идут по локальной сети, через сервер не отправляем.
func (h *Hub) relaySendCtx(parent context.Context, job *Job, id, path string, first func() bool) error {
	fileID := packedID(path) // архив помечен — получатель может докачать с места обрыва
	var off int64
	for try := 0; ; try++ {
		next, err := h.relaySendFrom(parent, job, id, path, fileID, off, first)
		if next < 0 || try >= 2 {
			return err
		}
		off = next // сервер: у получателя уже есть начало — шлём остаток
		if off > 0 {
			job.Logf("Докачиваю с места обрыва: у %s уже есть %s", "получателя", fmtBytes(off))
		}
	}
}

// relaySendFrom отдаёт файл с байта off. next >= 0 — сервер просит начать с другого места (докачка).
func (h *Hub) relaySendFrom(parent context.Context, job *Job, id, path, fileID string, off int64, first func() bool) (next int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	st, _ := f.Stat()
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return -1, err
		}
	}
	ctx, touch, stop, stalled := stallGuard(parent, stallAfter)
	defer stop()
	var body io.Reader = &progressReader{r: f, total: st.Size(), done: off, job: job, verb: "Передаю", touch: touch}
	if first != nil {
		body = &firstReader{r: body, first: first}
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", h.cfg.ServerURL()+"/api/plugin-requests/"+url.PathEscape(id)+"/stream", body)
	if err != nil {
		return -1, err
	}
	req.ContentLength = st.Size() - off
	req.Header.Set("X-Size", fmt.Sprint(st.Size()))
	if fileID != "" {
		req.Header.Set("X-File-Id", fileID)
		req.Header.Set("X-Offset", fmt.Sprint(off))
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Expect", "100-continue") // данные пойдут, только когда получатель подключится
	job.SetProgress("Жду, пока получатель подключится к передаче…", -1)
	resp, err := streamClient().Do(req)
	if err != nil {
		if stalled() {
			return -1, errStalled
		}
		if parent.Err() != nil {
			return -1, errCancelled
		}
		return -1, fmt.Errorf("передача прервалась: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 408 {
		return -1, errNotOnline
	}
	if resp.StatusCode == 410 {
		return -1, errCancelled
	}
	if resp.StatusCode == 416 && fileID != "" {
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(resp.Body)
		json.Unmarshal(b, &e)
		if n, perr := strconv.ParseInt(strings.TrimSpace(e.Error), 10, 64); perr == nil && n >= 0 && n < st.Size() {
			return n, fmt.Errorf("сервер просит начать с %d", n)
		}
	}
	return -1, readHubResp(resp, nil)
}

// relayRecv takes the file while the owner's app is sending it (through the server).
func (h *Hub) relayRecv(job *Job, r *PluginRequest, dst string) error {
	ctx, touch, stop, stalled := stallGuard(job.Ctx(), stallAfter)
	defer stop()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.cfg.ServerURL()+"/api/plugin-requests/"+url.PathEscape(r.ID)+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	resumeHeaders(req.Header.Set, dst) // есть начало файла с прошлой попытки — пусть пришлют только остаток
	resp, err := streamClient().Do(req)
	if err != nil {
		if job.Ctx().Err() != nil {
			return errCancelled
		}
		return fmt.Errorf("сервер недоступен: %v", err)
	}
	defer resp.Body.Close()
	touch() // с этого момента ждём данных не дольше stallAfter
	if resp.StatusCode == 408 {
		return errors.New("отправитель сейчас не в сети")
	}
	if resp.StatusCode == 410 {
		return errCancelled
	}
	if resp.StatusCode >= 300 {
		return readHubResp(resp, nil)
	}
	return receiveInto(job, resp.Body, resp.Header.Get("X-File-Id"), resp.Header.Get("X-Offset"), resp.ContentLength, dst, "", "Получаю", touch, stalled)
}

// receiveInto пишет поток в dst.part (с места off, если это докачка того же архива), сверяет размер
// и sha256 (если известен) и переименовывает в dst. Оборвалось — начало файла остаётся для докачки.
func receiveInto(job *Job, body io.Reader, fileID, offHdr string, contentLen int64, dst, sha, verb string, touch func(), stalled func() bool) error {
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".part"
	off, _ := strconv.ParseInt(offHdr, 10, 64)
	haveID, have := partInfo(dst)
	var f *os.File
	var err error
	if off > 0 {
		if fileID == "" || fileID != haveID || off != have {
			dropPart(dst)
			return fmt.Errorf("докачка не сошлась (у меня %d байт, прислали с %d) — начну заново", have, off)
		}
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_APPEND, 0o644)
	} else {
		f, err = os.Create(tmp)
	}
	if err != nil {
		return err
	}
	savePartID(dst, fileID)
	total := int64(-1)
	if contentLen >= 0 {
		total = off + contentLen
	}
	n, err := io.CopyBuffer(f, &progressReader{r: body, total: total, done: off, job: job, verb: verb, touch: touch}, make([]byte, 1<<20))
	if err != nil && stalled != nil && stalled() {
		err = errStalled
	}
	f.Close()
	if err == nil && contentLen > 0 && n != contentLen {
		err = fmt.Errorf("получено %d из %d байт", off+n, off+contentLen)
	}
	if err == nil && sha != "" {
		if got, e := fileSha256(tmp); e != nil || got != sha {
			dropPart(dst)
			return errors.New("файл пришёл повреждённым")
		}
	}
	if err != nil {
		if fileID == "" {
			dropPart(dst) // без id докачка невозможна
		}
		if job.Ctx().Err() != nil {
			dropPart(dst)
			return errCancelled
		}
		if off+n > 0 && fileID != "" {
			job.Logf("Связь оборвалась на %s — при повторе докачаю остаток", fmtBytes(off+n))
		}
		return fmt.Errorf("передача прервалась: %v", err)
	}
	os.Remove(dst + ".part.id")
	os.Remove(dst)
	return os.Rename(tmp, dst)
}

// ---------- git access ----------

var remoteCache sync.Map // dir -> remoteEntry

type remoteEntry struct {
	url string
	at  time.Time
}

func forgetRemote(dir string) { remoteCache.Delete(filepath.Clean(dir)) }

// repoToken: what to give git as the password (the user's GitHub token).
func repoToken(cfg *Config, remote string) string {
	if cfg == nil {
		return ""
	}
	return cfg.Token
}
