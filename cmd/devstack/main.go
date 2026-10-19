// devstack: fake GitHub + our server on localhost, for trying the app
// without real GitHub (developer tool).
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"flsync/internal/fakegh"
	"flsync/internal/hub"
)

func main() {
	root := flag.String("root", "devstack-data", "data folder")
	addr := flag.String("addr", "127.0.0.1:47900", "server address")
	flag.Parse()
	gh := fakegh.New(*root)
	data := filepath.Join(*root, "hub")
	os.MkdirAll(data, 0o700)
	if _, err := os.Stat(filepath.Join(data, "github-app.json")); err != nil {
		key, _ := rsa.GenerateKey(rand.Reader, 2048)
		pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		b, _ := json.Marshal(map[string]any{"appId": 1, "slug": "dev", "org": gh.Org, "pem": string(pemKey)})
		os.WriteFile(filepath.Join(data, "github-app.json"), b, 0o600)
	}
	s, err := hub.New(hub.Options{DataDir: data, PublicURL: "http://" + *addr, GitHubAPI: gh.Srv.URL})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("GITHUB_API=" + gh.Srv.URL)
	fmt.Println("SERVER=http://" + *addr)
	log.Fatal(http.ListenAndServe(*addr, s.Handler()))
}
