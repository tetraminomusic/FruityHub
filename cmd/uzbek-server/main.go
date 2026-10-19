// uzbek-server — сервер «проги узбека»: пользователи, проекты, приглашения,
// передача плагинов. Сами проекты лежат на GitHub (в организации), сервер
// только раздаёт к ним временные ключи.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"flsync/internal/hub"

	"golang.org/x/crypto/acme/autocert"
)

func main() {
	data := flag.String("data", "/var/lib/uzbek-server", "папка с данными сервера")
	addr := flag.String("addr", ":8080", "адрес для HTTP (если без -domain)")
	domain := flag.String("domain", "", "домен для автоматического HTTPS (Let's Encrypt), напр. uzbek.example.com")
	public := flag.String("public", "", "внешний адрес сервера (по умолчанию https://<domain> или http://<addr>)")
	cert := flag.String("cert", "", "свой сертификат (вместо Let's Encrypt)")
	key := flag.String("key", "", "ключ к сертификату")
	maxFile := flag.Int64("max-file-mb", 1500, "максимальный размер одного плагина/установщика, МБ")
	maxTotal := flag.Int64("max-total-mb", 5000, "сколько места на диске могут занимать передаваемые плагины, МБ")
	ttl := flag.Duration("file-ttl", 72*time.Hour, "сколько файл ждёт получателя")
	flag.Parse()

	pub := *public
	if pub == "" {
		if *domain != "" {
			pub = "https://" + *domain
		} else {
			h := *addr
			if strings.HasPrefix(h, ":") {
				h = "localhost" + h
			}
			pub = "http://" + h
		}
	}
	s, err := hub.New(hub.Options{DataDir: *data, PublicURL: strings.TrimRight(pub, "/"),
		MaxFile: *maxFile << 20, MaxTotal: *maxTotal << 20, FileTTL: *ttl})
	if err != nil {
		log.Fatal(err)
	}
	go s.Janitor()
	h := logRequests(s.Handler())
	if !s.GitHubApp().Ready() {
		fmt.Printf("\nСервер ещё не подключён к GitHub. Открой в браузере:\n  %s/setup?key=%s\n\n", pub, s.AdminKey())
	} else {
		fmt.Printf("Сервер подключён к GitHub (организация %s). Настройка: %s/setup?key=%s\n", s.GitHubApp().Org, pub, s.AdminKey())
	}

	switch {
	case *domain != "":
		m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(*domain), Cache: autocert.DirCache(*data + "/certs")}
		go func() { log.Fatal(http.ListenAndServe(":80", m.HTTPHandler(nil))) }()
		srv := &http.Server{Addr: ":443", Handler: h, TLSConfig: &tls.Config{GetCertificate: m.GetCertificate, MinVersion: tls.VersionTLS12},
			ReadHeaderTimeout: 20 * time.Second}
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
