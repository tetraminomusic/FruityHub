package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	k32                  = windows.NewLazySystemDLL("kernel32.dll")
	pBeginUpdateResource = k32.NewProc("BeginUpdateResourceW")
	pUpdateResource      = k32.NewProc("UpdateResourceW")
	pEndUpdateResource   = k32.NewProc("EndUpdateResourceW")
	shell32dll           = windows.NewLazySystemDLL("shell32.dll")
	pSHChangeNotify      = shell32dll.NewProc("SHChangeNotify")
)

const (
	rtGroupIcon   = 14
	resLang       = 0x0409 // язык ресурсов, как их кладёт rsrc
	loadAsData    = 0x00000002 | 0x00000020
	shcneAssoc    = 0x08000000
	shcneUpdItem  = 0x00002000
	shcnfPathW    = 0x0005
	shcnfIDList   = 0x0000
	shcnfFlushNow = 0x1000
)

// patchExeIcon — в файле path (не запущенном) иконкой Проводника станет выбранный набор.
func patchExeIcon(path, style string) error {
	want, err := exeIconGroup(path, uint32(IconGroup(style)))
	if err != nil {
		return err
	}
	if cur, err := exeIconGroup(path, iconGroupShown); err == nil && string(cur) == string(want) {
		return nil
	}
	p, _ := syscall.UTF16PtrFromString(path)
	h, _, e := pBeginUpdateResource.Call(uintptr(unsafe.Pointer(p)), 0)
	if h == 0 {
		return fmt.Errorf("не смог открыть exe для правки: %v", e)
	}
	ok, _, e := pUpdateResource.Call(h, rtGroupIcon, iconGroupShown, resLang, uintptr(unsafe.Pointer(&want[0])), uintptr(len(want)))
	if ok == 0 {
		pEndUpdateResource.Call(h, 1)
		return fmt.Errorf("не смог записать иконку: %v", e)
	}
	if ok, _, e := pEndUpdateResource.Call(h, 0); ok == 0 {
		return fmt.Errorf("не смог сохранить exe: %v", e)
	}
	return nil
}

// replaceExeIcon: копия exe с новой иконкой встаёт на место запущенного. Старый файл уезжает во временную папку
// (запущенный exe можно переносить в пределах диска), если папка защищена — Windows один раз спросит разрешение.
func replaceExeIcon(exe, style string) error {
	want, err := exeIconGroup(exe, uint32(IconGroup(style)))
	if err != nil {
		return err
	}
	if cur, err := exeIconGroup(exe, iconGroupShown); err == nil && string(cur) == string(want) {
		refreshShellIcon(exe)
		return nil // у файла уже эта иконка
	}
	tmpDir, err := os.MkdirTemp("", "fruityhub-icon-")
	if err != nil {
		return err
	}
	tmp := filepath.Join(tmpDir, filepath.Base(exe))
	if err := copyFile(exe, tmp); err != nil {
		return err
	}
	if err := patchExeIcon(tmp, style); err != nil {
		return err
	}
	old := filepath.Join(tmpDir, "old-"+time.Now().Format("150405")+".exe")
	errFile := filepath.Join(tmpDir, "error.txt")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"try {",
		"  try { Move-Item -LiteralPath " + q(exe) + " -Destination " + q(old) + " -Force } catch { $h = " + q(exe+".old-icon") + "; Move-Item -LiteralPath " + q(exe) + " -Destination $h -Force; (Get-Item -LiteralPath $h -Force).Attributes = 'Hidden' }",
		"  Copy-Item -LiteralPath " + q(tmp) + " -Destination " + q(exe) + " -Force",
		"} catch { $_.Exception.Message | Out-File -LiteralPath " + q(errFile) + " -Encoding utf8; exit 1 }",
	}, "\r\n")
	ps1 := filepath.Join(tmpDir, "icon.ps1")
	if err := os.WriteFile(ps1, []byte("\ufeff"+script), 0o644); err != nil {
		return err
	}
	elevated := !writableDir(filepath.Dir(exe))
	var cmd *exec.Cmd
	if elevated {
		cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
			"Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList "+q(`-NoProfile -ExecutionPolicy Bypass -File "`+ps1+`"`))
	} else {
		cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1)
	}
	hideWindow(cmd)
	out, runErr := cmd.CombinedOutput()
	if !exists(exe) { // что-то пошло не так посередине — вернуть как было
		for _, b := range []string{old, exe + ".old-icon"} {
			if exists(b) {
				os.Rename(b, exe)
			}
		}
	}
	// проверяем сам файл на диске (а не загруженный модуль): встала ли копия с новой иконкой
	if cur, err := exeIconGroup(exe, iconGroupShown); err == nil && string(cur) == string(want) {
		refreshShellIcon(exe)
		return nil
	}
	why := ""
	if b, err := os.ReadFile(errFile); err == nil {
		why = strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
	} else if runErr != nil && elevated {
		why = "разрешение не дано"
	} else if runErr != nil {
		why = strings.TrimSpace(string(out))
	}
	if why == "" {
		why = "причина неизвестна"
	}
	return fmt.Errorf("Windows не дал заменить %s (%s). Окно и панель задач уже с новой иконкой; файл можно поменять ещё раз позже. Если мешает антивирус — «Контролируемый доступ к папкам» в Защитнике Windows не даёт менять файлы на рабочем столе", filepath.Base(exe), why)
}

// Проводник держит иконки в кэше — просим обновить.
func refreshShellIcon(path string) {
	p, _ := syscall.UTF16PtrFromString(path)
	pSHChangeNotify.Call(shcneUpdItem, shcnfPathW|shcnfFlushNow, uintptr(unsafe.Pointer(p)), 0)
	pSHChangeNotify.Call(shcneAssoc, shcnfIDList|shcnfFlushNow, 0, 0)
}

// cleanOldIcons — скрытый «.old-icon» рядом с exe от прошлой смены иконки (если удалось удалить).
func cleanOldIcons() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old-icon")
	}
}
