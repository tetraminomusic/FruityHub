package hub

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Журнал сервера: запросы с ошибками, передачи плагинов, ошибки, которые прислали приложения участников.
// Последние 5000 строк — в памяти (админка читает их), всё — в файл data/logs/server.log (до 10 МБ, потом .1).

type LogEntry struct {
	N     int64  `json:"n"`
	T     int64  `json:"t"`     // unix ms
	Level string `json:"level"` // info, error, client
	Src   string `json:"src"`   // server или ник участника
	Text  string `json:"text"`
}

type serverLog struct {
	mu   sync.Mutex
	buf  []LogEntry
	n    int64
	file *os.File
	path string
	// ограничение на присланные приложениями строки: ник → сколько за текущую минуту
	rate    map[string]int
	rateMin int64
}

const logMax = 5000

func newServerLog(dataDir string) *serverLog {
	l := &serverLog{rate: map[string]int{}}
	if dataDir != "" {
		dir := filepath.Join(dataDir, "logs")
		os.MkdirAll(dir, 0o700)
		l.path = filepath.Join(dir, "server.log")
	}
	return l
}

func (l *serverLog) add(level, src, text string) {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n++
	e := LogEntry{N: l.n, T: time.Now().UnixMilli(), Level: level, Src: src, Text: text}
	l.buf = append(l.buf, e)
	if len(l.buf) > logMax {
		l.buf = l.buf[len(l.buf)-logMax:]
	}
	if l.path == "" {
		return
	}
	if l.file == nil {
		if st, err := os.Stat(l.path); err == nil && st.Size() > 10<<20 {
			os.Rename(l.path, l.path+".1")
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		l.file = f
	}
	fmt.Fprintf(l.file, "%s [%s] %s: %s\n", time.UnixMilli(e.T).Format("2006-01-02 15:04:05"), level, src, text)
	if st, err := l.file.Stat(); err == nil && st.Size() > 10<<20 {
		l.file.Close()
		l.file = nil
		os.Rename(l.path, l.path+".1")
	}
}

func (l *serverLog) since(n int64) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []LogEntry{}
	for _, e := range l.buf {
		if e.N > n {
			out = append(out, e)
		}
	}
	return out
}

// allow: не больше 300 строк в минуту от одного участника.
func (l *serverLog) allow(src string, k int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := time.Now().Unix() / 60
	if m != l.rateMin {
		l.rateMin, l.rate = m, map[string]int{}
	}
	left := 300 - l.rate[src]
	if left <= 0 {
		return 0
	}
	if k > left {
		k = left
	}
	l.rate[src] += k
	return k
}

// Logf пишет строку в журнал сервера.
func (s *Server) Logf(level, src, format string, a ...any) {
	s.log.add(level, src, fmt.Sprintf(format, a...))
}

// LogWriter — для стандартного log: строки уходят в журнал сервера (и дальше, в w).
func (s *Server) LogWriter(w io.Writer) io.Writer {
	return logWriter{s: s, next: w}
}

type logWriter struct {
	s    *Server
	next io.Writer
}

func (lw logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		// стандартный log ставит дату — она в журнале уже есть
		if len(line) > 20 && line[4] == '/' && line[7] == '/' && line[10] == ' ' {
			line = line[20:]
		}
		level := "info"
		switch {
		case strings.Contains(line, "TLS handshake error"): // сканеры интернета и обрывы соединений — не поломка
		case strings.Contains(strings.ToLower(line), "error") || strings.Contains(line, "ошибк") || strings.Contains(line, "panic"):
			level = "error"
		}
		lw.s.log.add(level, "server", line)
	}
	if lw.next != nil {
		return lw.next.Write(p)
	}
	return len(p), nil
}

// clientLog: приложение участника присылает свои ошибки — чтобы их было видно в админке в одном месте.
func (s *Server) clientLog(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Lines []struct {
			T    int64  `json:"t"`
			Text string `json:"text"`
		} `json:"lines"`
		Version string `json:"version"`
		OS      string `json:"os"`
	}
	if err := decode(r, &req); err != nil {
		return nil, bad("неверный формат")
	}
	nick := view(me).Nick
	n := s.log.allow(nick, min(len(req.Lines), 100))
	for _, l := range req.Lines[:n] {
		t := l.Text
		if len(t) > 3000 {
			t = t[:3000] + "…"
		}
		ver := ""
		if req.Version != "" {
			ver = " (" + req.Version + ", " + req.OS + ")"
		}
		when := ""
		if l.T > 0 {
			when = time.UnixMilli(l.T).Format("15:04:05") + " "
		}
		s.log.add("client", nick+ver, when+t)
	}
	return map[string]any{"ok": true, "accepted": n}, nil
}

func (s *Server) adminLogs(w http.ResponseWriter, r *http.Request) (any, error) {
	n, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	return map[string]any{"entries": s.log.since(n), "file": s.log.path}, nil
}

// adminLogFile отдаёт файл журнала целиком (последние 5 МБ).
func (s *Server) adminLogFile(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.log.path == "" {
		return nil, notFound("журнал не пишется в файл")
	}
	b, err := os.ReadFile(s.log.path)
	if err != nil {
		return nil, notFound("журнал пуст")
	}
	if len(b) > 5<<20 {
		b = b[len(b)-5<<20:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
	return nil, nil
}
