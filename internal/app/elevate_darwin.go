//go:build darwin

package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// asAdmin выполняет команду оболочки с правами администратора: macOS спросит пароль в своём окне.
func asAdmin(sh string) error {
	esc := strings.ReplaceAll(strings.ReplaceAll(sh, `\`, `\\`), `"`, `\"`)
	out, err := exec.Command("osascript", "-e", `do shell script "`+esc+`" with administrator privileges`).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "-128") {
			return errors.New("разрешение не дано")
		}
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// elevatedReplace кладёт плагин в системную папку (/Library/Audio/Plug-Ins…).
func elevatedReplace(src, dst string) error {
	return asAdmin("mkdir -p " + shq(filepath.Dir(dst)) + " && rm -rf " + shq(dst) + " && /usr/bin/ditto " + shq(src) + " " + shq(dst))
}

// elevatedMerge докладывает в системную папку (/Library/…) только файлы, которых там ещё нет.
func elevatedMerge(src, dst string) error {
	return asAdmin("mkdir -p " + shq(dst) + " && /usr/bin/rsync -a --ignore-existing " + shq(strings.TrimRight(src, "/")+"/") + " " + shq(strings.TrimRight(dst, "/")+"/"))
}

func documentsDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Documents")
}
