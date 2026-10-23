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
	"regexp"
	"strings"
	"sync"
	"time"
)

// Storage keeps big "side" files (track preview, plugin transfers) as assets
// of one draft release in the project's GitHub repo. Unlike Git LFS this does
// not use the LFS quota, and replacing a file really replaces it.

var ghAPIBase = "https://api.github.com"

const storageTag = "flsync-storage"

var ghSlugRe = regexp.MustCompile(`github\.com[:/]+([^/]+)/([^/]+?)(?:\.git)?/?$`)

// repoSlug extracts owner/repo from a GitHub remote URL (overridable in tests).
var repoSlug = func(remote string) (string, string, bool) {
	m := ghSlugRe.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func init() {
	// developer hooks for testing against a fake GitHub API
	if v := os.Getenv("UZBEK_GITHUB_API"); v != "" {
		ghAPIBase = v
	}
	if os.Getenv("UZBEK_DEV_LOCALREPOS") != "" { // repos are local paths .../owner/name.git
		repoSlug = func(remote string) (string, string, bool) {
			parts := strings.Split(strings.TrimSuffix(filepath.ToSlash(remote), ".git"), "/")
			if len(parts) < 2 {
				return "", "", false
			}
			return parts[len(parts)-2], parts[len(parts)-1], true
		}
	}
	if v := os.Getenv("UZBEK_DEV_SLUG"); v != "" {
		o, r, _ := strings.Cut(v, "/")
		repoSlug = func(string) (string, string, bool) { return o, r, true }
	}
}

type Asset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Updated     string `json:"updated_at"`
	Uploader    struct {
		Login string `json:"login"`
	} `json:"uploader"`
}

type Storage struct {
	cfg         *Config
	remote      string
	owner, repo string
	relID       int64
	uploadURL   string
}

var (
	storageMu    sync.Mutex
	storageCache = map[string]*Storage{}
)

func OpenStorage(cfg *Config, remote string) (*Storage, error) {
	owner, repo, ok := repoSlug(remote)
	if !ok {
		return nil, errors.New("превью и передача плагинов пока работают только с репозиторием на GitHub")
	}
	tok := repoToken(cfg, remote)
	if tok == "" {
		return nil, errors.New("нет доступа к проекту на сервере — проект не подключён к FruityHub")
	}
	key := ghAPIBase + "|" + owner + "/" + repo
	storageMu.Lock()
	defer storageMu.Unlock()
	if s, ok := storageCache[key]; ok {
		s.cfg, s.remote = cfg, remote
		return s, nil
	}
	s := &Storage{cfg: cfg, remote: remote, owner: owner, repo: repo}
	var rels []struct {
		ID        int64  `json:"id"`
		TagName   string `json:"tag_name"`
		UploadURL string `json:"upload_url"`
	}
	if err := ghAPI(tok, "GET", fmt.Sprintf("/repos/%s/%s/releases?per_page=100", owner, repo), nil, &rels); err != nil {
		return nil, err
	}
	for _, r := range rels {
		if r.TagName == storageTag {
			s.relID, s.uploadURL = r.ID, r.UploadURL
		}
	}
	if s.relID == 0 {
		var r struct {
			ID        int64  `json:"id"`
			UploadURL string `json:"upload_url"`
		}
		err := ghAPI(tok, "POST", fmt.Sprintf("/repos/%s/%s/releases", owner, repo), map[string]any{
			"tag_name": storageTag, "name": "FruityHub — файлы проекта", "draft": true,
			"body": "Служебный черновик: превью трека и плагины в пути. Не удаляй и не публикуй."}, &r)
		if err != nil {
			return nil, err
		}
		s.relID, s.uploadURL = r.ID, r.UploadURL
	}
	if i := strings.Index(s.uploadURL, "{"); i >= 0 {
		s.uploadURL = s.uploadURL[:i]
	}
	storageCache[key] = s
	return s, nil
}

func (s *Storage) tok() string { return repoToken(s.cfg, s.remote) }

func (s *Storage) Assets() ([]Asset, error) {
	var a []Asset
	err := ghAPI(s.tok(), "GET", fmt.Sprintf("/repos/%s/%s/releases/%d/assets?per_page=100", s.owner, s.repo, s.relID), nil, &a)
	return a, err
}

