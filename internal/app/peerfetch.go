package app

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------- аудио проекта (Git LFS) с компьютера участника, а не с GitHub ----------
//
// Перед тем как качать аудио с GitHub, просим недостающие LFS-объекты у участников проекта, которые сейчас
// в сети: у кого они есть, присылает их — по локальной сети напрямую или потоком через сервер FruityHub.
// Каждый объект проверяется по sha256 (имя объекта и есть его хеш), поэтому прийти «не то» не может.
// Что не пришло — git lfs докачает с GitHub как обычно.

type FetchClaim struct {
	Count int      `json:"count"`
	Bytes int64    `json:"bytes"`
	Addrs []string `json:"addrs,omitempty"`
	Token string   `json:"token,omitempty"`
}

type FetchView struct {
	ID          string      `json:"id"`
	Project     string      `json:"project"`
	FromNick    string      `json:"fromNick"`
	ToNick      string      `json:"toNick,omitempty"`
	Count       int         `json:"count"`
	Bytes       int64       `json:"bytes"`
	Claim       *FetchClaim `json:"claim,omitempty"`
	Declined    int         `json:"declined,omitempty"`
	Oids        []string    `json:"oids,omitempty"`
	ProjectName string      `json:"projectName,omitempty"`
}

func (h *Hub) CreateFetch(project string, oids []string, bytes int64) (id string, online int, err error) {
	var out struct {
		ID     string `json:"id"`
		Online int    `json:"online"`
	}
	err = h.do("POST", "/api/projects/"+url.PathEscape(project)+"/fetch", map[string]any{"oids": oids, "bytes": bytes}, &out)
	return out.ID, out.Online, err
}

func (h *Hub) GetFetch(id string, wait int) (*FetchView, error) {
	var v FetchView
	err := h.do("GET", fmt.Sprintf("/api/fetches/%s?wait=%d", url.PathEscape(id), wait), nil, &v)
	return &v, err
}

func (h *Hub) ClaimFetch(id string, c FetchClaim) error {
	return h.do("POST", "/api/fetches/"+url.PathEscape(id)+"/claim", c, nil)
}

func (h *Hub) DeclineFetch(id string) error {
	return h.do("POST", "/api/fetches/"+url.PathEscape(id)+"/decline", map[string]any{}, nil)
}

func (h *Hub) CloseFetch(id, note string) error {
	return h.do("DELETE", "/api/fetches/"+url.PathEscape(id), map[string]any{"note": note}, nil)
}

// ---------- LFS-объекты проекта ----------

// peerFetchMin — меньше этого аудио берём сразу с GitHub.
var peerFetchMin int64 = 8 << 20

type lfsObj struct {
	Oid  string `json:"oid"`
	Size int64  `json:"size"`
	Name string `json:"name"`
}

// lfsObjectsDir — где git-lfs хранит объекты этого репозитория.
func lfsObjectsDir(g *Git) string {
	if dirExists(filepath.Join(g.Dir, ".git")) {
		return filepath.Join(g.Dir, ".git", "lfs", "objects")
	}
	if out, err := g.RunQuiet("rev-parse", "--git-common-dir"); err == nil && strings.TrimSpace(out) != "" {
		d := strings.TrimSpace(out)
		if !filepath.IsAbs(d) {
			d = filepath.Join(g.Dir, d)
		}
		return filepath.Join(d, "lfs", "objects")
	}
	return filepath.Join(g.Dir, ".git", "lfs", "objects")
}

func lfsObjPath(dir, oid string) string {
	return filepath.Join(dir, oid[0:2], oid[2:4], oid)
}

func validOid(o string) bool {
	return len(o) == 64 && strings.Trim(o, "0123456789abcdef") == ""
}

// lfsObjects — LFS-файлы в ref (пусто — текущая версия) с размерами.
func lfsObjects(g *Git, ref string) []lfsObj {
	args := []string{"lfs", "ls-files", "--json"}
	if ref != "" {
		args = append(args, ref)
	}
	if out, err := g.RunQuiet(args...); err == nil {
		var v struct {
			Files []lfsObj `json:"files"`
		}
		if json.Unmarshal([]byte(out), &v) == nil {
			return dedupObjs(v.Files)
		}
	}
	// старый git-lfs без --json: «oid * имя», размеров нет
	args = []string{"lfs", "ls-files", "-l"}
	if ref != "" {
		args = append(args, ref)
	}
	out, err := g.RunQuiet(args...)
	if err != nil {
		return nil
	}
	var res []lfsObj
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.SplitN(strings.TrimSpace(sc.Text()), " ", 3)
		if len(f) == 3 && validOid(f[0]) {
			res = append(res, lfsObj{Oid: f[0], Name: f[2]})
		}
	}
	return dedupObjs(res)
}

