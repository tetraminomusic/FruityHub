// uzbek-server — сервер «проги узбека»: пользователи, проекты и участники,
// приглашения, передача плагинов. Сами проекты лежат на GitHub у владельцев;
// плагины идут потоком с компьютера на компьютер и на сервере не сохраняются.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"flsync/internal/hub"

	"golang.org/x/crypto/acme/autocert"
)

func main() {
	data := flag.String("data", "/var/lib/uzbek-server", "папка с данными сервера")
	addr := flag.String("addr", ":8080", "адрес для HTTP (если без -domain)")
	domain := flag.String("domain", "", "домен для автоматического HTTPS (Let's Encrypt), напр. uzbek.example.com")
	cert := flag.String("cert", "", "свой сертификат (вместо Let's Encrypt)")
	key := flag.String("key", "", "ключ к сертификату")
	code := flag.String("code", "", "код для регистрации (по умолчанию — из файла server-code в папке данных)")
	flag.Parse()

	s, err := hub.New(hub.Options{DataDir: *data, Code: *code})
	if err != nil {
		log.Fatal(err)
	}
	go s.Janitor()
	h := logRequests(s.Handler())
	fmt.Printf("Код сервера для регистрации: %s\n", s.Code())

	switch {
	case *domain != "":
		m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(*domain), Cache: autocert.DirCache(*data + "/certs")}
		go func() { // port 80 is only for certificate checks and redirects; 443 can do without it
			if err := http.ListenAndServe(":80", m.HTTPHandler(nil)); err != nil {
				log.Printf("порт 80 недоступен (%v) — сертификат будет получен через порт 443", err)
			}
		}()
		tc := m.TLSConfig()
		tc.MinVersion = tls.VersionTLS12
		srv := &http.Server{Addr: ":443", Handler: h, TLSConfig: tc, ReadHeaderTimeout: 20 * time.Second}
		log.Printf("слушаю https://%s", *domain)
		log.Fatal(srv.ListenAndServeTLS("", ""))
	case *cert != "":
		log.Printf("слушаю %s (TLS)", *addr)
		log.Fatal((&http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 20 * time.Second}).ListenAndServeTLS(*cert, *key))
	default:
		log.Printf("слушаю %s", *addr)
		log.Fatal((&http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 20 * time.Second}).ListenAndServe())
	}
}

func logRequests(h http.Handler) http.Handler {
	quiet := os.Getenv("UZBEK_QUIET") != ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !quiet && r.URL.Path != "/api/inbox" && r.URL.Path != "/api/ping" {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}
