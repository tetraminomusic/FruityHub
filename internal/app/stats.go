package app

import (
	"bufio"
	"bytes"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"flsync/internal/flp"
)

// Статистика работы в FL Studio (вкладка «Профиль»).
//
// Время берётся у самой FL Studio: в каждом .flp есть счётчик «сколько проект был открыт»
// (File → Project info → Time spent) и дата создания проекта. Счётчик растёт, пока проект открыт,
// и сохраняется вместе с файлом, поэтому каждый коммит .flp — это «столько-то минут работы»:
// разница счётчика между прежней и новой версией файла. Она записывается автору коммита.
// К моему времени добавляется ещё не сохранённое в историю (файл на диске против последнего коммита).
//
// Копии «Сохранить как…» (новое имя, тот же проект) не удваивают время: у нового файла за точку
// отсчёта берётся наибольший счётчик, который уже был у файлов с той же датой создания.
// Счётчик по каждой версии файла (blob) кешируется навсегда — версии в git не меняются.

type StatsPerson struct {
	Nick string `json:"nick"`
	Sec  int64  `json:"sec"`
	Me   bool   `json:"me,omitempty"`
}

type StatsProject struct {
	Path   string        `json:"path"`
	Name   string        `json:"name"`
	Me     int64         `json:"me"`     // моё время, сек
	Total  int64         `json:"total"`  // всех
	FL     int64         `json:"fl"`     // счётчик главного .flp сейчас (всё время проекта, в т. ч. до FruityHub)
	Last   int64         `json:"last"`   // когда последний раз работали (unix)
	People []StatsPerson `json:"people"` // кто сколько
}

type StatsWeek struct {
	Start  int64 `json:"start"` // понедельник, unix
	Me     int64 `json:"me"`
	Others int64 `json:"others"`
}

type Stats struct {
	Me       string         `json:"me"`
	Today    int64          `json:"today"`
	Week     int64          `json:"week"`  // с понедельника
	Month    int64          `json:"month"` // с 1-го числа
	All      int64          `json:"all"`
	Days     int            `json:"days"` // дней, когда работал
	Weeks    []StatsWeek    `json:"weeks"`
	Projects []StatsProject `json:"projects"`
	At       int64          `json:"at"`
}

// одна порция работы: кто, когда, сколько
type workPiece struct {
	who  string
	me   bool
	at   time.Time
	spnt time.Duration
}

type blobTime struct {
	Spent   int64 `json:"s"` // сек; -1 — счётчика нет (не .flp FL или старая версия формата)
	Created int64 `json:"c"` // unix
}

var statsCache struct {
	sync.Mutex
	blobs  map[string]blobTime
	dirty  bool
	result *Stats
	at     time.Time
}

func statsCachePath() string { return filepath.Join(ConfigDir(), "stats-cache.json") }

func loadBlobCache() {
	if statsCache.blobs != nil {
		return
	}
	statsCache.blobs = map[string]blobTime{}
	readJSON(statsCachePath(), &statsCache.blobs)
}

func statsSkip(rel string) bool {
	l := strings.ToLower(rel)
	return !strings.HasSuffix(l, ".flp") || strings.HasPrefix(l, ".flsync/") || strings.HasPrefix(l, "backup/") ||
		strings.Contains(l, "/backup/") || isCopyName(rel) || strings.Contains(l, "autosave") || strings.Contains(l, "overwritten")
}

func timeOfFLP(b []byte) blobTime {
	sp, cr, ok := flp.Timestamp(b)
	if !ok {
		return blobTime{Spent: -1}
	}
	return blobTime{Spent: int64(sp / time.Second), Created: cr.Unix()}
}

// blobTimes — счётчики для версий файлов (из кеша; недостающие — одним git cat-file --batch).
func blobTimes(g *Git, shas []string) map[string]blobTime {
	res := map[string]blobTime{}
	var need []string
	for _, s := range shas {
		if bt, ok := statsCache.blobs[s]; ok {
			res[s] = bt
		} else if _, dup := res[s]; !dup {
			res[s] = blobTime{Spent: -1}
			need = append(need, s)
		}
	}
	for len(need) > 0 {
		n := min(len(need), 300)
		out, err := g.RunInput([]byte(strings.Join(need[:n], "\n")+"\n"), "cat-file", "--batch")
		need = need[n:]
		if err != nil && len(out) == 0 {
			continue
		}
		r := bufio.NewReader(bytes.NewReader(out))
		for {
			hdr, err := r.ReadString('\n')
			if err != nil {
				break
			}
			f := strings.Fields(hdr)
			if len(f) < 3 { // «<sha> missing»
				continue
			}
			size, _ := strconv.Atoi(f[2])
			buf := make([]byte, size+1)
			if _, err := io.ReadFull(r, buf); err != nil {
				break
			}
			bt := timeOfFLP(buf[:size])
			res[f[0]] = bt
			statsCache.blobs[f[0]] = bt
			statsCache.dirty = true
		}
	}
	return res
}

