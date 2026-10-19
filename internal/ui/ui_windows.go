//go:build windows

package ui

import (
	"errors"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

func Run(o Options) error {
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
