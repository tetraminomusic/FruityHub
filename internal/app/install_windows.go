package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\FruityHub`

func installDir() string {
	d := os.Getenv("LOCALAPPDATA")
	if d == "" {
		return ""
	}
	return filepath.Join(d, "Programs", "FruityHub")
}

func installedExe() string { return filepath.Join(installDir(), "FruityHub.exe") }

// installedVersion — версия установленного FruityHub ("" — не установлен).
func installedVersion() string {
	if !fileExists(installedExe()) {
		return ""
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return "?"
	}
	defer k.Close()
	v, _, err := k.GetStringValue("DisplayVersion")
	if err != nil || v == "" {
		return "?"
	}
	return v
}

// номер иконки в exe для ярлыка: 0 — без фона, 1 — на тёмной плитке (наборы 2, 10, 18 по порядку)
func shortcutIconIndex(style string) int {
	if style == IconTile {
		return 1
	}
	return 0
}

// shortcuts: create — сделать ярлыки (desk/start), icon — поменять иконку у существующих, remove — убрать.
// Ярлыки — через WScript.Shell; пути и текст передаются переменными окружения (без кавычек в команде).
func shortcuts(action, exe string, desk, start bool, icon int) error {
	ps := `$ErrorActionPreference = 'Stop'
$ws = New-Object -ComObject WScript.Shell
$folders = @()
if ($env:FH_DESK -eq '1') { $folders += [Environment]::GetFolderPath('Desktop') }
if ($env:FH_START -eq '1') { $folders += [Environment]::GetFolderPath('Programs') }
foreach ($f in $folders) {
  $p = Join-Path $f 'FruityHub.lnk'
  if ($env:FH_ACTION -eq 'remove') { if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Force }; continue }
  if ($env:FH_ACTION -eq 'icon' -and -not (Test-Path -LiteralPath $p)) { continue }
  $s = $ws.CreateShortcut($p)
  $s.TargetPath = $env:FH_EXE
  $s.WorkingDirectory = $env:FH_DIR
  $s.IconLocation = $env:FH_EXE + ',' + $env:FH_ICON
  $s.Description = $env:FH_DESC
  $s.Save()
}`
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	c := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	c.Env = append(os.Environ(), "FH_ACTION="+action, "FH_EXE="+exe, "FH_DIR="+filepath.Dir(exe),
		"FH_DESK="+b(desk), "FH_START="+b(start), fmt.Sprintf("FH_ICON=%d", icon),
		"FH_DESC=FruityHub — совместная работа над проектами FL Studio")
	hideWindow(c)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("ярлыки: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeUninstallEntry(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	size := uint32(0)
	if st, err := os.Stat(exe); err == nil {
		size = uint32(st.Size() / 1024)
	}
	for name, v := range map[string]string{
		"DisplayName":     "FruityHub",
		"DisplayVersion":  Version,
		"DisplayIcon":     exe + ",0",
		"Publisher":       "tetraminomusic",
		"InstallLocation": filepath.Dir(exe),
		"UninstallString": `"` + exe + `" --uninstall`,
		"URLInfoAbout":    "https://github.com/tetraminomusic/FruityHub",
	} {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	k.SetDWordValue("NoModify", 1)
	k.SetDWordValue("NoRepair", 1)
	k.SetDWordValue("EstimatedSize", size)
	return nil
}

// RefreshInstallEntry — после обновления установленного FruityHub в «Приложениях» новая версия.
func RefreshInstallEntry() {
	exe := thisExe()
	if installDir() == "" || !samePath(filepath.Dir(exe), installDir()) {
		return
	}
	if installedVersion() != Version {
		writeUninstallEntry(exe)
	}
}

func installApp(j *Job, desktop, startMenu bool, iconStyle string) (any, error) {
	dir := installDir()
	if dir == "" {
		return nil, errors.New("не нашёл папку программ пользователя (LOCALAPPDATA)")
	}
	src := thisExe()
	dst := filepath.Join(dir, "FruityHub.exe")
	j.Logf("Копирую FruityHub в %s", dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("не смог создать %s: %w", dir, err)
	}
	tmp := dst + ".new"
	if err := copyFile(src, tmp); err != nil {
		return nil, fmt.Errorf("не смог скопировать: %w", err)
	}
	if fileExists(dst) {
		old := dst + ".old"
		os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			os.Remove(tmp)
			return nil, errors.New("установленный FruityHub сейчас открыт — закрой его и нажми «Установить» ещё раз")
		}
		defer os.Remove(old)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, fmt.Errorf("не смог поставить файл на место: %w", err)
	}
	if desktop || startMenu {
		var where []string
		if desktop {
			where = append(where, "на рабочем столе")
		}
		if startMenu {
			where = append(where, "в меню «Пуск»")
		}
		j.Logf("Создаю ярлыки %s", strings.Join(where, " и "))
		if err := shortcuts("create", dst, desktop, startMenu, shortcutIconIndex(iconStyle)); err != nil {
			return nil, err
		}
	}
	j.Logf("Добавляю FruityHub в «Приложения» Windows (оттуда его можно удалить)")
	if err := writeUninstallEntry(dst); err != nil {
		return nil, fmt.Errorf("не смог записать в «Приложения»: %w", err)
	}
	j.Logf("✓ Установлено")
	return map[string]string{"dir": dir}, nil
}

func launchInstalled(dir, old string, removeOld bool) error {
	exe := filepath.Join(dir, "FruityHub.exe")
	if !fileExists(exe) {
		return errors.New("FruityHub ещё не установлен")
	}
	args := []string{"--after-install"}
	if removeOld {
		args = append(args, "--remove-old", old)
	}
	c := exec.Command(exe, args...)
	c.Dir = dir
	return c.Start()
}

// RemoveOldExe — убрать файл, с которого ставили (запускается из установленного FruityHub, когда старый закрылся).
// В защищённой папке (общий рабочий стол) Windows один раз спросит разрешение.
func RemoveOldExe(p string) {
	if p == "" || !strings.EqualFold(filepath.Ext(p), ".exe") || samePath(p, thisExe()) || samePath(filepath.Dir(p), installDir()) {
		return
	}
	os.Remove(p + ".old-icon") // хвост прошлых версий (смена иконки подменой файла)
	var err error
	for i := 0; i < 40; i++ { // старый процесс может ещё закрываться
		if err = os.Remove(p); err == nil || os.IsNotExist(err) {
			return
		}
		if os.IsPermission(err) && !writableDir(filepath.Dir(p)) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	ps1 := filepath.Join(os.TempDir(), "fruityhub-remove-old.ps1")
	if os.WriteFile(ps1, []byte("\ufeffRemove-Item -LiteralPath "+q(p)+" -Force"), 0o644) == nil {
		c := exec.Command("powershell", "-NoProfile", "-Command",
			"Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList "+q(`-NoProfile -ExecutionPolicy Bypass -File "`+ps1+`"`))
		hideWindow(c)
		c.Run()
		os.Remove(ps1)
	}
	if fileExists(p) {
		LogActivity("", "warn", "Не убрал старый FruityHub.exe ("+p+"): Windows не дал разрешение — его можно удалить вручную")
	}
}

// setShortcutIcon — иконка у ярлыков (установленный FruityHub; сам exe не меняется).
func setShortcutIcon(style string) error {
	exe := thisExe()
	if installDir() == "" || !samePath(filepath.Dir(exe), installDir()) {
		return nil
	}
	return shortcuts("icon", exe, true, true, shortcutIconIndex(style))
}

func uninstallApp(settings bool) error {
	exe := installedExe()
	shortcuts("remove", exe, true, true, 0)
	if err := registry.DeleteKey(registry.CURRENT_USER, uninstallKey); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("не смог убрать из «Приложений»: %w", err)
	}
	// папку программы (а по желанию — и настройки) убирает помощник, когда FruityHub закроется
	ps := `$p = [int]$env:FH_PID
while (Get-Process -Id $p -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 300 }
foreach ($d in @($env:FH_DIR, $env:FH_CFG)) {
  if (-not $d) { continue }
  for ($i = 0; $i -lt 10 -and (Test-Path -LiteralPath $d); $i++) {
    Remove-Item -LiteralPath $d -Recurse -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $d) { Start-Sleep -Seconds 1 }
  }
}`
	cfgDir := ""
	if settings {
		cfgDir = ConfigDir()
	}
	c := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
	c.Env = append(os.Environ(), fmt.Sprintf("FH_PID=%d", os.Getpid()), "FH_DIR="+installDir(), "FH_CFG="+cfgDir)
	c.Dir = os.TempDir()
	hideWindow(c)
	return c.Start()
}
