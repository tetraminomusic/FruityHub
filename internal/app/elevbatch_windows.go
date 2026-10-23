package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func runElevBatch(ops []elevOp, log string) error {
	robolog := log + ".robocopy"
	defer os.Remove(robolog)
	var sb strings.Builder
	sb.WriteString("$ErrorActionPreference = 'Continue'\r\n$script:fail = 0\r\n")
	sb.WriteString("function Fail($m) { $script:fail++; Add-Content -LiteralPath " + psq(log) + " -Value $m -Encoding UTF8 }\r\n")
	for _, o := range ops {
		switch o.Kind {
		case "replace":
			bak := filepath.Join(ConfigDir(), "plugin-backups", time.Now().Format("20060102-150405"))
			sb.WriteString("try { if (Test-Path -LiteralPath " + psq(o.B) + ") { New-Item -ItemType Directory -Force -Path " + psq(bak) + " | Out-Null; Move-Item -LiteralPath " + psq(o.B) + " -Destination " + psq(bak) + " -Force -ErrorAction Stop }; " +
				"New-Item -ItemType Directory -Force -Path " + psq(filepath.Dir(o.B)) + " -ErrorAction Stop | Out-Null; Copy-Item -LiteralPath " + psq(o.A) + " -Destination " + psq(o.B) + " -Recurse -Force -ErrorAction Stop } " +
				"catch { Fail ('Плагин → ' + " + psq(o.B) + " + ': ' + $_.Exception.Message) }\r\n")
		case "moveaway":
			sb.WriteString("try { New-Item -ItemType Directory -Force -Path " + psq(o.B) + " | Out-Null; Move-Item -LiteralPath " + psq(o.A) + " -Destination " + psq(o.B) + " -Force -ErrorAction Stop; " +
				"$par = " + psq(filepath.Dir(o.A)) + "; if ((Test-Path -LiteralPath $par) -and -not (Get-ChildItem -LiteralPath $par -Force)) { Remove-Item -LiteralPath $par -Force } } " +
				"catch { Fail ('Старая копия ' + " + psq(o.A) + " + ': ' + $_.Exception.Message) }\r\n")
		case "merge", "mergeover":
			// merge — только недостающие файлы; mergeover (переустановка) — заменить все
			keep := " /XC /XN /XO"
			if o.Kind == "mergeover" {
				keep = " /IS /IT"
			}
			sb.WriteString("New-Item -ItemType Directory -Force -Path " + psq(o.B) + " | Out-Null\r\n")
			sb.WriteString("robocopy " + psq(o.A) + " " + psq(o.B) + " /E" + keep + " /R:1 /W:1 /NP /NJH /NJS /NDL /UNILOG+:" + psq(robolog) + " | Out-Null\r\n")
			sb.WriteString("if ($LASTEXITCODE -ge 8) { Fail ('Данные → ' + " + psq(o.B) + " + ': robocopy, код ' + $LASTEXITCODE); " +
				"Get-Content -LiteralPath " + psq(robolog) + " -ErrorAction SilentlyContinue | Where-Object { $_ -match 'ERROR|ОШИБКА' } | Select-Object -First 3 | ForEach-Object { Fail ('   ' + $_.Trim()) } }\r\n")
		case "reg":
			sb.WriteString("try { " + o.A + " } catch { Fail ('Реестр ' + " + psq(o.B) + " + ': ' + $_.Exception.Message) }\r\n")
		}
	}
	sb.WriteString("exit $script:fail\r\n")
	ps1 := strings.TrimSuffix(log, ".log") + ".ps1"
	if err := os.WriteFile(ps1, []byte("\ufeff"+sb.String()), 0o644); err != nil {
		return err
	}
	defer os.Remove(ps1)
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
		"try { $p = Start-Process powershell -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList "+psq(`-NoProfile -ExecutionPolicy Bypass -File "`+ps1+`"`)+" -ErrorAction Stop; exit $p.ExitCode } catch { exit 255 }")
	hideWindow(cmd)
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 255 {
		return errors.New("Windows не дал разрешение (окно подтверждения закрыли или нажали «Нет»)")
	}
	if err != nil {
		return errors.New("часть шагов не выполнилась")
	}
	return nil
}
