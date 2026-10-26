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
	pLoadImageW          = user32.NewProc("LoadImageW")
	pSendMessageW        = user32.NewProc("SendMessageW")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pGetModuleHandleW    = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

// windowIcon ставит окну (и кнопке на панели задач) набор иконок id из exe.
func windowIcon(hwnd uintptr, id int) {
	mod, _, _ := pGetModuleHandleW.Call(0)
	for _, v := range [][3]uintptr{{1, 11, 12}, {0, 49, 50}} { // ICON_BIG: SM_CXICON/SM_CYICON; ICON_SMALL: SM_CXSMICON/SM_CYSMICON
		cx, _, _ := pGetSystemMetrics.Call(v[1])
		cy, _, _ := pGetSystemMetrics.Call(v[2])
		if h, _, _ := pLoadImageW.Call(mod, uintptr(id), 1, cx, cy, 0); h != 0 {
			pSendMessageW.Call(hwnd, 0x0080, v[0], h) // WM_SETICON
		}
	}
}

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
			Title: o.Title, Width: uint(o.Width), Height: uint(o.Height), IconId: uint(iconOr(o.IconID)), Center: true,
		},
	})
	if w == nil {
		return errors.New("WebView2 недоступен")
	}
	defer w.Destroy()
	w.SetSize(900, 600, webview2.HintMin)
	w.Navigate(o.URL)
	Handle = func() uintptr { return uintptr(w.Window()) }
	setIcon = func(id int, _ []byte, _ bool) {
		w.Dispatch(func() { windowIcon(uintptr(w.Window()), iconOr(id)) })
	}
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

func iconOr(id int) int {
	if id <= 0 {
		return 2
	}
	return id
}
