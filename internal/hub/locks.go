package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------- «Взять в работу»: замок проекта хранится на сервере ----------
// Раньше замок был файлом в репозитории: взять = скачать изменения + коммит + push на GitHub (десятки секунд).
// Теперь это одна запись на сервере, друзья видят её при следующем опросе (раз в несколько секунд).

type LockView struct {
	UserID string `json:"userId"`
	User   string `json:"user"` // ник
	OS     string `json:"os,omitempty"`
	Since  int64  `json:"since"`
	Note   string `json:"note,omitempty"`
}

func (s *Server) lockView(l *Lock) *LockView {
	if l == nil {
		return nil
	}
	return &LockView{UserID: l.User, User: view(s.st.db.Users[l.User]).Nick, OS: l.OS, Since: l.Since, Note: l.Note}
}

func (s *Server) takeLock(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	var req struct {
		Force bool   `json:"force"`
		Note  string `json:"note"`
		OS    string `json:"os"`
	}
	decode(r, &req)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p, err := s.memberProject(r, me)
	if err != nil {
		return nil, err
	}
	if l := p.Lock; l != nil && l.User != me.ID && !req.Force {
		return nil, &apiErr{409, fmt.Sprintf("проект сейчас в работе у %s (с %s). Попроси его нажать «Отправить и освободить» или забери принудительно",
			view(s.st.db.Users[l.User]).Nick, time.Unix(l.Since, 0).Format("02.01 15:04"))}
	}
	if req.OS == "" {
		req.OS = me.OS
	}
	since := now()
	if p.Lock != nil && p.Lock.User == me.ID {
		since = p.Lock.Since
	}
	p.Lock = &Lock{User: me.ID, OS: req.OS, Since: since, Note: strings.TrimSpace(req.Note)}
	s.Logf("info", view(me).Nick, "взял в работу «%s»%s", p.Name, map[bool]string{true: " (принудительно)", false: ""}[req.Force])
	return s.lockView(p.Lock), s.st.save()
}

// releaseLock: освободить может тот, у кого замок (или кто угодно с force — например, если друг пропал).
func (s *Server) releaseLock(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	p, err := s.memberProject(r, me)
	if err != nil {
		return nil, err
	}
	if p.Lock == nil {
		return map[string]any{"ok": true}, nil
	}
	if p.Lock.User != me.ID && r.URL.Query().Get("force") != "1" {
		return nil, &apiErr{409, "проект в работе у " + view(s.st.db.Users[p.Lock.User]).Nick + " — освободить может только он"}
	}
	p.Lock = nil
	s.Logf("info", view(me).Nick, "освободил «%s»", p.Name)
	return map[string]any{"ok": true}, s.st.save()
}

// myLocks — замки проектов, где я участник (уходят в inbox, чтобы приложение сразу видело чужие замки).
func (s *Server) myLocks(me *User) map[string]*LockView {
	out := map[string]*LockView{}
	for _, p := range s.st.db.Projects {
		if isMember(p, me.ID) {
			out[p.ID] = s.lockView(p.Lock)
		}
	}
	return out
}

// ---------- договорённость о зависимостях плагина перед передачей ----------
// Отправитель публикует список файлов, которые собирается отправить вместе с плагином (offer),
// получатель отвечает, какие из них у него уже есть (have) — они не передаются.
// Живёт только в памяти: после перезапуска сервера отправитель просто отправит всё.

type nego struct {
	seq     int
	offer   []byte
	have    []byte
	haveSeq int
	at      time.Time
}

const negoMax = 8 << 20 // список файлов одной передачи

func (s *Server) negoFor(id string, create bool) *nego {
	s.negoMu.Lock()
	defer s.negoMu.Unlock()
	if s.negos == nil {
		s.negos = map[string]*nego{}
	}
	n := s.negos[id]
	if n == nil && create {
		n = &nego{}
		s.negos[id] = n
	}
	// старые договорённости (больше суток) — выбрасываем
	for k, v := range s.negos {
		if time.Since(v.at) > 24*time.Hour && k != id {
			delete(s.negos, k)
		}
	}
	return n
}

func (s *Server) dropNego(id string) {
	s.negoMu.Lock()
	delete(s.negos, id)
	s.negoMu.Unlock()
}

// offerSeq — номер текущего предложения (0 — нет) для ReqView.
func (s *Server) offerSeq(id string) int {
	s.negoMu.Lock()
	defer s.negoMu.Unlock()
	if n := s.negos[id]; n != nil && n.offer != nil && n.haveSeq != n.seq {
		return n.seq
	}
	return 0
}

func (s *Server) putOffer(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	_, err := s.myReq(r, me, true)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, negoMax+1))
	if err != nil || len(b) > negoMax || !json.Valid(b) {
		return nil, bad("слишком большой или неверный список файлов")
	}
	n := s.negoFor(r.PathValue("id"), true)
	s.negoMu.Lock()
	n.seq++
	n.offer, n.have, n.haveSeq, n.at = b, nil, 0, time.Now()
	seq := n.seq
	s.negoMu.Unlock()
	return map[string]any{"seq": seq}, nil
}

func (s *Server) getOffer(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	_, err := s.myReq(r, me, false)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	n := s.negoFor(r.PathValue("id"), false)
	if n == nil {
		return nil, notFound("нет списка")
	}
	s.negoMu.Lock()
	defer s.negoMu.Unlock()
	if n.offer == nil {
		return nil, notFound("нет списка")
	}
	return map[string]any{"seq": n.seq, "offer": json.RawMessage(n.offer)}, nil
}

func (s *Server) putHave(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	_, err := s.myReq(r, me, false)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var req struct {
		Seq  int             `json:"seq"`
		Have json.RawMessage `json:"have"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, negoMax)).Decode(&req); err != nil {
		return nil, bad("неверный ответ")
	}
	n := s.negoFor(r.PathValue("id"), false)
	if n == nil {
		return nil, notFound("нет списка")
	}
	s.negoMu.Lock()
	defer s.negoMu.Unlock()
	if req.Seq != n.seq {
		return nil, &apiErr{409, "список уже устарел"}
	}
	n.have, n.haveSeq = req.Have, req.Seq
	return map[string]any{"ok": true}, nil
}

// getHave: отправитель ждёт ответа получателя (до 20 секунд в одном запросе).
func (s *Server) getHave(w http.ResponseWriter, r *http.Request, me *User) (any, error) {
	s.st.mu.Lock()
	_, err := s.myReq(r, me, true)
	s.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		n := s.negoFor(r.PathValue("id"), false)
		if n == nil {
			return nil, notFound("нет списка")
		}
		s.negoMu.Lock()
		have, hs, seq := n.have, n.haveSeq, n.seq
		s.negoMu.Unlock()
		if have != nil && hs == seq {
			return map[string]any{"seq": seq, "have": json.RawMessage(have)}, nil
		}
		if time.Now().After(deadline) {
			return nil, &apiErr{408, "получатель ещё не ответил"}
		}
		select {
		case <-r.Context().Done():
			return nil, nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}
