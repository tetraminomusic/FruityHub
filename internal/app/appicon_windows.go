package app

import (
	"errors"
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

// groupData — содержимое набора иконок id из exe-файла (не запущенного модуля: после смены там уже другой).
func groupData(path string, id int) ([]byte, error) {
	h, err := windows.LoadLibraryEx(path, 0, loadAsData)
	if err != nil {
		return nil, fmt.Errorf("не открыл %s: %v", path, err)
	}
	defer windows.FreeLibrary(h)
	r, err := windows.FindResource(h, windows.ResourceID(id), windows.RT_GROUP_ICON)
	if err != nil {
		return nil, errors.New("в этой версии нет второй иконки")
	}
	b, err := windows.LoadResourceData(h, r)
	if err != nil || len(b) == 0 {
		return nil, errors.New("не прочитал иконку")
	}
	return append([]byte(nil), b...), nil
}

// patchExeIcon — в файле path (не запущенном) иконкой Проводника станет выбранный набор.
func patchExeIcon(path, style string) error {
	want, err := groupData(path, IconGroup(style))
	if err != nil {
		return err
	}
	if cur, err := groupData(path, iconGroupShown); err == nil && string(cur) == string(want) {
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
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"try { Move-Item -LiteralPath " + q(exe) + " -Destination " + q(old) + " -Force } catch { $h = " + q(exe+".old-icon") + "; Move-Item -LiteralPath " + q(exe) + " -Destination $h -Force; (Get-Item -LiteralPath $h -Force).Attributes = 'Hidden' }",
		"Copy-Item -LiteralPath " + q(tmp) + " -Destination " + q(exe) + " -Force",
	}, "\r\n")
	ps1 := filepath.Join(tmpDir, "icon.ps1")
	if err := os.WriteFile(ps1, []byte("\ufeff"+script), 0o644); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if writableDir(filepath.Dir(exe)) {
		cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1)
	} else {
		cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
			"Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList "+q(`-NoProfile -ExecutionPolicy Bypass -File "`+ps1+`"`))
	}
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if !exists(exe) { // что-то пошло не так посередине — вернуть как было
		for _, b := range []string{old, exe + ".old-icon"} {
			if exists(b) {
				os.Rename(b, exe)
			}
		}
		return errors.New("не получилось заменить файл приложения")
	}
	if cur, e := groupData(exe, iconGroupShown); e != nil || string(cur) != string(mustGroup(tmp)) {
		if err != nil {
			return fmt.Errorf("Windows не дал заменить %s (разрешение не дано?) %s", filepath.Base(exe), strings.TrimSpace(string(out)))
		}
		return errors.New("Windows не дал заменить файл приложения (разрешение не дано?)")
	}
	refreshShellIcon(exe)
	return nil
}

func mustGroup(path string) []byte { b, _ := groupData(path, iconGroupShown); return b }

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