func dedupObjs(l []lfsObj) []lfsObj {
	seen := map[string]bool{}
	var out []lfsObj
	for _, o := range l {
		if validOid(o.Oid) && !seen[o.Oid] {
			seen[o.Oid] = true
			out = append(out, o)
		}
	}
	return out
}

func tarSize(sizes []int64) int64 {
	var n int64 = 1024 // два нулевых блока в конце
	for _, s := range sizes {
		n += 512 + (s+511)/512*512
	}
	return n
}

// ---------- получатель ----------

// peerFetchLFS просит у участников в сети аудио, которого нет на этом компьютере (для ref, «» — текущая версия).
// Ошибки не роняют получение: что не пришло, докачается с GitHub.
func (p *Project) peerFetchLFS(ref string) (got int, gotBytes int64) {
	job := p.Job
	if p.Cfg == nil || !p.Cfg.Hub().Registered() || p.Cfg.NoPeerFetch {
		return
	}
	pid := p.hubID()
	if pid == "" {
		return
	}
	g := p.git()
	dir := lfsObjectsDir(g)
	var need []lfsObj
	var total int64
	for _, o := range lfsObjects(g, ref) {
		if !exists(lfsObjPath(dir, o.Oid)) {
			need = append(need, o)
			total += o.Size
		}
	}
	if len(need) == 0 || (total > 0 && total < peerFetchMin) {
		return // мелочь быстрее взять с GitHub, чем ждать ответа участников
	}
	oids := make([]string, len(need))
	want := map[string]int64{}
	for i, o := range need {
		oids[i] = o.Oid
		want[o.Oid] = o.Size
	}
	id, online, err := p.Cfg.Hub().CreateFetch(pid, oids, total)
	if err != nil || id == "" {
		if err == nil && online == 0 {
			job.Logf("Участников проекта сейчас нет в сети — аудио скачаю с GitHub")
		}
		return
	}
	note := ""
	defer func() { p.Cfg.Hub().CloseFetch(id, note) }()
	job.SetProgress(fmt.Sprintf("Спрашиваю аудио (%d файлов, %s) у участников в сети…", len(need), fmtBytes(total)), -1)
	var fv *FetchView
	for t0 := time.Now(); time.Since(t0) < 30*time.Second; {
		fv, err = p.Cfg.Hub().GetFetch(id, 20)
		if err != nil || fv.Claim != nil || (online > 0 && fv.Declined >= online) {
			break
		}
	}
	if err != nil || fv == nil || fv.Claim == nil {
		job.Logf("Ни у кого из участников в сети нужного аудио нет — скачаю с GitHub")
		return
	}
	c := fv.Claim
	job.Logf("%s пришлёт аудио: %d файлов, %s", fv.ToNick, c.Count, fmtBytes(c.Bytes))
	// сторож зависаний: полторы минуты без данных — обрыв, остальное докачается с GitHub
	sctx, touch, stop, stalled := stallGuard(job.Ctx(), stallAfter)
	defer stop()
	var body io.ReadCloser
	var contentLen int64
	via := "через сервер FruityHub"
	if len(c.Addrs) > 0 {
		if resp, err := directGet(sctx, c.Addrs, c.Token); err == nil {
			body, contentLen, via = resp.Body, resp.ContentLength, "по локальной сети"
		}
	}
	if body == nil {
		resp, err := p.Cfg.Hub().fetchRelayRecv(sctx, id)
		if err != nil {
			note = "не дошло: " + err.Error()
			job.Logf("Аудио от %s не пришло (%v) — скачаю с GitHub", fv.ToNick, err)
			return
		}
		body, contentLen = resp.Body, resp.ContentLength
	}
	defer body.Close()
	touch()
	t0 := time.Now()
	got, gotBytes, err = receiveLFSTar(job, &touchReader{body, touch}, contentLen, dir, want, fv.ToNick, via)
	if err != nil && stalled() {
		err = errStalled
	}
	el := time.Since(t0).Seconds()
	if got > 0 {
		job.Logf("✓ Аудио от %s %s: %d файлов, %s (%.1f МБ/с)", fv.ToNick, via, got, fmtBytes(gotBytes), float64(gotBytes)/1e6/max(el, 0.001))
		LogActivity(p.Dir, "ok", fmt.Sprintf("Аудио проекта пришло от %s %s: %d файлов, %s", fv.ToNick, via, got, fmtBytes(gotBytes)))
	}
	if err != nil {
		note = fmt.Sprintf("оборвалось на %d файлах: %v", got, err)
		job.Logf("Передача аудио оборвалась (%v) — остальное скачаю с GitHub", err)
	} else {
		note = fmt.Sprintf("получено %d файлов, %s %s за %.0f с", got, fmtBytes(gotBytes), via, el)
	}
	return
}

