package app

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Проверки после установки полученного плагина: всё, из-за чего плагин в FL «появился, но не работает»
// (серое окно, не грузится). Что можно исправить само — исправляется, остальное показывается
// человеку с понятным действием.

type Warning struct {
	Text   string `json:"text"`
	Action string `json:"action,omitempty"` // "vcredist" — кнопка установки Visual C++, "retrydata" — положить данные ещё раз
	Arg    string `json:"arg,omitempty"`
}

type InstallReport struct {
	ID          string       `json:"id"`
	Plugin      string       `json:"plugin"`
	From        string       `json:"from"`
	Path        string       `json:"path"`
	Data        []string     `json:"data"`        // куда легли папки данных
	DataSkipped []PluginData `json:"dataSkipped"` // что отправитель не прислал (большое)
	Warnings    []Warning    `json:"warnings"`
	At          int64        `json:"at"`
	Partial     bool         `json:"partial,omitempty"` // плагин встал, но что-то из данных/путей не легло
}

// ---------- архитектура бинарника ----------

const (
	archX86   = "x86 (32 бита)"
	archX64   = "x64"
	archARM64 = "arm64"
)

// pluginBinary — сам исполняемый файл плагина (внутри пакета .vst3/.clap/.component/.vst или файл .dll).
func pluginBinary(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if !st.IsDir() {
		return path
	}
	var dirs []string
	if runtime.GOOS == "windows" {
		dirs = []string{"x86_64-win", "arm64x-win", "arm64-win", "x86-win"}
	} else {
		dirs = []string{"MacOS"}
	}
	for _, d := range dirs {
		ents, _ := os.ReadDir(filepath.Join(path, "Contents", d))
		for _, e := range ents {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				return filepath.Join(path, "Contents", d, e.Name())
			}
		}
	}
	return ""
}

// binArch читает заголовок PE (Windows) или Mach-O (Mac) и возвращает архитектуры.
func binArch(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	h := make([]byte, 4096)
	n, _ := io.ReadFull(f, h)
	h = h[:n]
	if len(h) < 64 {
		return nil
	}
	switch {
	case h[0] == 'M' && h[1] == 'Z':
		off := int(binary.LittleEndian.Uint32(h[0x3c:]))
		if off+6 > len(h) {
			b := make([]byte, 6)
			if _, err := f.ReadAt(b, int64(off)); err != nil {
				return nil
			}
			h, off = b, 0
		}
		if string(h[off:off+4]) != "PE\x00\x00" {
			return nil
		}
		switch binary.LittleEndian.Uint16(h[off+4:]) {
		case 0x14c:
			return []string{archX86}
		case 0x8664:
			return []string{archX64}
		case 0xaa64, 0xa641:
			return []string{archARM64}
		}
	case binary.LittleEndian.Uint32(h) == 0xfeedfacf:
		return []string{machoCPU(binary.LittleEndian.Uint32(h[4:]))}
	case binary.BigEndian.Uint32(h) == 0xcafebabe:
		cnt := int(binary.BigEndian.Uint32(h[4:]))
		var out []string
		for i := 0; i < cnt && 8+i*20+4 <= len(h); i++ {
			out = append(out, machoCPU(binary.BigEndian.Uint32(h[8+i*20:])))
		}
		return out
	}
	return nil
}

func machoCPU(t uint32) string {
	switch t {
	case 0x01000007:
		return archX64
	case 0x0100000c:
		return archARM64
	}
	return "?"
}

func hasArch(l []string, a string) bool {
	for _, x := range l {
		if x == a {
			return true
		}
	}
	return false
}

func hostArch() string {
	if runtime.GOARCH == "arm64" {
		return archARM64
	}
	return archX64
}

// ---------- Windows: Visual C++ и видеодрайвер ----------

// VCRedistMissing: нет библиотек Microsoft Visual C++ 2015–2022 (x64), без которых не грузится большинство плагинов.
func VCRedistMissing() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	for _, f := range []string{"vcruntime140.dll", "vcruntime140_1.dll", "msvcp140.dll"} {
		if !exists(filepath.Join(sys, f)) {
			return true
		}
	}
	return false
}

var (
	gpuOnce  sync.Once
	gpuNames []string
)

// GPUs — названия видеоадаптеров (только Windows).
func GPUs() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	gpuOnce.Do(func() {
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_VideoController).Name")
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return
		}
		for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				gpuNames = append(gpuNames, l)
			}
		}
	})
	return gpuNames
}

