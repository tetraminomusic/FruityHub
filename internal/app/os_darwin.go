//go:build darwin

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func hideWindow(cmd *exec.Cmd) {}

func OpenURL(u string) error    { return exec.Command("open", u).Start() }
func OpenFolder(p string) error { return exec.Command("open", p).Start() }

func PickFolder() (string, error) {
	out, err := exec.Command("osascript", "-e", `POSIX path of (choose folder with prompt "Папка проекта FL Studio")`).Output()
	return trimOut(out), err
}

// flSettingsPluginDirs reads FL Studio preference plists and returns any
// folder paths in them that look like plugin search folders.
func flSettingsPluginDirs() []string {
	home, _ := os.UserHomeDir()
	var files []string
	for _, pat := range []string{"*mage-line*", "*mage-Line*", "*flstudio*", "*FL Studio*"} {
		m, _ := filepath.Glob(filepath.Join(home, "Library", "Preferences", pat))
		files = append(files, m...)
	}
	re := regexp.MustCompile(`<string>(/[^<]+)</string>`)
	var d []string
	seen := map[string]bool{}
	for _, f := range files {
		out, err := exec.Command("plutil", "-convert", "xml1", "-o", "-", f).Output()
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(string(out), -1) {
			p := m[1]
			l := strings.ToLower(p)
			if (strings.Contains(l, "vst") || strings.Contains(l, "plug") || strings.Contains(l, "clap") || strings.Contains(l, "component")) && dirExists(p) && !seen[p] {
				seen[p] = true
				d = append(d, p)
			}
		}
	}
	return d
}

func systemVSTDirs() []string { return nil }

func killAUCache() { exec.Command("killall", "-9", "AudioComponentRegistrar").Run() }

func PickAudioFile() (string, error) {
	out, err := exec.Command("osascript", "-e", `POSIX path of (choose file with prompt "Трек для прослушивания (mp3, flac или wav)" of type {"public.audio"})`).Output()
	return strings.TrimSpace(string(out)), err
}

// moveToTrash puts a file into the user's Trash.
func moveToTrash(p string) error {
	home, _ := os.UserHomeDir()
	return trashInto(filepath.Join(home, ".Trash"), p)
}

func PickAnyFile() (string, error) {
	out, err := exec.Command("osascript", "-e", `POSIX path of (choose file with prompt "Файл для отправки (например, установщик плагина)")`).Output()
	return strings.TrimSpace(string(out)), err
}

// OpenFile открывает файл программой по умолчанию (.flp — в FL Studio).
func OpenFile(p string) error { return exec.Command("open", p).Start() }

// moveDirToTrash убирает папку в Корзину через Finder (работает и на внешних дисках), иначе — сами.
func moveDirToTrash(p string) error {
	esc := strings.ReplaceAll(strings.ReplaceAll(p, `\`, `\\`), `"`, `\"`)
	if exec.Command("osascript", "-e", `tell application "Finder" to delete (POSIX file "`+esc+`")`).Run() == nil && !exists(p) {
		return nil
	}
	home, _ := os.UserHomeDir()
	return trashInto(filepath.Join(home, ".Trash"), p)
}
