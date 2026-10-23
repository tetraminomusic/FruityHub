//go:build !windows && !darwin

package app

import (
	"errors"
	"os"
	"path/filepath"
)

func elevatedReplace(src, dst string) error {
	return errors.New("нет прав на запись в " + dst)
}

func elevatedMerge(src, dst string) error {
	return errors.New("нет прав на запись в " + dst + " — скопируй вручную из пакета")
}

func documentsDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Documents")
}
