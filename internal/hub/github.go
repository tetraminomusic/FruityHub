package hub

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GitHubApp talks to GitHub as a GitHub App installed on an organization:
// it creates project repos there and mints short-lived per-repo tokens for
// the members, so users never need GitHub accounts or tokens.
type GitHubApp struct {
	API            string `json:"-"`
	AppID          int64  `json:"appId"`
	Slug           string `json:"slug"`
	Org            string `json:"org"`
	InstallationID int64  `json:"installationId"`
	PEM            string `json:"pem"`

	key  *rsa.PrivateKey
	mu   sync.Mutex
	full struct {
		token string
		exp   time.Time
	}
	path string
}

func LoadGitHubApp(dir, api string) *GitHubApp {
	a := &GitHubApp{API: api, path: filepath.Join(dir, "github-app.json")}
	if b, err := os.ReadFile(a.path); err == nil {
		json.Unmarshal(b, a)
		a.parseKey()
	}
	return a
}

func (a *GitHubApp) Save() error {
	b, _ := json.MarshalIndent(a, "", " ")
	return os.WriteFile(a.path, b, 0o600)
}

func (a *GitHubApp) parseKey() error {
	blk, _ := pem.Decode([]byte(a.PEM))
	if blk == nil {
		return errors.New("bad PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		a.key = k
		return nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return errors.New("not an RSA key")
	}
	a.key = rk
	return nil
}

func (a *GitHubApp) Ready() bool { return a.key != nil && a.AppID != 0 && a.Org != "" }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// jwt signs the app's identity (RS256), valid ~9 minutes.
func (a *GitHubApp) jwt() (string, error) {
	if a.key == nil {
		return "", errors.New("GitHub App не настроен")
	}
	t := time.Now()
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	p, _ := json.Marshal(map[string]any{"iat": t.Add(-60 * time.Second).Unix(), "exp": t.Add(9 * time.Minute).Unix(), "iss": fmt.Sprint(a.AppID)})
	unsigned := b64(h) + "." + b64(p)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + b64(sig), nil
}

func (a *GitHubApp) call(auth, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	url := path
	if !strings.HasPrefix(path, "http") {
		url = a.API + path
	}
	req, _ := http.NewRequest(method, url, rd)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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

// FindInstallation looks up (and remembers) the installation on the configured org.
func (a *GitHubApp) FindInstallation() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.findInstallation()
}

// findInstallation: call with mu held.
func (a *GitHubApp) findInstallation() error {
	j, err := a.jwt()
	if err != nil {
		return err
	}
	var list []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := a.call(j, "GET", "/app/installations", nil, &list); err != nil {
		return err
	}
	for _, i := range list {
		if strings.EqualFold(i.Account.Login, a.Org) {
			a.InstallationID = i.ID
			a.Save()
			return nil
		}
	}
	return fmt.Errorf("приложение не установлено в организацию %s", a.Org)
}

type Token struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expiresAt"`
}

// RepoToken: a token that can only read/write this one repository (~1 hour).
func (a *GitHubApp) RepoToken(repoName string) (*Token, error) {
	return a.instToken(map[string]any{"repositories": []string{repoName},
		"permissions": map[string]string{"contents": "write", "metadata": "read"}})
}

func (a *GitHubApp) instToken(body map[string]any) (*Token, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.InstallationID == 0 {
		if err := a.findInstallation(); err != nil {
			return nil, err
		}
	}
	j, err := a.jwt()
	if err != nil {
		return nil, err
	}
	var t Token
	err = a.call(j, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", a.InstallationID), body, &t)
	if err != nil && strings.Contains(err.Error(), "404") {
		a.InstallationID = 0 // reinstalled? look it up again next time
	}
	return &t, err
}

func (a *GitHubApp) fullToken() (string, error) {
	a.mu.Lock()
	if a.full.token != "" && time.Until(a.full.exp) > 5*time.Minute {
		t := a.full.token
		a.mu.Unlock()
		return t, nil
	}
	a.mu.Unlock()
	t, err := a.instToken(nil)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.full.token, a.full.exp = t.Token, t.Expires
	a.mu.Unlock()
	return t.Token, nil
}

// CreateRepo makes a private repo in the org. Returns full name and clone URL.
func (a *GitHubApp) CreateRepo(name, description string) (string, string, error) {
	tok, err := a.fullToken()
	if err != nil {
		return "", "", err
	}
	base := name
	for i := 2; ; i++ {
		var r struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		}
		err := a.call(tok, "POST", "/orgs/"+a.Org+"/repos", map[string]any{"name": name, "private": true, "description": description,
			"has_issues": false, "has_wiki": false}, &r)
		if err == nil {
			a.call(tok, "PUT", "/repos/"+r.FullName+"/topics", map[string]any{"names": []string{"programma-uzbeka"}}, nil)
			return r.FullName, r.CloneURL, nil
		}
		if !strings.Contains(err.Error(), "already exists") || i > 50 {
			return "", "", err
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// ConvertManifest finishes the "create GitHub App from manifest" flow.
func (a *GitHubApp) ConvertManifest(code string) error {
	var r struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		PEM  string `json:"pem"`
	}
	if err := a.call("", "POST", "/app-manifests/"+code+"/conversions", nil, &r); err != nil {
		return err
	}
	a.AppID, a.Slug, a.PEM, a.InstallationID = r.ID, r.Slug, r.PEM, 0
	if err := a.parseKey(); err != nil {
		return err
	}
	return a.Save()
}
