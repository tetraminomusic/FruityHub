package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Обновления приложения через наш сервер: администратор выкладывает сборки (server-up.sh или админка),
// приложение видит новую версию, по кнопке скачивает её, проверяет контрольную сумму, заменяет себя
// и перезапускается.

type UpdateFile struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type UpdateManifest struct {
	Version   string                `json:"version"`
	Notes     string                `json:"notes,omitempty"`
	Published int64                 `json:"published"`
	Files     map[string]UpdateFile `json:"files"`
}

func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// newerVersion: a новее b ("0.7.10" > "0.7.9"). Сборка "dev" не обновляется.
func newerVersion(a, b string) bool {
	if a == "" || b == "dev" || b == "" {
		return false
	}
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// AvailableUpdate — версия на сервере, если она новее этой и для этой системы есть файл.
func (s *Server) AvailableUpdate() string {
	in := s.cfg.Hub().LastInbox()
	if in == nil || !newerVersion(in.Update, Version) || in.Update == s.cfg.SkipUpdate {
		return ""
	}
	return in.Update
}

func (h *Hub) LatestUpdate() (*UpdateManifest, error) {
	var m UpdateManifest
	return &m, h.do("GET", "/api/updates/latest", nil, &m)
}

// ApplyUpdate скачивает новую версию и запускает замену. После успеха приложение нужно закрыть —
// помощник дождётся этого, поставит новую версию и откроет её.
func ApplyUpdate(cfg *Config, job *Job) (string, error) {
	h := cfg.Hub()
	m, err := h.LatestUpdate()
	if err != nil {
		return "", err
	}
	if !newerVersion(m.Version, Version) {
		return "", errors.New("у тебя уже последняя версия")
	}
	f, ok := m.Files[Platform()]
	if !ok {
		return "", fmt.Errorf("на сервере нет версии %s для этой системы (%s)", m.Version, Platform())
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	tmpDir, err := os.MkdirTemp("", "fruityhub-update-")
	if err != nil {
		return "", err
	}
	dl := filepath.Join(tmpDir, "update"+filepath.Ext(f.File))
	job.Logf("Скачиваю FruityHub %s…", m.Version)
	if err := h.downloadUpdate(job, Platform(), dl, f); err != nil {
		return "", err
	}
	job.Logf("✓ Скачано и проверено")
	switch runtime.GOOS {
	case "windows":
		return m.Version, startWindowsUpdate(exe, dl)
	case "darwin":
		return m.Version, startMacUpdate(exe, dl, tmpDir)
	}
	return "", errors.New("обновление поддерживается на Windows и macOS")
}

func (h *Hub) downloadUpdate(job *Job, platform, dst string, f UpdateFile) error {
	req, err := http.NewRequest("GET", h.cfg.ServerURL()+"/api/updates/"+url.PathEscape(platform)+"/download", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return readHubResp(resp, nil)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, sum), &progressReader{r: resp.Body, total: f.Size, job: job, verb: "Скачивание"})
	out.Close()
	if err != nil {
		return err
	}
	if f.SHA256 != "" && hex.EncodeToString(sum.Sum(nil)) != f.SHA256 {
		os.Remove(dst)
		return errors.New("файл обновления повреждён при скачивании — попробуй ещё раз")
	}
	return nil
}

// Windows: запущенный exe нельзя перезаписать, поэтому помощник ждёт, пока приложение закроется,
// кладёт новый FruityHub.exe рядом (старый «прога узбека.exe» убирает) и запускает его. Если папка
// защищена (общий рабочий стол), Windows один раз спросит разрешение на замену файла.
func startWindowsUpdate(exe, newFile string) error {
	inner, outer := windowsUpdateScripts(exe, newFile, writableDir(filepath.Dir(exe)), os.Getpid())
	tmp := filepath.Dir(newFile)
	if err := os.WriteFile(filepath.Join(tmp, "replace.ps1"), []byte("\ufeff"+inner), 0o644); err != nil {
		return err
	}
	outerPath := filepath.Join(tmp, "update.ps1")
	if err := os.WriteFile(outerPath, []byte("\ufeff"+outer), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", outerPath)
	hideWindow(cmd)
	return cmd.Start()
}

// windowsUpdateScripts: replace.ps1 (замена файла, при необходимости — с правами администратора)
// и update.ps1 (ждёт закрытия приложения, заменяет, запускает новую версию от имени пользователя).
func windowsUpdateScripts(exe, newFile string, writable bool, pid int) (inner, outer string) {
	dir := filepath.Dir(exe)
	target := filepath.Join(dir, "FruityHub.exe")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	inner = strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"Copy-Item -LiteralPath " + q(newFile) + " -Destination " + q(target) + " -Force",
		"if (" + q(exe) + " -ne " + q(target) + ") { Remove-Item -LiteralPath " + q(exe) + " -Force -ErrorAction SilentlyContinue }",
	}, "\r\n")
	innerPath := filepath.Join(filepath.Dir(newFile), "replace.ps1")
	replace := "& " + q(innerPath)
	if !writable {
		replace = "Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList " + q(`-NoProfile -ExecutionPolicy Bypass -File "`+innerPath+`"`)
	}
	outer = strings.Join([]string{
		fmt.Sprintf("$p = %d", pid),
		"while (Get-Process -Id $p -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 300 }",
		"Start-Sleep -Milliseconds 500",
		"try { " + replace + " } catch { }",
		"if (Test-Path -LiteralPath " + q(target) + ") { Start-Process -FilePath " + q(target) + " } else { Start-Process -FilePath " + q(exe) + " }",
	}, "\r\n")
	return
}

