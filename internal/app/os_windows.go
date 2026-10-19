//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

func OpenURL(u string) error {
	c := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	return c.Start()
}

func OpenFolder(p string) error { return exec.Command("explorer", p).Start() }

func PickFolder() (string, error) {
	ps := `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description = 'Папка проекта FL Studio'; $f.ShowNewFolderButton = $true; $top = New-Object System.Windows.Forms.Form -Property @{TopMost=$true}; if ($f.ShowDialog($top) -eq 'OK') { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8; Write-Output $f.SelectedPath }`
	c := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	hideWindow(c)
	out, err := c.Output()
	return trimOut(out), err
}

// regValues runs `reg query` and returns REG_SZ / REG_EXPAND_SZ values.
func regValues(key string, recursive bool) map[string]string {
	args := []string{"query", key}
	if recursive {
		args = append(args, "/s")
	}
	c := exec.Command("reg", args...)
	hideWindow(c)
	out, err := c.Output()
	m := map[string]string{}
	if err != nil {
		return m
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		for _, t := range []string{"REG_EXPAND_SZ", "REG_SZ"} {
			if i := strings.Index(l, "    "+t+"    "); i > 0 {
				name := strings.TrimSpace(l[:i])
				m[name+"|"+key+"|"+l] = expandWinEnv(strings.TrimSpace(l[i+len(t)+8:]))
				break
			}
		}
	}
	return m
}

// flSettingsPluginDirs: folders from FL Studio settings (Image-Line registry
// keys) that look like plugin search folders.
func flSettingsPluginDirs() []string {
	var d []string
	for k, v := range regValues(`HKCU\Software\Image-Line`, true) {
		lk := strings.ToLower(k + v)
		if (strings.Contains(lk, "vst") || strings.Contains(lk, "plugin") || strings.Contains(lk, "clap")) && dirExists(v) {
			for _, part := range strings.Split(v, ";") {
				if dirExists(part) {
					d = append(d, part)
				}
			}
		}
	}
	return d
}

// systemVSTDirs: the classic Steinberg "VSTPluginsPath" registry value.
func systemVSTDirs() []string {
	var d []string
	for _, key := range []string{`HKLM\SOFTWARE\VST`, `HKLM\SOFTWARE\WOW6432Node\VST`, `HKCU\SOFTWARE\VST`} {
		for k, v := range regValues(key, false) {
			if strings.HasPrefix(strings.ToLower(k), "vstpluginspath") && dirExists(v) {
				d = append(d, v)
			}
		}
	}
	return d
}

var winEnvRe = regexp.MustCompile(`%([^%]+)%`)

func expandWinEnv(s string) string {
	return winEnvRe.ReplaceAllStringFunc(s, func(m string) string {
		if v := os.Getenv(m[1 : len(m)-1]); v != "" {
			return v
		}
		return m
	})
}

func killAUCache() {}

func PickAudioFile() (string, error) {
	ps := `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.OpenFileDialog; $f.Title = 'Трек для прослушивания'; $f.Filter = 'Аудио (mp3, flac, wav)|*.mp3;*.flac;*.wav'; $top = New-Object System.Windows.Forms.Form -Property @{TopMost=$true}; if ($f.ShowDialog($top) -eq 'OK') { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8; Write-Output $f.FileName }`
	c := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	hideWindow(c)
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

// moveToTrash sends a file to the Recycle Bin.
func moveToTrash(p string) error {
	ps := `Add-Type -AssemblyName Microsoft.VisualBasic; [Microsoft.VisualBasic.FileIO.FileSystem]::DeleteFile($env:UZBEK_TRASH, 'OnlyErrorDialogs', 'SendToRecycleBin')`
	c := exec.Command("powershell", "-NoProfile", "-Command", ps)
	c.Env = append(os.Environ(), "UZBEK_TRASH="+p)
	hideWindow(c)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func PickAnyFile() (string, error) {
	ps := `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.OpenFileDialog; $f.Title = 'Файл для отправки (например, установщик плагина)'; $top = New-Object System.Windows.Forms.Form -Property @{TopMost=$true}; if ($f.ShowDialog($top) -eq 'OK') { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8; Write-Output $f.FileName }`
	c := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	hideWindow(c)
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}
