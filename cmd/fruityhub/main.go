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
	// установка на Windows: --after-install — первый запуск установленного (старый ещё закрывается),
	// --remove-old <путь> — убрать файл, с которого ставили; --uninstall — удаление из «Приложений»
	app.UninstallMode = hasArg("--uninstall")
	afterInstall := hasArg("--after-install")
	removeOld := argValue("--remove-old")

	cl := &http.Client{Timeout: time.Second}
	running := func() bool {
		resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", preferredPort))
		if err != nil {
			return false
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b) == "uzbek"
	}
	if afterInstall || app.UninstallMode {
		if app.UninstallMode && running() { // удалить можно, только когда FruityHub закрыт — просим открытый закрыться
			req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/bye", preferredPort), nil)
			req.Header.Set("X-FruityHub-Uninstall", "1")
			if resp, err := cl.Do(req); err == nil {
				resp.Body.Close()
			}
		}
		for i := 0; i < 75 && running(); i++ { // ждём, пока прежний FruityHub закроется
			time.Sleep(200 * time.Millisecond)
		}
	} else if running() { // уже открыт — показать его окно
		cl.Get(fmt.Sprintf("http://127.0.0.1:%d/focus", preferredPort))
		return
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
	go app.RefreshInstallEntry()
	if removeOld != "" {
		go app.RemoveOldExe(removeOld)
	}
	app.OnFocus = ui.Focus
	app.PickerOwner = func() uintptr { return ui.Handle() }

	if !browserMode {
		srv.Windowed = true
		style := srv.IconStyle()
		var dock []byte
		if style == app.IconTile {
			dock = app.AppIconPNG(web.FS, style)
		}
		app.OnIconChange = func(st string) {
			ui.SetIcon(app.IconGroup(st), app.AppIconPNG(web.FS, st), st == app.IconTile)
		}
		err := ui.Run(ui.Options{URL: url, Title: app.AppName, Width: 1240, Height: 820,
			DataPath: filepath.Join(app.ConfigDir(), "webview"), Quit: srv.Quit(), Lite: srv.LiteUI(),
			IconID: app.IconGroup(style), IconPNG: dock, Bundle: app.AppBundle()})
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

func hasArg(a string) bool {
	for _, x := range os.Args[1:] {
		if x == a {
			return true
		}
	}
	return false
}

func argValue(a string) string {
	for i, x := range os.Args[1:] {
		if x == a && i+2 < len(os.Args) {
			return os.Args[i+2]
		}
	}
	return ""
}
