package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var regLineRe = regexp.MustCompile(`^\s{2,}(.*?)\s{2,}(REG_SZ|REG_EXPAND_SZ)\s{2,}(.*)$`)

func regQuery(args ...string) string {
	cmd := exec.Command("reg", append([]string{"query"}, args...)...)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(out), "\r", "")
}

// regCollect читает ключи плагина (найденные в его файле и SOFTWARE\<Производитель или Плагин>)
// и возвращает строковые значения с путями.
func regCollect(found []string, keys []string, vkey string) []RegValue {
	cand := append([]string{}, found...)
	for _, hive := range []string{`HKCU\SOFTWARE`, `HKLM\SOFTWARE`} {
		for _, l := range strings.Split(regQuery(hive), "\n") {
			l = strings.TrimSpace(l)
			i := strings.LastIndex(l, `\`)
			if !strings.HasPrefix(l, "HKEY_") || i < 0 {
				continue
			}
			n := l[i+1:]
			if vendorMatch(n, vkey) || anyNameMatch(n, keys) {
				cand = append(cand, `SOFTWARE\`+n)
			}
		}
	}
	var out []RegValue
	seen := map[string]bool{}
	for _, k := range cand {
		if len(out) >= 100 {
			break
		}
		for _, hive := range []string{"HKCU", "HKLM"} {
			if seen[hive+strings.ToLower(k)] {
				continue
			}
			seen[hive+strings.ToLower(k)] = true
			cur := ""
			for _, l := range strings.Split(regQuery(hive+`\`+k, "/s"), "\n") {
				if strings.HasPrefix(l, "HKEY_") {
					cur = l
					continue
				}
				m := regLineRe.FindStringSubmatch(l)
				if m == nil || cur == "" || !regPathRe.MatchString(m[3]) || regLicenseName(m[1]) {
					continue
				}
				i := strings.Index(cur, `\`)
				name := m[1]
				if strings.HasPrefix(name, "(") && strings.HasSuffix(name, ")") { // (По умолчанию) / (Default)
					name = ""
				}
				out = append(out, RegValue{Hive: hive, Key: strings.TrimSpace(cur[i+1:]), Name: name, Type: m[2], Data: m[3]})
			}
		}
	}
	return out
}

// regApply записывает пути плагина в реестр получателя: только те значения, которых у него ещё нет,
// и только если папка по этому пути у него теперь есть. Пути из чужой папки пользователя переписываются на свою.
// HKCU пишется сразу, HKLM — в batch (одним запросом прав вместе с остальным).
func regApply(job *Job, vals []RegValue, senderHome string, batch *elevBatch, overwrite bool) (done []string, problems []string) {
	home, _ := os.UserHomeDir()
	var user []string
	for _, v := range vals {
		if v.Hive != "HKCU" && v.Hive != "HKLM" || strings.ContainsAny(v.Key+v.Name+v.Data, "\n\r") || !strings.HasPrefix(strings.ToUpper(v.Key), `SOFTWARE\`) {
			continue
		}
		data := v.Data
		if senderHome != "" && len(data) >= len(senderHome) && strings.EqualFold(data[:len(senderHome)], senderHome) {
			data = home + data[len(senderHome):]
		}
		if !exists(expandEnvPrefix(expandWinEnv(data))) {
			continue
		}
		vn := []string{"/v", v.Name}
		if v.Name == "" {
			vn = []string{"/ve"}
		}
		if !overwrite && regQuery(append([]string{v.Hive + `\` + v.Key}, vn...)...) != "" {
			continue // у получателя уже есть своё значение
		}
		kind := "String"
		if v.Type == "REG_EXPAND_SZ" {
			kind = "ExpandString"
		}
		hive := "LocalMachine"
		if v.Hive == "HKCU" {
			hive = "CurrentUser"
		}
		// напрямую через .NET (64-битный вид реестра): у PowerShell-провайдера реестра бывают странные ошибки
		// вроде «No more data is available» на ключах с точкой в имени (FabFilter\Pro-Q 4\4.0)
		line := "$k = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::" + hive + ", [Microsoft.Win32.RegistryView]::Registry64).CreateSubKey(" + psq(v.Key) + "); " +
			"if ($k -eq $null) { throw 'не удалось открыть ключ' }; $k.SetValue(" + psq(v.Name) + ", " + psq(data) + ", [Microsoft.Win32.RegistryValueKind]::" + kind + "); $k.Close()"
		desc := v.Hive + `\` + v.Key + ` → ` + data
		if v.Hive == "HKCU" {
			user = append(user, "try { "+line+" } catch { Write-Output ('Реестр "+strings.ReplaceAll(v.Key, "'", "")+": ' + $_.Exception.Message) }")
			done = append(done, desc)
		} else if batch != nil {
			batch.Reg(line, v.Key)
			done = append(done, desc)
		}
	}
	if len(user) > 0 {
		ps1 := filepath.Join(os.TempDir(), "fruityhub-reg-"+time.Now().Format("150405.000")+".ps1")
		if err := os.WriteFile(ps1, []byte("\ufeff"+strings.Join(user, "\r\n")), 0o644); err == nil {
			defer os.Remove(ps1)
			cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1)
			hideWindow(cmd)
			out, _ := cmd.Output()
			for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					problems = append(problems, l)
				}
			}
		}
	}
	return done, problems
}

// regQueryText — содержимое ключа реестра (для диагностики), не больше 60 строк.
func regQueryText(key string) string {
	out := regQuery(key, "/s")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 60 {
		lines = append(lines[:60], "…")
	}
	return strings.Join(lines, "\n")
}
