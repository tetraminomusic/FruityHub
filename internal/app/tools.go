package app

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Tools: we rely on git + git-lfs. If they are missing, the app downloads
// portable builds into its own folder (MinGit on Windows, git-lfs everywhere),
// so the friend doesn't have to install anything by hand.

func toolsDir() string { return filepath.Join(ConfigDir(), "tools") }

func toolsBinDirs() []string {
	var d []string
	lfs := filepath.Join(toolsDir(), "git-lfs")
	if _, err := os.Stat(lfs); err == nil {
		d = append(d, lfs)
	}
	if runtime.GOOS == "windows" {
		mg := filepath.Join(toolsDir(), "mingit", "cmd")
		if _, err := os.Stat(mg); err == nil {
			d = append(d, mg)
			for _, sub := range []string{"ucrt64", "mingw64", "clangarm64"} {
				if b := filepath.Join(toolsDir(), "mingit", sub, "bin"); dirExists(b) {
					d = append(d, b)
				}
			}
		}
	}
	return d
}

func gitBinary() string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("git"); err == nil {
			return "git"
		}
		// Git for Windows, установленный обычным установщиком, но не попавший в PATH, — надёжнее нашего MinGit
		for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("LOCALAPPDATA") + `\Programs`} {
			if pf == "" {
				continue
			}
			if p := filepath.Join(pf, "Git", "cmd", "git.exe"); exists(p) {
				return p
			}
		}
		p := filepath.Join(toolsDir(), "mingit", "cmd", "git.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "git"
}

type ToolsStatus struct {
	Git      string `json:"git"`
	LFS      string `json:"lfs"`
	GitOK    bool   `json:"gitOk"`
	LFSOK    bool   `json:"lfsOk"`
	CanFetch bool   `json:"canFetch"`
}

var toolsCache struct {
	sync.Mutex
	key   string
	at    time.Time
	value ToolsStatus
}

func CheckTools() ToolsStatus {
	toolsCache.Lock()
	defer toolsCache.Unlock()
	key := ConfigDir() + os.Getenv("PATH")
	ttl := time.Minute
	if !toolsCache.value.GitOK || !toolsCache.value.LFSOK {
		ttl = 2 * time.Second
	}
	if toolsCache.key == key && time.Since(toolsCache.at) < ttl {
		return toolsCache.value
	}
	value := checkToolsNow()
	toolsCache.key, toolsCache.at, toolsCache.value = key, time.Now(), value
	return value
}

func checkToolsNow() ToolsStatus {
	var s ToolsStatus
	g := &Git{Dir: os.TempDir()}
	if v, err := g.RunQuiet("--version"); err == nil {
		s.Git, s.GitOK = v, true
		if l, err := g.RunQuiet("lfs", "version"); err == nil {
			s.LFS, s.LFSOK = l, true
		}
	}
	s.CanFetch = true
	return s
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func latestRelease(repo string) (*ghRelease, error) {
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: %s", resp.Status)
	}
	var r ghRelease
	return &r, json.NewDecoder(resp.Body).Decode(&r)
}

func download(job *Job, url, dst string) error {
	job.Logf("Скачиваю %s", filepath.Base(url))
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("скачивание: %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	buf := make([]byte, 256*1024)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if time.Since(last) > 500*time.Millisecond {
				job.SetProgress(fmt.Sprintf("Скачивание · %.1f МБ", float64(done)/1e6), -1)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	job.SetProgress("Скачано", 100)
	return nil
}

func unzip(src, dst string, only func(name string) (string, bool)) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := f.Name
		if only != nil {
			var ok bool
			if name, ok = only(name); !ok {
				continue
			}
		}
		p := filepath.Join(dst, filepath.FromSlash(name))
		if !strings.HasPrefix(p, filepath.Clean(dst)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untarGzOne(src, dst, base string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("в архиве нет " + base)
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) == base && h.Typeflag == tar.TypeReg {
			os.MkdirAll(dst, 0o755)
			w, err := os.OpenFile(filepath.Join(dst, base), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, tr)
			w.Close()
			return err
		}
	}
}

// InstallTools downloads whatever is missing.
func InstallTools(job *Job) error {
	st := CheckTools()
	os.MkdirAll(toolsDir(), 0o755)
	if !st.GitOK {
		if runtime.GOOS != "windows" {
			if runtime.GOOS == "darwin" {
				exec.Command("xcode-select", "--install").Start()
				return errors.New("git не найден. Открылось окно установки Command Line Tools от Apple — согласись, дождись окончания и нажми «Проверить» ещё раз")
			}
			return errors.New("git не найден — установи его пакетным менеджером")
		}
		job.Logf("Git не найден — ставлю портативный MinGit (≈40 МБ)…")
		rel, err := latestRelease("git-for-windows/git")
		if err != nil {
			return err
		}
		var url string
		for _, a := range rel.Assets {
			n := a.Name
			if strings.HasPrefix(n, "MinGit-") && strings.HasSuffix(n, "-64-bit.zip") && !strings.Contains(n, "busybox") {
				url = a.URL
			}
		}
		if url == "" {
			return errors.New("не нашёл MinGit в релизах git-for-windows")
		}
		zipPath := filepath.Join(toolsDir(), "mingit.zip")
		if err := download(job, url, zipPath); err != nil {
			return err
		}
		os.RemoveAll(filepath.Join(toolsDir(), "mingit"))
		if err := unzip(zipPath, filepath.Join(toolsDir(), "mingit"), nil); err != nil {
			return err
		}
		os.Remove(zipPath)
		job.Logf("MinGit установлен")
		st = CheckTools()
	}
	if !st.LFSOK {
		job.Logf("git-lfs не найден — скачиваю…")
		rel, err := latestRelease("git-lfs/git-lfs")
		if err != nil {
			return err
		}
		arch := runtime.GOARCH
		want := fmt.Sprintf("git-lfs-%s-%s-", map[string]string{"darwin": "darwin", "windows": "windows", "linux": "linux"}[runtime.GOOS], arch)
		var url, name string
		for _, a := range rel.Assets {
			if strings.HasPrefix(a.Name, want) && (strings.HasSuffix(a.Name, ".zip") || strings.HasSuffix(a.Name, ".tar.gz")) {
				url, name = a.URL, a.Name
			}
		}
		if url == "" {
			return fmt.Errorf("нет сборки git-lfs для %s/%s", runtime.GOOS, arch)
		}
		arc := filepath.Join(toolsDir(), name)
		if err := download(job, url, arc); err != nil {
			return err
		}
		dst := filepath.Join(toolsDir(), "git-lfs")
		bin := "git-lfs"
		if runtime.GOOS == "windows" {
			bin = "git-lfs.exe"
		}
		if strings.HasSuffix(name, ".zip") {
			err = unzip(arc, dst, func(n string) (string, bool) {
				if filepath.Base(n) == bin {
					return bin, true
				}
				return "", false
			})
		} else {
			err = untarGzOne(arc, dst, bin)
		}
		os.Remove(arc)
		if err != nil {
			return err
		}
		os.Chmod(filepath.Join(dst, bin), 0o755)
		job.Logf("git-lfs установлен")
	}
	st = CheckTools()
	if !st.GitOK || !st.LFSOK {
		return errors.New("компоненты всё ещё не найдены")
	}
	job.Logf("Готово: %s, %s", st.Git, st.LFS)
	return nil
}