type statsChange struct {
	old, new, path string
	added          bool
}

type statsCommit struct {
	who     string
	at      time.Time
	root    bool // первый коммит репозитория (без родителей)
	changes []statsChange
}

// statsLog — коммиты с .flp, от старых к новым (все ветки: и то, что друг отправил, а я ещё не получил).
func statsLog(g *Git) []statsCommit {
	out, err := g.RunQuiet("-c", "core.quotePath=false", "log", "--branches", "--remotes", "HEAD", "--no-merges", "--date-order", "--reverse",
		"--raw", "-M", "--no-abbrev", "-z", "--format=\x1e%H\x1f%an\x1f%at\x1f%P", "--", ":(icase)*.flp")
	if err != nil {
		return nil
	}
	var res []statsCommit
	for _, chunk := range strings.Split(out, "\x1e") {
		if chunk == "" {
			continue
		}
		tok := strings.Split(chunk, "\x00")
		h := strings.Split(tok[0], "\x1f")
		if len(h) < 4 {
			continue
		}
		at, _ := strconv.ParseInt(strings.TrimSpace(h[2]), 10, 64)
		c := statsCommit{who: h[1], at: time.Unix(at, 0), root: strings.TrimSpace(h[3]) == ""}
		for i := 1; i < len(tok); i++ {
			m := strings.TrimSpace(tok[i])
			if !strings.HasPrefix(m, ":") {
				continue
			}
			f := strings.Fields(m)
			if len(f) < 5 || i+1 >= len(tok) {
				continue
			}
			st := f[4]
			path := tok[i+1]
			i++
			if (st[0] == 'R' || st[0] == 'C') && i+1 < len(tok) {
				path = tok[i+1]
				i++
			}
			if st[0] == 'D' || statsSkip(path) {
				continue
			}
			ch := statsChange{old: f[2], new: f[3], path: path, added: st[0] == 'A' || st[0] == 'C'}
			if strings.Trim(ch.old, "0") == "" {
				ch.added = true
			}
			c.changes = append(c.changes, ch)
		}
		if len(c.changes) > 0 {
			res = append(res, c)
		}
	}
	return res
}

// projectWork — порции работы в одном проекте; fl — счётчик главного .flp сейчас.
func (s *Server) projectWork(lp LocalProject, name func(id, nick string) string) (pieces []workPiece, fl int64) {
	g := &Git{Dir: lp.Path, Cfg: s.cfg}
	if !g.IsRepo() {
		return nil, 0
	}
	commits := statsLog(g)
	var shas []string
	for _, c := range commits {
		for _, ch := range c.changes {
			shas = append(shas, ch.new)
			if !ch.added {
				shas = append(shas, ch.old)
			}
		}
	}
	// HEAD — для несохранённого
	head := map[string]string{}
	if out, err := g.RunQuiet("-c", "core.quotePath=false", "ls-tree", "-r", "-z", "HEAD"); err == nil {
		for _, l := range strings.Split(out, "\x00") {
			t := strings.SplitN(l, "\t", 2)
			if len(t) == 2 && !statsSkip(t[1]) {
				if f := strings.Fields(t[0]); len(f) == 3 {
					head[t[1]] = f[2]
					shas = append(shas, f[2])
				}
			}
		}
	}
	bt := blobTimes(g, shas)

	lineage := map[int64]int64{} // дата создания проекта → наибольший счётчик, что уже был
	add := func(who string, at time.Time, sec int64) {
		if sec <= 0 {
			return
		}
		w := workPiece{who: name("", who), me: s.cfg.isMe("", who), at: at, spnt: time.Duration(sec) * time.Second}
		if w.me {
			w.who = s.cfg.MachineID()
		}
		pieces = append(pieces, w)
	}
	// Первая запись проекта в историю (создание репозитория) — не работа: в .flp уже накопленное время,
	// оно становится точкой отсчёта. Считается только то, что прибавилось после.
	for i, c := range commits {
		start := i == 0 || c.root
		got := map[int64]int64{} // по проекту (дате создания) — сколько добавил этот коммит
		for _, ch := range c.changes {
			nw := bt[ch.new]
			if nw.Spent < 0 {
				continue
			}
			base, known := lineage[nw.Created]
			if !known && start {
				base = nw.Spent
			}
			if !ch.added {
				if o := bt[ch.old]; o.Spent >= 0 && o.Created == nw.Created {
					base = o.Spent
				}
			}
			if d := nw.Spent - base; d > got[nw.Created] {
				got[nw.Created] = d
			}
			if nw.Spent > lineage[nw.Created] {
				lineage[nw.Created] = nw.Spent
			}
		}
		for _, d := range got {
			add(c.who, c.at, d)
		}
	}

	// несохранённое в историю — моё
	me := s.cfg.MachineID()
	got := map[int64]int64{}
	var newest time.Time
	filepath.WalkDir(lp.Path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(lp.Path, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || strings.EqualFold(d.Name(), "Backup")) {
				return filepath.SkipDir
			}
			return nil
		}
		if statsSkip(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		cur := timeOfFLP(b)
		if cur.Spent < 0 {
			return nil
		}
		if strings.EqualFold(rel, lp.MainFLP) || (lp.MainFLP == "" && cur.Spent > fl) {
			fl = cur.Spent
		}
		base, known := lineage[cur.Created]
		if !known && len(commits) == 0 { // проект ещё ни разу не записан в историю — время в нём накоплено до FruityHub
			base = cur.Spent
		}
		if sha, ok := head[rel]; ok {
			if o := bt[sha]; o.Spent >= 0 && o.Created == cur.Created {
				base = o.Spent
			}
		}
		if dl := cur.Spent - base; dl > got[cur.Created] {
			got[cur.Created] = dl
			if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		return nil
	})
	if newest.IsZero() {
		newest = time.Now()
	}
	for _, d := range got {
		add(me, newest, d)
	}
	return pieces, fl
}

