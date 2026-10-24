package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ---------- прямая передача по локальной сети ----------
//
// Если два компьютера в одной сети (дома, в студии), передавать через сервер в Финляндии — терять скорость:
// данные идут туда и обратно через интернет. Отправитель открывает у себя временный порт и сообщает через
// сервер свои адреса в локальной сети и одноразовый ключ. Получатель пробует подключиться; получилось —
// данные идут напрямую на скорости сети, нет (разные сети, брандмауэр) — как раньше, через сервер.

// lanAddrs — IPv4-адреса этого компьютера в локальных сетях.
func lanAddrs() []string {
	if testLAN != nil {
		return testLAN
	}
	var out []string
	ifs, _ := net.Interfaces()
	for _, it := range ifs {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, _ := it.Addrs()
		for _, a := range as {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}

func randToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// directOffer — открытый порт, который отдаст данные один раз тому, кто придёт с ключом.
type directOffer struct {
	Addrs []string
	Token string
	ln    net.Listener
	srv   *http.Server
	mu    sync.Mutex
	taken bool
	conn  chan struct{} // закрывается, когда получатель подключился
	done  chan error    // итог отдачи
}

// startDirect открывает порт. body пишет данные (size байт) в w.
func startDirect(size int64, accept func() bool, body func(w io.Writer) error) (*directOffer, error) {
	return startDirectFrom(size, "", accept, func(w io.Writer, _ int64) error { return body(w) })
}

// startDirectFrom — то же с докачкой: если у получателя уже есть начало файла fileID, body пишет с байта off.
func startDirectFrom(size int64, fileID string, accept func() bool, body func(w io.Writer, off int64) error) (*directOffer, error) {
	ips := lanAddrs()
	if len(ips) == 0 || directOff.Load() {
		return nil, errors.New("нет локальной сети")
	}
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	o := &directOffer{Token: randToken(), ln: ln, conn: make(chan struct{}), done: make(chan error, 1)}
	for _, ip := range ips {
		o.Addrs = append(o.Addrs, net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/fh/"+o.Token, func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		if o.taken {
			o.mu.Unlock()
			http.Error(w, "уже отдано", 410)
			return
		}
		if accept != nil && !accept() { // уже идёт через сервер
			o.mu.Unlock()
			http.Error(w, "уже передаётся через сервер", 410)
			return
		}
		o.taken = true
		o.mu.Unlock()
		close(o.conn)
		var off int64
		if have, _ := strconv.ParseInt(r.Header.Get("X-Have-Bytes"), 10, 64); fileID != "" && r.Header.Get("X-Have-File") == fileID && have > 0 && have < size {
			off = have
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if fileID != "" {
			w.Header().Set("X-File-Id", fileID)
			w.Header().Set("X-Offset", strconv.FormatInt(off, 10))
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size-off, 10))
		w.WriteHeader(200)
		o.done <- body(w, off)
	})
	o.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go o.srv.Serve(ln)
	return o, nil
}

// wait ждёт получателя до timeout. connected=false — не пришёл (порт закрыт, можно через сервер).
func (o *directOffer) wait(ctx context.Context, timeout time.Duration) (connected bool, err error) {
	defer o.close()
	select {
	case <-o.conn:
	case <-time.After(timeout):
		o.mu.Lock()
		taken := o.taken
		o.taken = true // поздно — больше не отдаём
		o.mu.Unlock()
		if !taken {
			return false, nil
		}
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case err = <-o.done:
		return true, err
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

func (o *directOffer) close() {
	if o.srv != nil {
		o.srv.Close()
	}
}

// directGet пробует подключиться по всем адресам сразу; первый ответивший — источник.
func directGet(ctx context.Context, addrs []string, token string) (*http.Response, error) {
	return directGetH(ctx, addrs, token, nil)
}

// directGetH — то же с заголовками запроса (докачка: что уже есть у получателя).
func directGetH(ctx context.Context, addrs []string, token string, hdr http.Header) (*http.Response, error) {
	if len(addrs) == 0 || token == "" || directOff.Load() {
		return nil, errors.New("нет адресов")
	}
	cl := &http.Client{Transport: &http.Transport{
		Proxy:                 nil, // только локальная сеть, без прокси
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		ReadBufferSize:        1 << 20,
		DisableCompression:    true,
	}}
	type res struct {
		r   *http.Response
		err error
	}
	cctx, cancel := context.WithCancel(ctx)
	ch := make(chan res, len(addrs))
	for _, a := range addrs {
		go func(a string) {
			req, _ := http.NewRequestWithContext(cctx, "GET", "http://"+a+"/fh/"+token, nil)
			for k, v := range hdr {
				req.Header[k] = v
			}
			r, err := cl.Do(req)
			if err == nil && r.StatusCode != 200 {
				r.Body.Close()
				err = fmt.Errorf("%s: %s", a, r.Status)
				r = nil
			}
			ch <- res{r, err}
		}(a)
	}
	var lastErr error
	for range addrs {
		x := <-ch
		if x.err == nil {
			// остальные попытки пусть закроются сами (тот же ключ второй раз не отдаётся)
			go func(n int) {
				for i := 0; i < n; i++ {
					if y := <-ch; y.r != nil {
						y.r.Body.Close()
					}
				}
			}(len(addrs) - 1)
			x.r.Body = &cancelBody{x.r.Body, cancel}
			return x.r, nil
		}
		lastErr = x.err
	}
	cancel()
	return nil, lastErr
}

type cancelBody struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelBody) Close() error { err := c.ReadCloser.Close(); c.cancel(); return err }

// testLAN — адреса «локальной сети» в тестах (127.0.0.1).
var testLAN []string

// directOff — выключено пользователем (Настройки) или в тестах.
var directOff = &atomicBool{}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) Load() bool   { a.mu.Lock(); defer a.mu.Unlock(); return a.v }
func (a *atomicBool) Store(v bool) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
