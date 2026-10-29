//go:build windows

package ui

import (
	"runtime"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

type trayIconData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	BalloonIcon         uintptr
}

// Крестик и Alt+F4 доходят до одного WM_CLOSE. Перехватываем клавиатурное
// закрытие до этого, включая путь через системную команду WebView2.
func altF4Close(msg, key, flags uintptr, alt, f4 bool) bool {
	switch msg {
	case 0x104: // WM_SYSKEYDOWN: контекст Alt в бите 29
		return key == 0x73 && flags&(1<<29) != 0
	case 0x112: // WM_SYSCOMMAND / SC_CLOSE
		return key&0xfff0 == 0xf060 && alt && f4
	case 0x10: // WM_CLOSE, если дочернее окно переслало его напрямую
		return alt && f4
	}
	return false
}

// Закрытие окна скрывает его, сервер и передачи продолжают работать.
// При недоступном трее оставляем обычное закрытие: невидимого процесса не будет.
func installTray(w webview2.WebView, url string, iconID int) (func(), func()) {
	hwnd := uintptr(w.Window())
	shell := syscall.NewLazyDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	comctl := syscall.NewLazyDLL("comctl32.dll")
	setSubclass := comctl.NewProc("SetWindowSubclass")
	removeSubclass := comctl.NewProc("RemoveWindowSubclass")
	defSubclass := comctl.NewProc("DefSubclassProc")
	show := func() {
		pShowWindow.Call(hwnd, 9)
		pSetForegroundWindow.Call(hwnd)
		w.Eval("window.FH_TRAY=false; if(typeof pollNotes==='function')pollNotes();")
	}
	mod, _, _ := pGetModuleHandleW.Call(0)
	icon, _, _ := pLoadImageW.Call(mod, uintptr(iconID), 1, 16, 16, 0x8000)
	data := trayIconData{Window: hwnd, ID: 1, Flags: 7, Callback: 0x8000 + 73, Icon: icon}
	data.Size = uint32(unsafe.Sizeof(data))
	copy(data.Tip[:], syscall.StringToUTF16("FruityHub — работает в фоне"))
	add := func() bool { r, _, _ := shell.Call(0, uintptr(unsafe.Pointer(&data))); return r != 0 }
	active := add()
	if !active {
		return func() {}, show
	}
	name, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	restart, _, _ := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(name)))
	callback := syscall.NewCallback(func(h, msg, wp, lp, id, ref uintptr) uintptr {
		if msg == 0x104 || msg == 0x112 || msg == 0x10 {
			alt, _, _ := user32.NewProc("GetKeyState").Call(0x12)
			f4, _, _ := user32.NewProc("GetKeyState").Call(0x73)
			if altF4Close(msg, wp, lp, alt&0x8000 != 0, f4&0x8000 != 0) {
				show()
				w.Eval("if(typeof quitApp==='function'&&!window.FH_QUIT_PENDING){window.FH_QUIT_PENDING=true;quitApp(false).finally(()=>{window.FH_QUIT_PENDING=false;});}")
				return 0
			}
		}
		if msg == restart {
			active = add()
		}
		switch msg {
		case 0x10: // WM_CLOSE
			if !active {
				r, _, _ := defSubclass.Call(h, msg, wp, lp)
				return r
			}
			w.Eval("window.FH_TRAY=true;")
			pShowWindow.Call(hwnd, 0)
			return 0
		case 0x8000 + 73:
			switch lp {
			case 0x203: // WM_LBUTTONDBLCLK
				show()
			case 0x205, 0x7b: // WM_RBUTTONUP / WM_CONTEXTMENU
				menu, _, _ := user32.NewProc("CreatePopupMenu").Call()
				defer user32.NewProc("DestroyMenu").Call(menu)
				for i, text := range []string{"Открыть FruityHub", "Выйти"} {
					label, _ := syscall.UTF16PtrFromString(text)
					user32.NewProc("AppendMenuW").Call(menu, 0, uintptr(i+1), uintptr(unsafe.Pointer(label)))
				}
				var point struct{ X, Y int32 }
				user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&point)))
				pSetForegroundWindow.Call(hwnd)
				choice, _, _ := user32.NewProc("TrackPopupMenu").Call(menu, 0x100|0x2, uintptr(point.X), uintptr(point.Y), 0, hwnd, 0)
				if choice != 0 {
					show()
				}
				if choice == 2 {
					w.Eval("if(typeof quitApp==='function')quitApp();")
				}
				user32.NewProc("PostMessageW").Call(hwnd, 0, 0, 0)
			}
			return 0
		}
		r, _, _ := defSubclass.Call(h, msg, wp, lp)
		return r
	})
	ok, _, _ := setSubclass.Call(hwnd, callback, 1, 0)
	if ok == 0 {
		shell.Call(2, uintptr(unsafe.Pointer(&data)))
	}
	return func() {
		removeSubclass.Call(hwnd, callback, 1)
		shell.Call(2, uintptr(unsafe.Pointer(&data)))
		runtime.KeepAlive(callback)
	}, show
}
