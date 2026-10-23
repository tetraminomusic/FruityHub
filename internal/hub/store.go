// Package hub is the FruityHub server: users (one device = one user),
// projects and their members, invitations, plugin transfers. FL projects
// themselves stay on GitHub (one repo per project, on the owner's account);
// the server only knows where they are. Plugins are streamed from one
// computer to the other through the server without being stored.
package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type User struct {
	ID         string    `json:"id"`
	Nick       string    `json:"nick"`
	GitHub     string    `json:"github"` // GitHub login (checked at registration)
	OS         string    `json:"os"`
	Device     string    `json:"device"`
	TokenHash  string    `json:"tokenHash"`
	Created    int64     `json:"created"`
	LastSeen   int64     `json:"lastSeen"`
	Sharing    string    `json:"sharing"` // off | ask | members | all
	Inventory  []InvItem `json:"inventory,omitempty"`
	InvUpdated int64     `json:"invUpdated,omitempty"`
}

type InvItem struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Version string `json:"version,omitempty"`
	Vendor  string `json:"vendor,omitempty"`
}

type Project struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Repo     string   `json:"repo"` // owner/name on GitHub
	CloneURL string   `json:"cloneUrl"`
	Owner    string   `json:"owner"`
	Members  []string `json:"members"`
	Created  int64    `json:"created"`
	Lock     *Lock    `json:"lock,omitempty"` // кто сейчас работает над проектом («Взять в работу»)
}

// Lock — проект закреплён за участником. Хранится на сервере: взять и освободить — мгновенно, без git.
type Lock struct {
	User  string `json:"user"` // id
	OS    string `json:"os,omitempty"`
	Since int64  `json:"since"`
	Note  string `json:"note,omitempty"`
}

type Invite struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	From    string `json:"from"`
	To      string `json:"to"`
	Created int64  `json:"created"`
}

type PluginReq struct {
	ID       string `json:"id"`
	From     string `json:"from"` // who asks
	To       string `json:"to"`   // who has the plugin
	FromOS   string `json:"fromOs"`
	Plugin   string `json:"plugin"`
	Format   string `json:"format"`
	Vendor   string `json:"vendor,omitempty"`
	Status   string `json:"status"` // pending approved streaming declined installed failed cancelled
	Kind     string `json:"kind,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Note     string `json:"note,omitempty"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	// Reinstall — переустановить начисто: зависимости передаются все, файлы данных у получателя заменяются
	Reinstall bool `json:"reinstall,omitempty"`
	// Partial — плагин встал, но что-то из его данных/путей не легло (предупреждения у получателя)
	Partial bool `json:"partial,omitempty"`
}

type DB struct {
	Users    map[string]*User      `json:"users"`
	Projects map[string]*Project   `json:"projects"`
	Invites  map[string]*Invite    `json:"invites"`
	Requests map[string]*PluginReq `json:"requests"`
	Settings Settings              `json:"settings"`
}

// Settings — настройки сервера, которые меняются из админки.
type Settings struct {
	MoveTo string `json:"moveTo,omitempty"` // новый адрес сервера после переезда
}

type Store struct {
	mu   sync.Mutex
	path string
	db   DB
}

func OpenStore(dir string) (*Store, error) {
	os.MkdirAll(dir, 0o700)
	s := &Store{path: filepath.Join(dir, "db.json")}
	s.db = DB{Users: map[string]*User{}, Projects: map[string]*Project{}, Invites: map[string]*Invite{}, Requests: map[string]*PluginReq{}}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.db); err != nil {
			return nil, err
		}
	}
	if s.db.Users == nil {
		s.db.Users = map[string]*User{}
	}
	if s.db.Projects == nil {
		s.db.Projects = map[string]*Project{}
	}
	if s.db.Invites == nil {
		s.db.Invites = map[string]*Invite{}
	}
	if s.db.Requests == nil {
		s.db.Requests = map[string]*PluginReq{}
	}
	return s, nil
}

// save must be called with mu held.
func (s *Store) save() error {
	b, _ := json.MarshalIndent(s.db, "", " ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func now() int64 { return time.Now().Unix() }

func (s *Store) userByToken(tok string) *User {
	h := hashToken(tok)
	for _, u := range s.db.Users {
		if u.TokenHash == h {
			return u
		}
	}
	return nil
}

func (s *Store) userByNick(nick string) *User {
	n := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(nick, "@")))
	for _, u := range s.db.Users {
		if strings.ToLower(u.Nick) == n {
			return u
		}
	}
	return nil
}

func isMember(p *Project, uid string) bool {
	for _, m := range p.Members {
		if m == uid {
			return true
		}
	}
	return false
}

func (s *Store) projectByRepo(repo string) *Project {
	for _, p := range s.db.Projects {
		if strings.EqualFold(p.Repo, repo) {
			return p
		}
	}
	return nil
}

func (s *Store) usersByGitHub(login string) []*User {
	var out []*User
	for _, u := range s.db.Users {
		if login != "" && strings.EqualFold(u.GitHub, login) {
			out = append(out, u)
		}
	}
	return out
}

func (s *Store) sharedProject(a, b string) bool {
	for _, p := range s.db.Projects {
		if isMember(p, a) && isMember(p, b) {
			return true
		}
	}
	return false
}

func sortedUsers(m map[string]*User) []*User {
	var out []*User
	for _, u := range m {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Nick) < strings.ToLower(out[j].Nick) })
	return out
}
