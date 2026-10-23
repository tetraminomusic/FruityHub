package app

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Прокси для GitHub. Порядок: настройка в FruityHub → переменные окружения → системный прокси Windows
// (то, что включает VPN-клиент вроде v2rayN в режиме «системный прокси»). Go сам системный прокси
// Windows не видит, поэтому без этого запросы к GitHub идут напрямую и на закрытых сетях виснут.
// Весь остальной трафик (наш сервер, локальные адреса) идёт как раньше.

var userProxy atomic.Value // string

func SetUserProxy(p string) { userProxy.Store(strings.TrimSpace(p)) }

func userProxyString() string {
	s, _ := userProxy.Load().(string)
	return s
}

func isGitHubHost(h string) bool {
	h = strings.ToLower(h)
	for _, d := range []string{"github.com", "githubusercontent.com", "githubassets.com"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

func parseProxy(s string) *url.URL {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

// GitHubProxy — прокси для запросов к GitHub (nil = напрямую) и откуда он взялся.
// Прокси из настроек FruityHub используется всегда. Иначе FruityHub САМ проверяет: если GitHub открывается
// напрямую (например, VPN работает на уровне всей системы) — идёт напрямую; если нет — пробует прокси
// из переменных окружения и системный прокси Windows и берёт тот, что отвечает.
func GitHubProxy() (*url.URL, string) {
	if u := parseProxy(userProxyString()); u != nil {
		return u, "настройки FruityHub"
	}
	return autoProxy()
}

type autoState struct {
	u   *url.URL
	src string
	at  time.Time
	ok  bool
}

var (
	autoMu sync.Mutex
	auto   autoState
)

func candidateProxies() (us []*url.URL, srcs []string) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy"} {
		if u := parseProxy(os.Getenv(k)); u != nil {
			us, srcs = append(us, u), append(srcs, "переменная "+k)
		}
	}
	if u, _, _ := systemProxy(); u != nil {
		us, srcs = append(us, u), append(srcs, "системный прокси Windows")
	}
	return
}

// reachGitHub — отвечает ли api.github.com (любой HTTP-ответ годится) за ~7 секунд, напрямую или через прокси p.
func reachGitHub(p *url.URL) bool {
	t := &http.Transport{TLSHandshakeTimeout: 7 * time.Second, ResponseHeaderTimeout: 7 * time.Second,
		Proxy: func(*http.Request) (*url.URL, error) { return p, nil }}
	defer t.CloseIdleConnections()
	cl := &http.Client{Transport: t, Timeout: 8 * time.Second}
	resp, err := cl.Get("https://api.github.com/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func autoProxy() (*url.URL, string) {
	autoMu.Lock()
	defer autoMu.Unlock()
	ttl := 2 * time.Minute
	if !auto.ok {
		ttl = 30 * time.Second
	}
	if !auto.at.IsZero() && time.Since(auto.at) < ttl {
		return auto.u, auto.src
	}
	cands, srcs := candidateProxies()
	auto = autoState{at: time.Now()}
	if len(cands) == 0 || reachGitHub(nil) {
		auto.ok = true
		return nil, ""
	}
	for i, c := range cands {
		if reachGitHub(c) {
			auto = autoState{u: c, src: srcs[i], at: time.Now(), ok: true}
			return c, srcs[i]
		}
	}
	return nil, ""
}

// SystemProxyHint — адрес прокси, прописанный в системе, даже если он сейчас выключен (для подсказки в настройках).
func SystemProxyHint() string {
	if u, raw, _ := systemProxy(); u != nil {
		return raw
	}
	return ""
}

func init() {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSHandshakeTimeout = 30 * time.Second
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		if isGitHubHost(r.URL.Hostname()) {
			u, _ := GitHubProxy()
			return u, nil
		}
		return http.ProxyFromEnvironment(r)
	}
	http.DefaultTransport = loggingTransport{t}
	baseTransport = t
}

var (
	baseTransport *http.Transport
	streamOnce    sync.Once
	streamCl      *http.Client
)

// streamClient — для передачи плагинов: HTTP/1.1 (у HTTP/2 маленькое окно на загрузку — на дальнем сервере
// скорость упиралась в него) и большие буферы.
func streamClient() *http.Client {
	streamOnce.Do(func() {
		b := baseTransport
		if b == nil {
			b = http.DefaultTransport.(*http.Transport)
		}
		streamCl = &http.Client{Transport: loggingTransport{newStreamTransport(b)}}
	})
	return streamCl
}

func newStreamTransport(base *http.Transport) *http.Transport {
	t := base.Clone()
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	// Важно: после первого запроса общий транспорт прописывает в TLS «h2» — клон его унаследует,
	// сервер согласится на HTTP/2, а мы ждём HTTP/1.1 («malformed HTTP response \x00\x00…»). Явно просим только HTTP/1.1.
	if t.TLSClientConfig != nil {
		t.TLSClientConfig = t.TLSClientConfig.Clone()
	} else {
		t.TLSClientConfig = &tls.Config{}
	}
	t.TLSClientConfig.NextProtos = []string{"http/1.1"}
	t.ExpectContinueTimeout = 3 * time.Minute // тело пойдёт, только когда получатель подключился (сервер ответит 100 Continue)
	t.ResponseHeaderTimeout = 4 * time.Minute // сервер ждёт вторую сторону не больше 2 минут — дольше значит, связь пропала
	t.WriteBufferSize = 512 << 10
	t.ReadBufferSize = 512 << 10
	return t
}
