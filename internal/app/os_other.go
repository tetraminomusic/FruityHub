//go:build !windows && !darwin

package app

import (
	"os"
	"os/exec"
	"path/filepath"
)

func hideWindow(cmd *exec.Cmd) {}

func OpenURL(u string) error    { return exec.Command("xdg-open", u).Start() }
func OpenFolder(p string) error { return exec.Command("xdg-open", p).Start() }

func osPickFolder() (string, error) {
	out, err := exec.Command("zenity", "--file-selection", "--directory").Output()
	return trimOut(out), err
}

func flSettingsPluginDirs() []string { return nil }
func systemVSTDirs() []string        { return nil }

func killAUCache() {}

func osPickAudioFile() (string, error) {
	out, err := exec.Command("zenity", "--file-selection", "--file-filter=*.mp3 *.flac *.wav").Output()
	return trimOut(out), err
}

func moveToTrash(p string) error {
	home, _ := os.UserHomeDir()
	return trashInto(filepath.Join(home, ".local", "share", "Trash", "files"), p)
}

func osPickAnyFile() (string, error) {
	out, err := exec.Command("zenity", "--file-selection").Output()
	return trimOut(out), err
}

func OpenFile(p string) error { return exec.Command("xdg-open", p).Start() }

func moveDirToTrash(p string) error {
	home, _ := os.UserHomeDir()
	return trashInto(filepath.Join(home, ".local", "share", "Trash", "files"), p)
}

func osPickImageFile() (string, error) {
	out, err := exec.Command("zenity", "--file-selection", "--file-filter=*.jpg *.jpeg *.png *.webp").Output()
	return trimOut(out), err
}
