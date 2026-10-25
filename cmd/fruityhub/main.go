// FruityHub — совместная работа над проектами FL Studio: проекты на GitHub,
// пользователи, приглашения и передача плагинов — через свой сервер.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flsync/internal/app"
	"flsync/internal/ui"
	"flsync/web"
)

var version = "dev"

var preferredPort = 47811

func main() {
	// git calls us back as GIT_ASKPASS to get the GitHub token
	if os.Getenv("FLSYNC_ASKPASS") == "1" {
		prompt := strings.ToLower(strings.Join(os.Args[1:], " "))
		if strings.HasPrefix(prompt, "username") {
			fmt.Println("x-access-token")
		} else {
			fmt.Println(os.Getenv("FLSYNC_TOKEN"))
		}
		return
	}
	// пробная загрузка плагина в отдельном процессе («Проверить плагин»)
	if p := os.Getenv("FRUITYHUB_PROBE"); p != "" {
		app.RunProbe(p)
		return
	}
	app.Version = version
	if v, err := strconv.Atoi(os.Getenv("UZBEK_PORT")); err == nil { // for running two copies while testing
		preferredPort = v
	}
	poll := 45 * time.Second
	if d, err := time.ParseDuration(os.Getenv("UZBEK_POLL")); err == nil {
		poll = d
	}
	browserMode := len(os.Args) > 1 && (os.Args[1] == "--browser" || os.Args[1] == "--no-browser")

	// already running? bring that window to the front
	cl := &http.Client{Timeout: time.Second}
	if resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", preferredPort)); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) == "uzbek" {
			cl.Get(fmt.Sprintf("http://127.0.0.1:%d/focus", preferredPort))
			return
		}
	}

	l, port, err := app.Listen(preferredPort)
	if err != nil {
		fmt.Println("не удалось запустить:", err)
		os.Exit(1)
	}
	srv := app.NewServer(web.Index)
	srv.Assets = web.FS
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	fmt.Println(app.AppName, version, "→", url)
	go http.Serve(l, srv.Handler(port))
	go srv.Poller(poll)
	go srv.HubLoop(8 * time.Second)
	go srv.MetaWatcher(6 * time.Second)
	go srv.RelocateWatcher(time.Minute)
	app.OnFocus = ui.Focus
	app.PickerOwner = func() uintptr { return ui.Handle() }

	if !browserMode {
		srv.Windowed = true
		err := ui.Run(ui.Options{URL: url, Title: app.AppName, Width: 1240, Height: 820,
			DataPath: filepath.Join(app.ConfigDir(), "webview"), Quit: srv.Quit(), Lite: srv.LiteUI()})
		if err == nil {
			return // window closed → exit
		}
		fmt.Println("окно недоступно, открываю в браузере:", err)
		srv.Windowed = false
	}
	if len(os.Args) < 2 || os.Args[1] != "--no-browser" {
		app.OpenURL(url)
		go srv.IdleWatcher(3 * time.Minute)
	}
	<-srv.Quit()
}