func weekStart(t time.Time) time.Time {
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	wd := (int(day.Weekday()) + 6) % 7 // понедельник — 0
	return day.AddDate(0, 0, -wd)
}

func (s *Server) computeStats() *Stats {
	statsCache.Lock()
	defer statsCache.Unlock()
	if statsCache.result != nil && time.Since(statsCache.at) < 30*time.Second {
		return statsCache.result
	}
	loadBlobCache()
	name := s.cachedNickResolver()
	now := time.Now()
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
	month := time.Date(y, mo, 1, 0, 0, 0, 0, now.Location())
	wk := weekStart(now)
	const nWeeks = 12
	st := &Stats{Me: s.cfg.MachineID(), Weeks: make([]StatsWeek, nWeeks), At: now.Unix()}
	for i := range st.Weeks {
		st.Weeks[i].Start = wk.AddDate(0, 0, -7*(nWeeks-1-i)).Unix()
	}
	days := map[string]bool{}
	for _, lp := range append([]LocalProject{}, s.cfg.Projects...) {
		if !dirExists(lp.Path) {
			continue
		}
		pieces, fl := s.projectWork(lp, name)
		sp := StatsProject{Path: lp.Path, Name: lp.DisplayName(), FL: fl}
		people := map[string]*StatsPerson{}
		for _, w := range pieces {
			sec := int64(w.spnt / time.Second)
			sp.Total += sec
			if w.at.Unix() > sp.Last {
				sp.Last = w.at.Unix()
			}
			pp := people[w.who]
			if pp == nil {
				pp = &StatsPerson{Nick: w.who, Me: w.me}
				people[w.who] = pp
			}
			pp.Sec += sec
			if i := nWeeks - 1 - int(math.Floor(wk.Sub(weekStart(w.at)).Hours()/24/7+0.5)); i >= 0 && i < nWeeks {
				if w.me {
					st.Weeks[i].Me += sec
				} else {
					st.Weeks[i].Others += sec
				}
			}
			if !w.me {
				continue
			}
			sp.Me += sec
			st.All += sec
			days[w.at.Format("2006-01-02")] = true
			if !w.at.Before(today) {
				st.Today += sec
			}
			if !w.at.Before(wk) {
				st.Week += sec
			}
			if !w.at.Before(month) {
				st.Month += sec
			}
		}
		for _, pp := range people {
			sp.People = append(sp.People, *pp)
		}
		sort.Slice(sp.People, func(i, j int) bool { return sp.People[i].Sec > sp.People[j].Sec })
		if sp.Total > 0 || sp.FL > 0 {
			st.Projects = append(st.Projects, sp)
		}
	}
	st.Days = len(days)
	sort.SliceStable(st.Projects, func(i, j int) bool {
		if st.Projects[i].Me != st.Projects[j].Me {
			return st.Projects[i].Me > st.Projects[j].Me
		}
		return st.Projects[i].Total > st.Projects[j].Total
	})
	if statsCache.dirty {
		writeJSON(statsCachePath(), statsCache.blobs)
		statsCache.dirty = false
	}
	statsCache.result, statsCache.at = st, now
	return st
}

func (s *Server) routesStats(api func(string, func(r *http.Request) (any, error))) {
	api("/api/stats", func(r *http.Request) (any, error) {
		if r.URL.Query().Get("fresh") == "1" {
			statsCache.Lock()
			statsCache.result = nil
			statsCache.Unlock()
		}
		return s.computeStats(), nil
	})
}
