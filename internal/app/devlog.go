package app

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Журнал команд для режима разработчика: какие команды git запускает приложение, какие запросы уходят
// на сервер и GitHub, что нажали в интерфейсе, как закончились задачи. Хранится в памяти (последние
// 3000 строк) и в файле logs/fruityhub.log в папке настроек — его удобно прислать при тестировании.

type DevEntry struct {
	N    int64  `json:"n"`
	T    int64  `json:"t"` // unix ms
	Kind string `json:"kind"`
	Text string `json:"text"`
	Ms   int64  `json:"ms,omitempty"`
	Err  bool   `json:"err,omitempty"`
}

// DevVerbose — писать в журнал и служебные git-команды только для чтения (status, rev-parse…), которых очень много.
var DevVerbose atomic.Bool

var readOnlyVerbs = map[string]bool{"status": true, "rev-parse": true, "rev-list": true, "ls-files": true, "log": true,
	"show": true, "symbolic-ref": true, "branch": true, "diff": true, "cat-file": true, "for-each-ref": true, "ls-remote": false, "--version": true}

func readOnlyGit(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "remote":
		return len(args) > 1 && args[1] == "get-url"
	case "config":
		return len(args) > 1 && (args[1] == "--get" || args[1] == "--get-all" || args[1] == "-l")
	case "lfs":
		return len(args) > 1 && (args[1] == "ls-files" || args[1] == "status" || args[1] == "version")
	}
	return readOnlyVerbs[args[0]]
}

var (
	devMu   sync.Mutex
	devBuf  []DevEntry
	devN    int64
	devFile *os.File
)

const devMax = 3000

var secretRe = regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|(token|key|password|code)=[^&\s]+|Bearer\s+[A-Za-z0-9._-]+)`)

// maskSecrets прячет токены и ключи, чтобы журнал можно было спокойно кому-то отправить.
func maskSecrets(s string) string {
	return secretRe.ReplaceAllStringFunc(s, func(m string) string {
		if i := strings.IndexAny(m, "= "); i > 0 {
			return m[:i+1] + "***"
		}
		return m[:4] + "***"
	})
}

// DevLog добавляет строку в журнал. kind: git, http, ui, job, info, error.
func DevLog(kind string, ms int64, isErr bool, format string, a ...any) {
	text := maskSecrets(fmt.Sprintf(format, a...))
	devMu.Lock()
	defer devMu.Unlock()
	devN++
	e := DevEntry{N: devN, T: time.Now().UnixMilli(), Kind: kind, Text: text, Ms: ms, Err: isErr}
	devBuf = append(devBuf, e)
	if len(devBuf) > devMax {
		devBuf = devBuf[len(devBuf)-devMax:]
	}
	devWrite(e)
	if (isErr || kind == "error") && !strings.Contains(text, "/api/log") {
		go reportError("[" + kind + "] " + text)
	}
}

// ---------- ошибки — в журнал сервера (админка видит ошибки всех компьютеров в одном месте) ----------

type reportLine struct {
	T    int64  `json:"t"`
	Text string `json:"text"`
}

var (
	repMu    sync.Mutex
	repQueue []reportLine
)

func reportError(text string) {
	repMu.Lock()
	defer repMu.Unlock()
	repQueue = append(repQueue, reportLine{time.Now().UnixMilli(), text})
	if len(repQueue) > 300 {
		repQueue = repQueue[len(repQueue)-300:]
	}
}

// flushErrorReports отправляет накопленные ошибки на сервер (из цикла опроса сервера).
func flushErrorReports(cfg *Config) {
	if cfg.NoErrorReports || !cfg.Hub().Registered() {
		repMu.Lock()
		if cfg.NoErrorReports {
			repQueue = nil
		}
		repMu.Unlock()
		return
	}
	repMu.Lock()
	q := repQueue
	repQueue = nil
	repMu.Unlock()
	if len(q) == 0 {
		return
	}
	if len(q) > 100 {
		q = q[len(q)-100:]
	}
	err := cfg.Hub().do("POST", "/api/log", map[string]any{"lines": q, "version": Version, "os": OSName()}, nil)
	if err != nil {
		if he, ok := err.(*HubError); ok && (he.Code == 404 || he.Code == 405) {
			return // старый сервер — журнала нет
		}
		repMu.Lock()
		repQueue = append(q, repQueue...)
		if len(repQueue) > 300 {
			repQueue = repQueue[len(repQueue)-300:]
		}
		repMu.Unlock()
	}
}

func devWrite(e DevEntry) {
	if devFile == nil {
		dir := filepath.Join(ConfigDir(), "logs")
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, "fruityhub.log")
		if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
			os.Rename(p, p+".1")
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		devFile = f
	}
	mark := ""
	if e.Err {
		mark = " ОШИБКА"
	}
	dur := ""
	if e.Ms > 0 {
		dur = fmt.Sprintf(" (%d мс)", e.Ms)
	}
	fmt.Fprintf(devFile, "%s [%s]%s %s%s\n", time.UnixMilli(e.T).Format("2006-01-02 15:04:05.000"), e.Kind, mark, e.Text, dur)
}

// DevLogSince — строки журнала новее n.
func DevLogSince(n int64) []DevEntry {
	devMu.Lock()
	defer devMu.Unlock()
	out := []DevEntry{}
	for _, e := range devBuf {
		if e.N > n {
			out = append(out, e)
		}
	}
	return out
}

func DevLogPath() string { return filepath.Join(ConfigDir(), "logs", "fruityhub.log") }

func DevLogClear() {
	devMu.Lock()
	devBuf = nil
	devMu.Unlock()
}

// loggingTransport пишет в журнал все запросы приложения в сеть (сервер, GitHub, загрузки).
type loggingTransport struct{ next http.RoundTripper }

func (t loggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.next.RoundTrip(r)
	p := r.URL.Path
	if p == "/hello" || p == "/focus" { // проверка «не запущено ли приложение уже» при старте
		return resp, err
	}
	if p == "/api/inbox" || p == "/api/ping" || p == "/api/inventory" || p == "/api/admin/logs" || p == "/api/log" { // опрос раз в несколько секунд — только ошибки
		if err == nil && resp.StatusCode < 400 {
			return resp, err
		}
	}
	ms := time.Since(start).Milliseconds()
	u := r.URL.Host + p
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	if err != nil {
		DevLog("http", ms, true, "%s %s → %v", r.Method, u, err)
	} else {
		DevLog("http", ms, resp.StatusCode >= 400, "%s %s → %d", r.Method, u, resp.StatusCode)
	}
	return resp, err
}