// macOS: новая версия — архив с FruityHub.app. Помощник ждёт закрытия, ставит новый бандл на место
// старого целиком (поверх нельзя — сломается подпись) и открывает его.
func startMacUpdate(exe, zipFile, tmpDir string) error {
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(bundle, ".app") {
		return errors.New("приложение запущено не из .app — обнови вручную")
	}
	unpacked := filepath.Join(tmpDir, "new")
	if out, err := exec.Command("ditto", "-x", "-k", zipFile, unpacked).CombinedOutput(); err != nil {
		return fmt.Errorf("не смог распаковать обновление: %s", strings.TrimSpace(string(out)))
	}
	ents, _ := os.ReadDir(unpacked)
	newApp := ""
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".app") {
			newApp = filepath.Join(unpacked, e.Name())
		}
	}
	if newApp == "" {
		return errors.New("в обновлении нет приложения")
	}
	dest := filepath.Join(filepath.Dir(bundle), filepath.Base(newApp))
	script := macUpdateScript(bundle, newApp, dest, os.Getpid())
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	sh := filepath.Join(tmpDir, "update.sh")
	if err := os.WriteFile(sh, []byte(script+"\n"), 0o755); err != nil {
		return err
	}
	return exec.Command("/bin/sh", "-c", "nohup /bin/sh "+q(sh)+" >/dev/null 2>&1 &").Run()
}

// macUpdateScript ждёт закрытия приложения и ставит новый бандл на место старого целиком.
func macUpdateScript(bundle, newApp, dest string, pid int) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return strings.Join([]string{
		"#!/bin/sh",
		fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 0.3; done", pid),
		"sleep 0.5",
		"OLD=" + q(bundle) + "; NEW=" + q(newApp) + "; DEST=" + q(dest),
		`rm -rf "$OLD.old"`,
		`if mv "$OLD" "$OLD.old" && mv "$NEW" "$DEST"; then rm -rf "$OLD.old"; else [ -d "$OLD.old" ] && mv "$OLD.old" "$OLD"; DEST="$OLD"; fi`,
		`xattr -dr com.apple.quarantine "$DEST" 2>/dev/null`,
		`open "$DEST"`,
	}, "\n")
}

var (
	autoUpdMu   sync.Mutex
	autoUpdNext time.Time
)

// busyForUpdate — что мешает обновиться прямо сейчас (пусто — можно).
func (s *Server) busyForUpdate() string {
	busy := ""
	s.reqJobs.Range(func(k, v any) bool {
		j := v.(*Job)
		j.mu.Lock()
		done := j.Done
		j.mu.Unlock()
		if !done {
			busy = "идёт передача плагина"
			return false
		}
		return true
	})
	if busy != "" {
		return busy
	}
	s.busyReq.Range(func(k, v any) bool {
		if v == "sending" || v == "receiving" {
			busy = "идёт передача плагина"
			return false
		}
		return true
	})
	if busy != "" {
		return busy
	}
	s.jobsMu.Lock()
	running := s.running
	s.jobsMu.Unlock()
	if running > 0 {
		return "идёт задача (отправка, получение проекта…)"
	}
	return ""
}

