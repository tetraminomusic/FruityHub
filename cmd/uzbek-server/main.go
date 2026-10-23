// uzbek-server — сервер FruityHub: пользователи, проекты и участники,
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
	httpsPort := flag.Int("https-port", 443, "порт HTTPS вместе с -domain (если 443 занят, например VPN; тогда для сертификата нужен свободный порт 80)")
	code := flag.String("code", "", "код для регистрации (по умолчанию — из файла server-code в папке данных)")
	restore := flag.String("restore", "", "восстановить данные из бэкапа (файл .json из админки или server-up.sh backup) и выйти")
	flag.Parse()

	if *restore != "" {
		if err := hub.RestoreBackup(*data, *restore); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Бэкап восстановлен в", *data)
		return
	}

	s, err := hub.New(hub.Options{DataDir: *data, Code: *code})
	if err != nil {
		log.Fatal(err)
	}
	go s.Janitor()
	log.SetOutput(s.LogWriter(os.Stderr)) // всё, что пишет сервер, видно и в админке
	s.Logf("info", "server", "сервер запущен")
	h := logRequests(s.Handler())
	fmt.Printf("Код сервера для регистрации: %s\n", s.Code())
	fmt.Printf("Ключ администратора (режим разработчика в приложении): %s\n", s.AdminKey())

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
		srv := &http.Server{Addr: fmt.Sprintf(":%d", *httpsPort), Handler: h, TLSConfig: tc, ReadHeaderTimeout: 20 * time.Second}
		log.Printf("слушаю https://%s:%d", *domain, *httpsPort)
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
			fmt.Fprintf(os.Stderr, "%s %s %s\n", time.Now().Format("2006/01/02 15:04:05"), r.Method, r.URL.Path) // только в системный журнал, не в админку
		}
		h.ServeHTTP(w, r)
	})
}