func (s *Storage) Delete(id int64) error {
	return ghAPI(s.tok(), "DELETE", fmt.Sprintf("/repos/%s/%s/releases/assets/%d", s.owner, s.repo, id), nil, nil)
}

type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	job   *Job
	verb  string
	last  time.Time
	start time.Time
	touch func() // для сторожа зависаний (stallGuard)
}

func (p *progressReader) Read(b []byte) (int, error) {
	if p.start.IsZero() {
		p.start = time.Now()
	}
	n, err := p.r.Read(b)
	if p.touch != nil {
		p.touch()
	}
	p.done += int64(n)
	if time.Since(p.last) > 400*time.Millisecond || err == io.EOF {
		p.last = time.Now()
		speed := ""
		if el := time.Since(p.start).Seconds(); el > 1 {
			speed = fmt.Sprintf(" · %.1f МБ/с", float64(p.done)/1e6/el)
		}
		if p.total > 0 {
			p.job.SetProgress(fmt.Sprintf("%s · %.1f из %.1f МБ%s", p.verb, float64(p.done)/1e6, float64(p.total)/1e6, speed), int(p.done*100/p.total))
		} else {
			p.job.SetProgress(fmt.Sprintf("%s · %.1f МБ%s", p.verb, float64(p.done)/1e6, speed), -1)
		}
	}
	return n, err
}

func contentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".wav":
		return "audio/wav"
	case ".zip":
		return "application/zip"
	}
	return "application/octet-stream"
}

func (s *Storage) Upload(job *Job, name, label, path string) (*Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() > 2000<<20 {
		return nil, errors.New("файл больше 2 ГБ — GitHub такое не примет")
	}
	q := url.Values{"name": {name}}
	if label != "" {
		q.Set("label", label)
	}
	req, _ := http.NewRequest("POST", s.uploadURL+"?"+q.Encode(), &progressReader{r: f, total: st.Size(), job: job, verb: "Загрузка"})
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", contentType(name))
	req.Header.Set("Authorization", "Bearer "+s.tok())
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub upload: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var a Asset
	return &a, json.Unmarshal(b, &a)
}

func (s *Storage) Download(job *Job, a Asset, dst string) error {
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d", ghAPIBase, s.owner, s.repo, a.ID), nil)
	req.Header.Set("Authorization", "Bearer "+s.tok())
	req.Header.Set("Accept", "application/octet-stream")
	// Go drops the Authorization header when the redirect leaves the API host,
	// which is exactly what the signed download URL expects.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("скачивание: %s", resp.Status)
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, &progressReader{r: resp.Body, total: a.Size, job: job, verb: "Скачивание"})
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ---------- track preview: exactly one audio file per project ----------

var previewExts = map[string]bool{".mp3": true, ".flac": true, ".wav": true}

type PreviewInfo struct {
	Asset    *Asset `json:"asset"`
	FileName string `json:"fileName"`
	By       string `json:"by"`
	Cached   bool   `json:"cached"`
}

func (p *Project) storage() (*Storage, error) {
	return OpenStorage(p.Cfg, p.git().Remote())
}

func (s *Storage) cacheDir() string {
	return filepath.Join(ConfigDir(), "cache", s.owner+"_"+s.repo)
}

func previewLabel(a Asset) (file, by string) {
	file, by = a.Label, a.Uploader.Login
	if l, r, ok := strings.Cut(a.Label, " · "); ok {
		by, file = l, r
	}
	if file == "" {
		file = a.Name
	}
	return
}

func (p *Project) Preview() (*PreviewInfo, error) {
	s, err := p.storage()
	if err != nil {
		return nil, err
	}
	as, err := s.Assets()
	if err != nil {
		return nil, err
	}
	info := &PreviewInfo{}
	for i := range as {
		a := as[i]
		if strings.HasPrefix(a.Name, "preview-") && (info.Asset == nil || a.ID > info.Asset.ID) {
			info.Asset = &a
		}
	}
	if info.Asset != nil {
		info.FileName, info.By = previewLabel(*info.Asset)
		info.Cached = exists(s.previewCachePath(*info.Asset))
	}
	return info, nil
}