// BasicGPU: стоит только стандартный драйвер Windows — плагины, рисующие интерфейс через OpenGL/DirectX, показывают серое окно.
func BasicGPU() bool {
	g := GPUs()
	if len(g) == 0 {
		return false
	}
	for _, n := range g {
		l := strings.ToLower(n)
		if !strings.Contains(l, "basic display") && !strings.Contains(l, "базовый видеоадаптер") && !strings.Contains(l, "basic render") {
			return false
		}
	}
	return true
}

// ---------- активация ----------

// Производители, у которых плагины обычно активируются через аккаунт, ключ или iLok — простое копирование их не активирует.
var activationVendors = []string{"nativeinstruments", "waves", "arturia", "spectrasonics", "izotope", "softube", "slatedigital",
	"universalaudio", "output", "pluginalliance", "brainworx", "fabfilter", "xfer", "antares", "celemony", "eventide",
	"soundtoys", "toontrack", "xlnaudio", "uhe", "valhalla", "robpapen", "lennardigital", "refx", "cableguys", "sonible",
	"oeksound", "babyaudio", "kilohearts", "spitfire", "heavyocity", "steinberg", "lese", "tonestack", "ikmultimedia",
	"acustica", "polyverse", "minimal", "devious", "audiothing", "tokyodawn", "goodhertz", "mcdsp", "sonnox", "ssl",
	"solidstatelogic", "ujam", "unfiltered", "sugarbytes", "glitchmachines", "fxpansion", "korg", "roland", "synapse",
	"vengeance", "audiomodern", "landr", "neuraldsp", "stagecraft", "slate", "kv331", "dmg", "tal"}

// Бесплатные плагины у тех же производителей, которым активация не нужна.
var freePlugins = []string{"vital", "ott", "tdrnova", "tdrkotelnikov", "valhallasupermassive", "valhallafreqecho",
	"valhallaspacemodulator", "tal-noisemaker", "talnoisemaker", "kilohearts essentials", "snapheap", "multipass",
	"surge", "surgext", "dexed", "helm", "odin2", "spitfire labs", "labs", "decent sampler", "decentsampler"}

func needsActivation(name, vendor string) bool {
	n := normName(name)
	for _, f := range freePlugins {
		if n == normName(f) {
			return false
		}
	}
	v := normName(vendor)
	if v == "" {
		return false
	}
	for _, a := range activationVendors {
		if v == a || len(a) >= 4 && strings.HasPrefix(v, a) {
			return true
		}
	}
	return false
}

// ---------- Mac ----------

// macFixBundle: снимает карантин, возвращает права на запуск и, если подпись сломалась при копировании,
// подписывает пакет заново локально (ad-hoc) — иначе macOS молча не даёт FL его загрузить.
func macFixBundle(job *Job, dst string) {
	if runtime.GOOS != "darwin" {
		return
	}
	exec.Command("xattr", "-dr", "com.apple.quarantine", dst).Run()
	if bin := pluginBinary(dst); bin != "" {
		filepath.Walk(filepath.Dir(bin), func(p string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 == 0 {
				os.Chmod(p, info.Mode()|0o755)
			}
			return nil
		})
	}
	if exec.Command("codesign", "--verify", "--deep", "--strict", dst).Run() != nil {
		if exec.Command("codesign", "--force", "--deep", "--sign", "-", dst).Run() == nil {
			job.Logf("Подпись плагина сломалась при копировании — подписал заново на этом Mac")
		}
	}
}

// ---------- сборка отчёта ----------

