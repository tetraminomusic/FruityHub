//go:build windows

package ui

import (
	"errors"
	"os"
	"strings"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

// webviewArgs — ключи для движка WebView2 (Edge): не запускать службы, которые окну приложения не нужны
// (фоновая сеть, обновление компонентов, SmartScreen, синхронизация), один процесс отрисовки.
// В лёгком режиме — ещё и без процесса видеокарты: интерфейс без размытия рисуется и так быстро.
func webviewArgs(lite bool) string {
	a := []string{"--disable-background-networking", "--disable-component-update", "--disable-sync", "--disable-default-apps",
		"--disable-breakpad", "--no-pings", "--renderer-process-limit=1",
		"--disable-features=msSmartScreenProtection,msWebOOUI,msPdfOOUI,msEdgeCollections,msShoppingExp,Translate,AutofillServerCommunication"}
	if lite {
		a = append(a, "--disable-gpu", "--disable-gpu-compositing")
	}
	return strings.Join(a, " ")
}

func Run(o Options) error {
	args := webviewArgs(o.Lite)
	if prev := os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"); prev != "" {
		args = prev + " " + args
	}
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", args)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  o.DataPath,
		WindowOptions: webview2.WindowOptions{
			Title: o.Title, Width: uint(o.Width), Height: uint(o.Height), IconId: 2, Center: true,
		},
	})
	if w == nil {
		return errors.New("WebView2 недоступен")
	}
	defer w.Destroy()
	w.SetSize(900, 600, webview2.HintMin)
	w.Navigate(o.URL)
	Handle = func() uintptr { return uintptr(w.Window()) }
	focus = func() {
		w.Dispatch(func() {
			h := uintptr(w.Window())
			pShowWindow.Call(h, 9) // SW_RESTORE
			pSetForegroundWindow.Call(h)
		})
	}
	if o.Quit != nil {
		go func() {
			<-o.Quit
			w.Dispatch(w.Terminate)
		}()
	}
	w.Run()
	return nil
}
