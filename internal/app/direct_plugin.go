package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// directInfo — что отправитель публикует на сервере для прямого подключения.
type directInfo struct {
	Addrs []string `json:"addrs"`
	Token string   `json:"token"`
	Sha   string   `json:"sha256,omitempty"`
	Size  int64    `json:"size,omitempty"`
}

func fileSha256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// firstReader спрашивает разрешения перед первым куском данных.
type firstReader struct {
	r     io.Reader
	first func() bool
	asked bool
}

func (f *firstReader) Read(p []byte) (int, error) {
	if !f.asked {
		f.asked = true
		if !f.first() {
			return 0, errViaLAN
		}
	}
	return f.r.Read(p)
}

var errViaLAN = errors.New("передаётся по локальной сети")

// StreamSend отдаёт файл плагина. Сразу два пути: открытый порт в локальной сети (адрес — через сервер)
// и обычный поток через сервер. Каким путём придёт получатель, тем и передаём, второй закрываем.
func (h *Hub) StreamSend(job *Job, id, path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
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
	info := directInfo{}
	var o *directOffer
	if sha, err := fileSha256(path); err == nil {
		o, _ = startDirectFrom(st.Size(), packedID(path), func() bool {
			if !choose("direct") {
				return false
			}
			cancelRelay() // через сервер уже не нужно
			return true
		}, func(w io.Writer, off int64) error {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if off > 0 {
				if _, err := f.Seek(off, io.SeekStart); err != nil {
					return err
				}
				job.Logf("Докачиваю по локальной сети с %s", fmtBytes(off))
			}
			_, err = io.CopyBuffer(w, &progressReader{r: f, total: st.Size(), done: off, job: job, verb: "Передаю по локальной сети"}, make([]byte, 1<<20))
			return err
		})
		if o != nil {
			info = directInfo{Addrs: o.Addrs, Token: o.Token, Sha: sha, Size: st.Size()}
		}
	}
	// получатель узнаёт, ждать ли прямого подключения (пустой список — сразу через сервер)
	if err := h.do("PUT", "/api/plugin-requests/"+url.PathEscape(id)+"/direct", info, nil); err != nil && o != nil {
		o.close()
		o = nil
	}
	if o == nil {
		return h.relaySend(job, id, path)
	}
	defer o.close()
	t0 := time.Now()
	relayErr := make(chan error, 1)
	go func() { relayErr <- h.relaySendCtx(relayCtx, job, id, path, func() bool { return choose("relay") }) }()
	select {
	case <-o.conn: // получатель пришёл напрямую
	case err := <-relayErr:
		mu.Lock()
		m := mode
		mu.Unlock()
		if m != "direct" {
			return err // через сервер (или не дождались получателя)
		}
		<-o.conn
	}
	select {
	case err = <-o.done:
	case <-job.Ctx().Done():
		return errCancelled
	}
	if err != nil {
		if job.Ctx().Err() != nil {
			return errCancelled
		}
		return fmt.Errorf("передача по локальной сети оборвалась: %v", err)
	}
	el := time.Since(t0).Seconds()
	job.Logf("Передано напрямую по локальной сети: %.1f МБ (%.1f МБ/с)", float64(st.Size())/1e6, float64(st.Size())/1e6/max(el, 0.001))
	DevLog("info", 0, false, "плагин передан по локальной сети: %.1f МБ за %.0f с", float64(st.Size())/1e6, el)
	return nil
}

// StreamRecv получает файл плагина: напрямую по локальной сети, если отправитель это предложил и до него
// можно достучаться, иначе — через сервер.
func (h *Hub) StreamRecv(job *Job, r *PluginRequest, dst string) error {
	var d directInfo
	// отправитель новой версии сразу пишет, можно ли напрямую; старая версия молчит — тогда через сервер
	if err := h.do("GET", "/api/plugin-requests/"+url.PathEscape(r.ID)+"/direct?wait=12", nil, &d); err == nil && len(d.Addrs) > 0 {
		job.SetProgress("Пробую получить напрямую по локальной сети…", -1)
		if err := directRecvFile(job, d, dst); err == nil {
			return nil
		} else if job.Ctx().Err() != nil {
			return errCancelled
		} else {
			DevLog("info", 0, false, "напрямую не получилось (%v) — через сервер", err)
		}
	}
	return h.relayRecv(job, r, dst)
}

func directRecvFile(job *Job, d directInfo, dst string) error {
	hdr := http.Header{}
	resumeHeaders(hdr.Set, dst)
	resp, err := directGetH(job.Ctx(), d.Addrs, d.Token, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	ctx, touch, stop, stalled := stallGuard(job.Ctx(), stallAfter)
	defer stop()
	go func() { <-ctx.Done(); resp.Body.Close() }()
	t0 := time.Now()
	if err := receiveInto(job, resp.Body, resp.Header.Get("X-File-Id"), resp.Header.Get("X-Offset"), resp.ContentLength, dst, d.Sha, "Получаю по локальной сети", touch, stalled); err != nil {
		return err
	}
	n := d.Size
	el := time.Since(t0).Seconds()
	job.Logf("Получено напрямую по локальной сети: %.1f МБ (%.1f МБ/с)", float64(n)/1e6, float64(n)/1e6/max(el, 0.001))
	return nil
}
