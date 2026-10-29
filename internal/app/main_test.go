package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Изоляция нужна до запуска любого теста: macOS игнорирует XDG_CONFIG_HOME,
// а Windows берёт настройки из APPDATA. Тесты не должны читать или менять
// аккаунт, проекты и кэши запущенного приложения.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "fruityhub-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for key, dir := range map[string]string{
		"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config",
		"APPDATA": "appdata", "LOCALAPPDATA": "localappdata",
	} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.Setenv(key, path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}