// receiveLFSTar раскладывает присланные объекты, проверяя каждый по sha256.
func receiveLFSTar(job *Job, r io.Reader, contentLen int64, dir string, want map[string]int64, from, via string) (int, int64, error) {
	pr := &progressReader{r: r, total: contentLen, job: job, verb: "Аудио от " + from + " " + via}
	tr := tar.NewReader(pr)
	var n int
	var bytes int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return n, bytes, nil
		}
		if err != nil {
			return n, bytes, err
		}
		oid := hdr.Name
		sz, ok := want[oid]
		if !ok || !validOid(oid) || (sz > 0 && hdr.Size != sz) {
			return n, bytes, fmt.Errorf("пришёл лишний файл %q", oid)
		}
		dst := lfsObjPath(dir, oid)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		tmp := dst + ".part"
		f, err := os.Create(tmp)
		if err != nil {
			return n, bytes, err
		}
		hs := sha256.New()
		w, err := io.CopyBuffer(io.MultiWriter(f, hs), tr, make([]byte, 1<<20))
		f.Close()
		if err != nil {
			os.Remove(tmp)
			return n, bytes, err
		}
		if hex.EncodeToString(hs.Sum(nil)) != oid {
			os.Remove(tmp)
			return n, bytes, fmt.Errorf("файл %s… пришёл повреждённым", oid[:8])
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return n, bytes, err
		}
		n++
		bytes += w
		delete(want, oid)
	}
}

func (h *Hub) fetchRelayRecv(ctx context.Context, id string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", h.cfg.ServerURL()+"/api/fetches/"+url.PathEscape(id)+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	resp, err := streamClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("сервер недоступен: %v", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, readHubResp(resp, nil)
	}
	return resp, nil
}

// ---------- отправитель (у кого проект есть) ----------

// serveFetches: участники просят аудио проектов, которые лежат на этом компьютере.
func (s *Server) serveFetches(list []FetchView) {
	for _, f := range list {
		if _, seen := s.fetchSeen.LoadOrStore(f.ID, true); seen {
			continue
		}
		go s.serveFetch(f)
	}
}

func (s *Server) projectByHubID(id string) *Project {
	for _, lp := range s.cfg.Projects {
		if !exists(lp.Path) {
			continue
		}
		p := &Project{Dir: lp.Path, Cfg: s.cfg}
		if p.hubID() == id {
			return p
		}
	}
	return nil
}

