package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// elevatedReplace copies src to dst in a protected folder (Program Files):
// Windows shows its usual "allow changes?" prompt once.
func elevatedReplace(src, dst string) error {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	bak := filepath.Join(ConfigDir(), "plugin-backups", time.Now().Format("20060102-150405"))
	script := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"if (Test-Path -LiteralPath " + q(dst) + ") { New-Item -ItemType Directory -Force -Path " + q(bak) + " | Out-Null; Move-Item -LiteralPath " + q(dst) + " -Destination " + q(bak) + " -Force }",
		"New-Item -ItemType Directory -Force -Path " + q(filepath.Dir(dst)) + " | Out-Null",
		"Copy-Item -LiteralPath " + q(src) + " -Destination " + q(dst) + " -Recurse -Force",
	}, "\r\n")
	ps1 := filepath.Join(os.TempDir(), "uzbek-install-"+time.Now().Format("150405.000")+".ps1")
	if err := os.WriteFile(ps1, []byte("\ufeff"+script), 0o644); err != nil {
		return err
	}
	defer os.Remove(ps1)
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
		"Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File',"+q(ps1))
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return errors.New("Windows не дал разрешение на установку в " + filepath.Dir(dst))
	}
	if !exists(dst) {
		return errors.New("установка не удалась (разрешение не дано?)")
	}
	return nil
}