// maybeAutoUpdate: включено «обновлять автоматически» и на сервере есть новая версия — ставим её,
// но только когда ничего не передаётся и не выполняется.
func (s *Server) maybeAutoUpdate(latest string) {
	if !s.cfg.AutoUpdate || latest == "" || !newerVersion(latest, Version) {
		return
	}
	autoUpdMu.Lock()
	if time.Now().Before(autoUpdNext) {
		autoUpdMu.Unlock()
		return
	}
	autoUpdMu.Unlock()
	if why := s.busyForUpdate(); why != "" {
		DevLog("info", 0, false, "автообновление до %s ждёт: %s", latest, why)
		autoUpdMu.Lock()
		autoUpdNext = time.Now().Add(30 * time.Second)
		autoUpdMu.Unlock()
		return
	}
	autoUpdMu.Lock()
	autoUpdNext = time.Now().Add(30 * time.Minute) // если не вышло — попробуем позже
	autoUpdMu.Unlock()
	if m, err := s.cfg.Hub().LatestUpdate(); err != nil || m.Files[Platform()].File == "" {
		return // для этой системы сборки нет
	}
	if s.updatingJob() != "" { // уже обновляется (нажали «Обновить и перезапустить»)
		return
	}
	LogActivity("", "info", "Ставлю обновление FruityHub "+latest+" (автоматически)")
	s.startUpdateJob("Обновляю FruityHub до "+latest+" (автоматически)", func(j *Job) (any, error) {
		v, err := ApplyUpdate(s.cfg, j)
		if err != nil {
			return nil, err
		}
		if why := s.busyForUpdateExcept(j); why != "" { // пока качали, что-то началось — перезапустимся позже
			j.Logf("Обновление скачано, но %s — перезапущусь, когда закончится", why)
			go func() {
				for s.busyForUpdateExcept(j) != "" {
					time.Sleep(5 * time.Second)
				}
				s.Stop()
			}()
			return v, nil
		}
		j.Logf("Перезапускаю FruityHub %s…", v)
		go func() { time.Sleep(1500 * time.Millisecond); s.Stop() }()
		return v, nil
	})
}

// busyForUpdateExcept — то же, не считая саму задачу обновления.
func (s *Server) busyForUpdateExcept(self *Job) string {
	s.jobsMu.Lock()
	running := s.running
	s.jobsMu.Unlock()
	if running > 1 {
		return "идёт задача"
	}
	busy := ""
	s.busyReq.Range(func(k, v any) bool {
		if v == "sending" || v == "receiving" {
			busy = "идёт передача плагина"
			return false
		}
		return true
	})
	return busy
}

// ---------- одно обновление за раз ----------
// Автообновление и кнопка «Обновить и перезапустить» не должны качать обновление дважды.

var updMu sync.Mutex
var updJob *Job

// updatingJob — id задачи, которая сейчас обновляет FruityHub ("" — никакая).
func (s *Server) updatingJob() string {
	updMu.Lock()
	defer updMu.Unlock()
	if updJob == nil {
		return ""
	}
	updJob.mu.Lock()
	done, failed := updJob.Done, updJob.Err != ""
	updJob.mu.Unlock()
	if done && failed { // не вышло — можно пробовать снова
		updJob = nil
		return ""
	}
	return updJob.ID
}

// startUpdateJob запускает обновление, если оно ещё не идёт; иначе возвращает уже идущую задачу.
func (s *Server) startUpdateJob(title string, fn func(j *Job) (any, error)) *Job {
	updMu.Lock()
	defer updMu.Unlock()
	if updJob != nil {
		updJob.mu.Lock()
		busy := !updJob.Done || updJob.Err == ""
		updJob.mu.Unlock()
		if busy {
			return updJob
		}
	}
	updJob = s.startJobOpt(title, "", false, fn)
	return updJob
}