func (s *Storage) previewCachePath(a Asset) string {
	return filepath.Join(s.cacheDir(), fmt.Sprintf("preview-%d%s", a.ID, strings.ToLower(filepath.Ext(a.Name))))
}

// UploadPreview replaces the project's preview track.
func (p *Project) UploadPreview(path string) (*PreviewInfo, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if !previewExts[ext] {
		return nil, errors.New("можно только mp3, flac или wav")
	}
	s, err := p.storage()
	if err != nil {
		return nil, err
	}
	old, err := s.Assets()
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("preview-%d%s", time.Now().Unix(), ext)
	p.Job.Logf("Загружаю %s", filepath.Base(path))
	a, err := s.Upload(p.Job, name, p.Cfg.MachineID()+" · "+filepath.Base(path), path)
	if err != nil {
		return nil, err
	}
	// only now remove the previous one, so a failed upload never leaves you without a preview
	for _, o := range old {
		if strings.HasPrefix(o.Name, "preview-") {
			s.Delete(o.ID)
			os.Remove(s.previewCachePath(o))
		}
	}
	copyFile(path, s.previewCachePath(*a))
	p.Job.Logf("✓ Превью обновлено — старая версия удалена")
	return p.Preview()
}

// PreviewFile downloads (once) and returns the local path of the preview.
func (p *Project) PreviewFile(job *Job) (string, error) {
	info, err := p.Preview()
	if err != nil {
		return "", err
	}
	if info.Asset == nil {
		return "", errors.New("превью ещё не загружено")
	}
	s, _ := p.storage()
	dst := s.previewCachePath(*info.Asset)
	if !exists(dst) {
		// drop stale cached previews of this project
		old, _ := filepath.Glob(filepath.Join(s.cacheDir(), "preview-*"))
		for _, o := range old {
			os.Remove(o)
		}
		if err := s.Download(job, *info.Asset, dst); err != nil {
			return "", err
		}
	}
	return dst, nil
}

func (p *Project) DeletePreview() error {
	s, err := p.storage()
	if err != nil {
		return err
	}
	as, err := s.Assets()
	if err != nil {
		return err
	}
	for _, a := range as {
		if strings.HasPrefix(a.Name, "preview-") {
			if err := s.Delete(a.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

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
	var resp *http.Response
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(time.Duration(try) * 2 * time.Second)
			if rd != nil {
				rd.(*bytes.Reader).Seek(0, io.SeekStart)
				req.Body = io.NopCloser(rd)
			}
		}
		resp, err = cl.Do(req)
		// повторяем только когда запрос заведомо не дошёл до GitHub (не успели соединиться / TLS)
		if err == nil || !(strings.Contains(err.Error(), "TLS handshake") || strings.Contains(err.Error(), "dial tcp") || strings.Contains(err.Error(), "connection reset")) {
			break
		}
	}
	if err != nil {
		if strings.Contains(err.Error(), "TLS handshake") || strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "timeout") {
			return fmt.Errorf("%v\nGitHub с этого компьютера не открывается напрямую. Если у тебя есть VPN/прокси — впиши его адрес в Настройки → «Прокси для GitHub» (например http://127.0.0.1:10809) или включи в VPN-программе «системный прокси»", err)
		}
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

// stallGuard обрывает передачу, если данные не идут дольше d (после первого touch).
// Возвращает контекст для запроса, touch (вызывать при каждом куске данных), stop и признак обрыва по зависанию.
func stallGuard(parent context.Context, d time.Duration) (ctx context.Context, touch func(), stop func(), stalled func() bool) {
	ctx, cancel := context.WithCancel(parent)
	var mu sync.Mutex
	var last time.Time
	var hit bool
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				mu.Lock()
				if !last.IsZero() && time.Since(last) > d {
					hit = true
					mu.Unlock()
					cancel()
					return
				}
				mu.Unlock()
			}
		}
	}()
	var once sync.Once
	return ctx, func() { mu.Lock(); last = time.Now(); mu.Unlock() },
		func() { once.Do(func() { close(done); cancel() }) },
		func() bool { mu.Lock(); defer mu.Unlock(); return hit }
}