func checkInstalled(job *Job, rep *InstallReport, format, vendor string) {
	macFixBundle(job, rep.Path)
	arch := binArch(pluginBinary(rep.Path))
	switch {
	case len(arch) == 0:
	case runtime.GOOS == "windows" && hasArch(arch, archX86) && !hasArch(arch, archX64):
		rep.Warnings = append(rep.Warnings, Warning{Text: "Это 32-битный плагин. FL Studio 64 бит запускает такие только через мост — может работать нестабильно или с серым окном. Лучше найти 64-битную версию у производителя."})
	case runtime.GOOS == "darwin" && !hasArch(arch, hostArch()):
		if hostArch() == archARM64 {
			rep.Warnings = append(rep.Warnings, Warning{Text: "Плагин собран только под Intel. FL Studio на Mac с M-чипом не загрузит его, пока FL не открыт через Rosetta (Finder → FL Studio → Свойства → «Открывать в Rosetta»). Лучше скачать новую версию у производителя."})
		} else {
			rep.Warnings = append(rep.Warnings, Warning{Text: "Плагин собран только под Apple Silicon (M-чип) — на этом Mac с Intel он не запустится."})
		}
	case runtime.GOOS == "windows" && !hasArch(arch, hostArch()) && !hasArch(arch, archX64):
		rep.Warnings = append(rep.Warnings, Warning{Text: "Плагин собран под другой процессор (" + strings.Join(arch, ", ") + ") — здесь он не запустится."})
	}
	rep.Warnings = append(rep.Warnings, missingLibWarnings(rep.Path)...)
	if VCRedistMissing() {
		rep.Warnings = append(rep.Warnings, Warning{Text: "На этом компьютере нет библиотек Microsoft Visual C++ — без них большинство плагинов не загружаются или показывают серое окно.", Action: "vcredist"})
	}
	if BasicGPU() {
		rep.Warnings = append(rep.Warnings, Warning{Text: "Видеокарта работает на стандартном драйвере Windows («Базовый видеоадаптер»). Плагины, которые рисуют интерфейс через видеокарту, показывают серое окно. Поставь драйвер видеокарты с сайта Intel / AMD / NVIDIA или производителя ноутбука."})
	}
	if needsActivation(rep.Plugin, vendor) {
		rep.Warnings = append(rep.Warnings, Warning{Text: activationWarning(rep.Plugin, vendor)})
	}
	for _, d := range rep.DataSkipped {
		rep.Warnings = append(rep.Warnings, Warning{Text: "У отправителя есть ещё папка данных «" + d.Rel + "» (" + fmtBytes(d.Size) + ") — она не передаётся: " + d.Note + "."})
	}
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f ГБ", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return fmt.Sprintf("%.0f МБ", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f КБ", float64(n)/1024)
	}
}

// ---------- отчёты для интерфейса ----------

var (
	reportsMu  sync.Mutex
	reports    []InstallReport
	reportSeen = map[string]bool{}
)

func addReport(r InstallReport) {
	reportsMu.Lock()
	defer reportsMu.Unlock()
	reports = append(reports, r)
	if len(reports) > 50 {
		reports = reports[len(reports)-50:]
	}
}

// pendingReports — отчёты с предупреждениями, которые ещё не показали.
func pendingReports() []InstallReport {
	reportsMu.Lock()
	defer reportsMu.Unlock()
	out := []InstallReport{}
	for _, r := range reports {
		if !reportSeen[r.ID] && (len(r.Warnings) > 0 || len(r.Data) > 0) {
			out = append(out, r)
		}
	}
	return out
}

func markReportSeen(id string) {
	reportsMu.Lock()
	reportSeen[id] = true
	reportsMu.Unlock()
}

func allReports() map[string]InstallReport {
	reportsMu.Lock()
	defer reportsMu.Unlock()
	out := map[string]InstallReport{}
	for _, r := range reports {
		out[r.ID] = r
	}
	return out
}

// InstallVCRedist скачивает официальный установщик Microsoft Visual C++ 2015–2022 (x64) и запускает его.
func InstallVCRedist(job *Job) error {
	if runtime.GOOS != "windows" {
		return errors.New("это нужно только на Windows")
	}
	job.Logf("Скачиваю Microsoft Visual C++ Redistributable с сайта Microsoft…")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get("https://aka.ms/vs/17/release/vc_redist.x64.exe")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("сайт Microsoft ответил " + resp.Status)
	}
	exe := filepath.Join(os.TempDir(), "vc_redist.x64.exe")
	f, err := os.Create(exe)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		return err
	}
	job.Logf("Запускаю установку — Windows попросит разрешение")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"$p = Start-Process -FilePath "+q(exe)+" -ArgumentList '/install','/passive','/norestart' -Verb RunAs -Wait -PassThru; exit $p.ExitCode")
	hideWindow(cmd)
	err = cmd.Run()
	if VCRedistMissing() {
		if err != nil {
			return errors.New("установка не завершилась (разрешение не дано?)")
		}
		return errors.New("установка прошла, но библиотеки не появились — перезагрузи компьютер")
	}
	job.Logf("✓ Visual C++ установлен. Перезапусти FL Studio")
	return nil
}
