package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---------- файлы проекта (аудио из Git LFS) напрямую с компьютера участника ----------
//
// Получатель (скачивает проект или «Получить») просит недостающие LFS-объекты: POST /api/projects/{id}/fetch.
// Приложения других участников видят просьбу во «Входящих», у кого объекты есть — забирает её (claim) и
// присылает их: напрямую по локальной сети, если получатель достучится (адреса в claim), иначе потоком
// через сервер. Что не пришло — получатель докачивает с GitHub. Сервер ничего не хранит, просьбы живут в памяти.

type fetchReq struct {
	ID      string
	Project string
	From    string // кто просит
	To      string // кто взялся прислать
	Oids    []string
	Bytes   int64
	Created time.Time
	Claim   *FetchClaim
	Done    bool
	Online  int             // сколько участников было в сети, когда попросили
	Decl    map[string]bool // у кого этих файлов нет
}

// FetchClaim — что участник может прислать и как к нему подключиться напрямую.
type FetchClaim struct {
	Count int      `json:"count"`
	Bytes int64    `json:"bytes"`
	Addrs []string `json:"addrs,omitempty"` // адреса в локальной сети (ip:порт)
	Token string   `json:"token,omitempty"`
}

type FetchView struct {
	ID        string      `json:"id"`
	Project   string      `json:"project"`
	FromNick  string      `json:"fromNick"`
	ToNick    string      `json:"toNick,omitempty"`
	Count     int         `json:"count"`
	Bytes     int64       `json:"bytes"`
	Claim     *FetchClaim `json:"claim,omitempty"`
	Done      bool        `json:"done,omitempty"`
	Declined  int         `json:"declined,omitempty"`
	Oids      []string    `json:"oids,omitempty"`
	ProjectNm string      `json:"projectName,omitempty"`
}

const fetchMaxOids = 20000

func (s *Server) fetchView(f *fetchReq, withOids bool) FetchView {
	v := FetchView{ID: f.ID, Project: f.Project, FromNick: view(s.st.db.Users[f.From]).Nick, Count: len(f.Oids), Bytes: f.Bytes, Claim: f.Claim, Done: f.Done, Declined: len(f.Decl)}
	if f.To != "" {
		v.ToNick = view(s.st.db.Users[f.To]).Nick
	}
	if p := s.st.db.Projects[f.Project]; p != nil {
		v.ProjectNm = p.Name
	}
	if withOids {
		v.Oids = f.Oids
	}
	return v
}

// janitor: просьбам больше часа — конец
func (s *Server) fetchGC() {
	for id, f := range s.fetches {
		if time.Since(f.Created) > time.Hour {
			delete(s.fetches, id)
			s.stopRelay("f-" + id)
		}
	}
}