func (s *Server) serveFetch(f FetchView) {
	h := s.cfg.Hub()
	p := s.projectByHubID(f.Project)
	if p == nil || s.cfg.NoPeerFetch {
		h.DeclineFetch(f.ID)
		return
	}
	full, err := h.GetFetch(f.ID, 0)
	if err != nil {
		return
	}
	dir := lfsObjectsDir(p.git())
	var have []string
	var sizes []int64
	var bytes int64
	for _, oid := range full.Oids {
		if !validOid(oid) {
			continue
		}
		if st, err := os.Stat(lfsObjPath(dir, oid)); err == nil && st.Mode().IsRegular() {
			have = append(have, oid)
			sizes = append(sizes, st.Size())
			bytes += st.Size()
		}
	}
	if len(have) == 0 {
		h.DeclineFetch(f.ID)
		return
	}
	size := tarSize(sizes)
	write := func(job *Job, w io.Writer) error {
		tw := tar.NewWriter(w)
		var done int64
		t0 := time.Now()
		last := time.Time{}
		for i, oid := range have {
			fh, err := os.Open(lfsObjPath(dir, oid))
			if err != nil {
				return err
			}
			hdr := &tar.Header{Name: oid, Size: sizes[i], Mode: 0o644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0)}
			if err := tw.WriteHeader(hdr); err != nil {
				fh.Close()
				return err
			}
			buf := make([]byte, 1<<20)
			for {
				n, rerr := fh.Read(buf)
				if n > 0 {
					if _, err := tw.Write(buf[:n]); err != nil {
						fh.Close()
						return err
					}
					done += int64(n)
					if time.Since(last) > 400*time.Millisecond {
						last = time.Now()
						speed := ""
						if el := time.Since(t0).Seconds(); el > 1 {
							speed = fmt.Sprintf(" · %.1f МБ/с", float64(done)/1e6/el)
						}
						job.SetProgress(fmt.Sprintf("Отдаю аудио %s · файл %d из %d · %s из %s%s", f.FromNick, i+1, len(have), fmtBytes(done), fmtBytes(bytes), speed), int(done*100/max(bytes, 1)))
					}
				}
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					fh.Close()
					return rerr
				}
			}
			fh.Close()
		}
		return tw.Close()
	}
	title := fmt.Sprintf("Отдаю аудио «%s» → %s", filepath.Base(p.Dir), f.FromNick)
	s.startJobOpt(title, "", false, func(job *Job) (any, error) {
		var mu sync.Mutex
		mode := ""
		choose := func(m string) bool {
			mu.Lock()
			defer mu.Unlock()
			if mode == "" {
				mode = m
			}
			return mode == m
		}
		relayCtx, cancelRelay := context.WithCancel(job.Ctx())
		defer cancelRelay()
		claim := FetchClaim{Count: len(have), Bytes: bytes}
		o, _ := startDirect(size, func() bool {
			if !choose("direct") {
				return false
			}
			cancelRelay()
			return true
		}, func(w io.Writer) error { return write(job, w) })
		if o != nil {
			defer o.close()
			claim.Addrs, claim.Token = o.Addrs, o.Token
		}
		if err := h.ClaimFetch(f.ID, claim); err != nil {
			job.Logf("Уже отдаёт другой участник")
			return nil, nil
		}
		job.Logf("%s просит аудио проекта «%s»: у меня есть %d файлов, %s", f.FromNick, filepath.Base(p.Dir), len(have), fmtBytes(bytes))
		relayErr := make(chan error, 1)
		go func() {
			relayErr <- h.fetchRelaySend(relayCtx, f.ID, size, func() bool { return choose("relay") }, func(w io.Writer) error { return write(job, w) })
		}()
		var conn <-chan struct{}
		if o != nil {
			conn = o.conn
		}
		t0 := time.Now()
		select {
		case <-conn:
			select {
			case err := <-o.done:
				if err != nil {
					return nil, fmt.Errorf("передача по локальной сети оборвалась: %v", err)
				}
			case <-job.Ctx().Done():
				return nil, errCancelled
			}
			job.Logf("✓ Отдано по локальной сети: %s за %.0f с", fmtBytes(bytes), time.Since(t0).Seconds())
		case err := <-relayErr:
			mu.Lock()
			m := mode
			mu.Unlock()
			if m == "direct" {
				if err := <-o.done; err != nil {
					return nil, fmt.Errorf("передача по локальной сети оборвалась: %v", err)
				}
				job.Logf("✓ Отдано по локальной сети: %s за %.0f с", fmtBytes(bytes), time.Since(t0).Seconds())
			} else if err != nil {
				return nil, err
			} else {
				job.Logf("✓ Отдано через сервер: %s за %.0f с", fmtBytes(bytes), time.Since(t0).Seconds())
			}
		}
		return nil, nil
	})
}

func (h *Hub) fetchRelaySend(ctx context.Context, id string, size int64, first func() bool, write func(w io.Writer) error) error {
	pr, pw := io.Pipe()
	defer pr.Close()
	started := make(chan struct{})
	go func() {
		select {
		case <-started:
			pw.CloseWithError(write(pw))
		case <-ctx.Done():
			pw.CloseWithError(ctx.Err())
		}
	}()
	var once sync.Once
	body := &firstReader{r: pr, first: func() bool {
		if !first() {
			return false
		}
		once.Do(func() { close(started) })
		return true
	}}
	sctx, touch, stop, stalled := stallGuard(ctx, stallAfter)
	defer stop()
	req, err := http.NewRequestWithContext(sctx, "PUT", h.cfg.ServerURL()+"/api/fetches/"+url.PathEscape(id)+"/stream", &touchReader{body, touch})
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("X-Size", fmt.Sprint(size))
	req.Header.Set("Authorization", "Bearer "+h.cfg.DeviceToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Expect", "100-continue")
	resp, err := streamClient().Do(req)
	if err != nil {
		if stalled() {
			return errStalled
		}
		if ctx.Err() != nil {
			return errCancelled
		}
		return fmt.Errorf("передача прервалась: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 408 {
		return errNotOnline
	}
	if resp.StatusCode == 410 {
		return errCancelled
	}
	return readHubResp(resp, nil)
}

type touchReader struct {
	r     io.Reader
	touch func()
}

func (t *touchReader) Read(p []byte) (int, error) { n, err := t.r.Read(p); t.touch(); return n, err }
