package app

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Запросы к GitHub идут через прокси из настроек, остальные — мимо него.
func TestGitHubProxyRouting(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 512)
			n, _ := c.Read(buf)
			got <- string(buf[:n])
			c.Close()
		}
	}()
	SetUserProxy(ln.Addr().String())
	defer SetUserProxy("")
	cl := &http.Client{Timeout: 3 * time.Second}
	cl.Get("https://api.github.com/")
	select {
	case s := <-got:
		if len(s) < 7 || s[:7] != "CONNECT" || !contains(s, "api.github.com:443") {
			t.Fatalf("неожиданный запрос к прокси: %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("запрос к GitHub не пошёл через прокси")
	}
	if u, _ := GitHubProxy(); u == nil || u.Host != ln.Addr().String() {
		t.Fatalf("GitHubProxy = %v", u)
	}
	// не-GitHub — мимо прокси
	cl.Get("http://127.0.0.1:1/")
	select {
	case s := <-got:
		t.Fatalf("локальный запрос ушёл в прокси: %q", s)
	case <-time.After(300 * time.Millisecond):
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Передача плагинов идёт по HTTP/1.1, даже если общий транспорт уже ходил к серверу по HTTP/2
// (иначе: «malformed HTTP response \x00\x00\x1e\x04…»).
func TestStreamTransportHTTP1(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(r.Proto))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	base := srv.Client().Transport.(*http.Transport).Clone()
	base.ForceAttemptHTTP2 = true
	resp, err := (&http.Client{Transport: base}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "HTTP/2.0" {
		t.Fatalf("общий транспорт должен был пойти по HTTP/2: %s", b)
	}
	cl := &http.Client{Transport: newStreamTransport(base)}
	req, _ := http.NewRequest("PUT", srv.URL, strings.NewReader("payload"))
	req.Header.Set("Expect", "100-continue")
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "HTTP/1.1" {
		t.Fatalf("передача должна идти по HTTP/1.1: %s", b)
	}
}
