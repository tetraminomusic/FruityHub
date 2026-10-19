// devstack: fake GitHub + our server on localhost, for trying the app
// without real GitHub (developer tool). GitHub tokens: tokA (alogin), tokB (blogin).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"flsync/internal/fakegh"
	"flsync/internal/hub"
)

func main() {
	root := flag.String("root", "devstack-data", "data folder")
	addr := flag.String("addr", "127.0.0.1:47900", "server address")
	flag.Parse()
	gh := fakegh.New(*root)
	gh.AddUser("tokA", "alogin")
	gh.AddUser("tokB", "blogin")
	s, err := hub.New(hub.Options{DataDir: filepath.Join(*root, "hub"), GitHubAPI: gh.Srv.URL, Code: "uzbek"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("GITHUB_API=" + gh.Srv.URL)
	fmt.Println("SERVER=http://" + *addr)
	log.Fatal(http.ListenAndServe(*addr, s.Handler()))
}