func (s *Server) createFetch(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Oids  []string `json:"oids"`
		Bytes int64    `json:"bytes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		return nil, bad("неверный запрос")
	}
	if len(req.Oids) == 0 || len(req.Oids) > fetchMaxOids {
		return nil, bad("неверный список файлов")
	}
	for _, o := range req.Oids {
		if len(o) != 64 || strings.Trim(o, "0123456789abcdef") != "" {
			return nil, bad("неверный id файла")
		}
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p := s.st.db.Projects[r.PathValue("id")]
	if p == nil || !isMember(p, me.ID) {
		return nil, notFound("проект не найден")
	}
	nOnline := 0 // участники в сети, чьё приложение умеет отдавать аудио (старые версии просьбу не увидят)
	s.fetchMu.Lock()
	for _, m := range p.Members {
		if m != me.ID && online(s.st.db.Users[m]) && time.Since(s.caps[m]) < time.Minute {
			nOnline++
		}
	}
	s.fetchMu.Unlock()
	if nOnline == 0 {
		return map[string]any{"id": "", "online": 0}, nil
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	if s.fetches == nil {
		s.fetches = map[string]*fetchReq{}
	}
	s.fetchGC()
	f := &fetchReq{ID: newID(), Project: p.ID, From: me.ID, Oids: req.Oids, Bytes: req.Bytes, Created: time.Now(), Online: nOnline, Decl: map[string]bool{}}
	s.fetches[f.ID] = f
	s.Logf("info", view(me).Nick, "просит файлы проекта «%s» у участников: %d (%.1f МБ), в сети: %d", p.Name, len(req.Oids), float64(req.Bytes)/1e6, nOnline)
	return map[string]any{"id": f.ID, "online": nOnline}, nil
}

// myFetches — открытые просьбы в моих проектах (для «Входящих»).
func (s *Server) myFetches(me *User) []FetchView {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	out := []FetchView{}
	for _, f := range s.fetches {
		if f.Done || f.From == me.ID || f.To != "" || f.Decl[me.ID] || time.Since(f.Created) > 10*time.Minute {
			continue
		}
		if p := s.st.db.Projects[f.Project]; p == nil || !isMember(p, me.ID) || !online(s.st.db.Users[f.From]) {
			continue
		}
		out = append(out, s.fetchView(f, false))
	}
	return out
}

func (s *Server) fetchFor(r *http.Request, me *User) (*fetchReq, error) {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	f := s.fetches[r.PathValue("id")]
	if f == nil {
		return nil, notFound("просьба не найдена")
	}
	p := s.st.db.Projects[f.Project]
	if f.From != me.ID && f.To != me.ID && !(f.To == "" && p != nil && isMember(p, me.ID)) {
		return nil, notFound("просьба не найдена")
	}
	return f, nil
}

// getFetch: получатель ждёт, кто возьмётся (до wait секунд); участник — забирает список файлов.
func (s *Server) getFetch(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := time.Now().Add(time.Duration(min(max(wait, 0), 25)) * time.Second)
	for {
		s.st.mu.Lock()
		f, err := s.fetchFor(r, me)
		var v FetchView
		claimed := false
		if err == nil {
			s.fetchMu.Lock()
			v = s.fetchView(f, f.From != me.ID)
			// взялся кто-то или все, кто был в сети, ответили «у меня нет»
			claimed = f.Claim != nil || (f.Online > 0 && len(f.Decl) >= f.Online)
			s.fetchMu.Unlock()
		}
		s.st.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if claimed || time.Now().After(deadline) {
			return v, nil
		}
		select {
		case <-r.Context().Done():
			return nil, nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Server) claimFetch(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var c FetchClaim
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&c); err != nil {
		return nil, bad("неверный запрос")
	}
	if len(c.Addrs) > 16 {
		c.Addrs = c.Addrs[:16]
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	f, err := s.fetchFor(r, me)
	if err != nil {
		return nil, err
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	if f.From == me.ID || f.Done {
		return nil, bad("эту просьбу забрать нельзя")
	}
	if f.To != "" && f.To != me.ID {
		return nil, &apiErr{409, "уже взялся другой участник"}
	}
	f.To, f.Claim = me.ID, &c
	s.Logf("info", view(me).Nick, "пришлёт %s файлы проекта: %d (%.1f МБ)%s", view(s.st.db.Users[f.From]).Nick, c.Count, float64(c.Bytes)/1e6,
		map[bool]string{true: ", пробуем напрямую по локальной сети", false: ""}[len(c.Addrs) > 0])
	return map[string]any{"ok": true}, nil
}

// declineFetch: у участника этих файлов нет — получатель не ждёт его зря.
func (s *Server) declineFetch(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	f, err := s.fetchFor(r, me)
	if err != nil {
		return nil, err
	}
	s.fetchMu.Lock()
	if f.From != me.ID && f.To == "" {
		f.Decl[me.ID] = true
	}
	s.fetchMu.Unlock()
	return map[string]any{"ok": true}, nil
}

func (s *Server) closeFetch(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Note string `json:"note"`
	}
	decode(r, &req)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	f, err := s.fetchFor(r, me)
	if err != nil {
		return nil, err
	}
	s.fetchMu.Lock()
	f.Done = true
	delete(s.fetches, f.ID)
	s.fetchMu.Unlock()
	s.stopRelay("f-" + f.ID)
	if req.Note != "" {
		s.Logf("info", view(me).Nick, "файлы проекта: %s", req.Note)
	}
	return map[string]any{"ok": true}, nil
}

// fetchStreamSend / fetchStreamRecv — поток через сервер, если напрямую не вышло.
func (s *Server) fetchStreamSend(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	f, err := s.fetchFor(r, me)
	label := ""
	if err == nil {
		if f.To != me.ID {
			err = forbidden("сначала возьми просьбу")
		}
		label = fmt.Sprintf("файлы проекта: %s → %s", view(me).Nick, view(s.st.db.Users[f.From]).Nick)
	}
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	size, _ := strconv.ParseInt(r.Header.Get("X-Size"), 10, 64)
	return s.pipeSend(w, r, "f-"+f.ID, size, label)
}

func (s *Server) fetchStreamRecv(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	f, err := s.fetchFor(r, me)
	if err == nil && f.From != me.ID {
		err = forbidden("это не твоя просьба")
	}
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.pipeRecv(w, r, "f-"+f.ID)
}

// pipeSend — отправитель ждёт получателя и льёт ему поток (как у плагинов, но без статусов запроса).
func (s *Server) pipeSend(w http.ResponseWriter, r *http.Request, id string, size int64, label string) (any, error) {
	rl := s.relayFor(id)
	var rs *recvSide
	select {
	case rs = <-rl.recv:
	case <-time.After(s.opt.StreamWait):
		return nil, &apiErr{408, "получатель не подключился"}
	case <-rl.stop:
		return nil, &apiErr{410, "передачу отменили"}
	case <-r.Context().Done():
		return nil, nil
	}
	rs.w.Header().Set("Content-Type", "application/octet-stream")
	if size > 0 {
		rs.w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	rs.w.WriteHeader(200)
	dr := &deadlineReader{r: stopReader{r.Body, rl.stop}, rd: http.NewResponseController(w), wr: http.NewResponseController(rs.w), d: 90 * time.Second}
	t0 := time.Now()
	n, cerr := io.CopyBuffer(&flushWriter{w: rs.w}, dr, make([]byte, 1<<20))
	dr.clear()
	el := time.Since(t0).Seconds()
	if cerr == nil && size > 0 && n != size {
		cerr = fmt.Errorf("передано %d из %d байт", n, size)
	}
	if cerr != nil {
		s.Logf("error", "server", "%s оборвалось на %.1f из %.1f МБ через %.0f с: %v", label, float64(n)/1e6, float64(size)/1e6, el, cerr)
	} else {
		s.Logf("info", "server", "%s: %.1f МБ за %.0f с (%.1f МБ/с)", label, float64(n)/1e6, el, float64(n)/1e6/max(el, 0.001))
	}
	rs.done <- cerr
	if cerr != nil {
		return nil, &apiErr{502, "передача прервалась: " + cerr.Error()}
	}
	return map[string]any{"ok": true, "bytes": n}, nil
}

func (s *Server) pipeRecv(w http.ResponseWriter, r *http.Request, id string) (any, error) {
	rl := s.relayFor(id)
	me2 := &recvSide{w: w, done: make(chan error, 1)}
	select {
	case rl.recv <- me2:
	default:
		return nil, &apiErr{409, "уже принимается"}
	}
	withdraw := func() bool {
		select {
		case <-rl.recv:
			return true
		default:
			return false
		}
	}
	select {
	case <-me2.done:
		return nil, nil
	case <-time.After(s.opt.StreamWait):
		if withdraw() {
			return nil, &apiErr{408, "отправитель не подключился"}
		}
	case <-rl.stop:
		if withdraw() {
			return nil, &apiErr{410, "передачу отменили"}
		}
	case <-r.Context().Done():
		if withdraw() {
			return nil, nil
		}
	}
	<-me2.done
	return nil, nil
}

// ---------- прямое соединение по локальной сети для передачи плагина ----------
// Отправитель открывает у себя порт и пишет сюда адреса и одноразовый ключ; получатель пробует подключиться,
// не вышло — обычный поток через сервер.

type directInfo struct {
	Addrs []string `json:"addrs"`
	Token string   `json:"token"`
	Sha   string   `json:"sha256,omitempty"`
	Size  int64    `json:"size,omitempty"`
	at    time.Time
}

func (s *Server) putDirect(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var d directInfo
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&d); err != nil {
		return nil, bad("неверный запрос")
	}
	if len(d.Addrs) > 16 {
		d.Addrs = d.Addrs[:16]
	}
	s.st.mu.Lock()
	q, err := s.myReq(r, me, true)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	d.at = time.Now()
	s.fetchMu.Lock()
	if s.directs == nil {
		s.directs = map[string]*directInfo{}
	}
	for k, v := range s.directs {
		if time.Since(v.at) > 10*time.Minute {
			delete(s.directs, k)
		}
	}
	s.directs[q.ID] = &d
	s.fetchMu.Unlock()
	return map[string]any{"ok": true}, nil
}

func (s *Server) getDirect(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	q, err := s.myReq(r, me, false)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := time.Now().Add(time.Duration(min(max(wait, 0), 25)) * time.Second)
	for {
		s.fetchMu.Lock()
		d := s.directs[q.ID]
		if d != nil && time.Since(d.at) > 2*time.Minute {
			d = nil
		}
		var cp directInfo
		if d != nil {
			cp = *d
			delete(s.directs, q.ID) // ключ одноразовый
		}
		s.fetchMu.Unlock()
		if d != nil {
			return cp, nil
		}
		if time.Now().After(deadline) {
			return directInfo{Addrs: []string{}}, nil // напрямую не предлагали — через сервер
		}
		select {
		case <-r.Context().Done():
			return nil, nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ---------- проверка скорости связи с сервером (Настройки → Система) ----------

var speedChunk = func() []byte {
	b := make([]byte, 1<<16)
	x := uint32(2463534242)
	for i := range b { // псевдослучайные байты: не сжимаются по дороге
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}()

func (s *Server) speedDown(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	mb, _ := strconv.Atoi(r.URL.Query().Get("mb"))
	mb = min(max(mb, 1), 32)
	total := int64(mb) << 20
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
	w.WriteHeader(200)
	rc := http.NewResponseController(w)
	for sent := int64(0); sent < total; {
		rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		n, err := w.Write(speedChunk[:min(int64(len(speedChunk)), total-sent)])
		if err != nil {
			break
		}
		sent += int64(n)
	}
	return nil, nil
}

func (s *Server) speedUp(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	t0 := time.Now()
	n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, 33<<20))
	return map[string]any{"bytes": n, "seconds": time.Since(t0).Seconds()}, nil
}
