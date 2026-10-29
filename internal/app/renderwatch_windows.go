//go:build windows

package app

import (
	"sync"
	"syscall"
	"unsafe"
)

var renderWatches = struct {
	sync.Mutex
	handles map[string]syscall.Handle
}{handles: map[string]syscall.Handle{}}
var renderKernel = syscall.NewLazyDLL("kernel32.dll")
var firstRenderChange = renderKernel.NewProc("FindFirstChangeNotificationW")
var nextRenderChange = renderKernel.NewProc("FindNextChangeNotification")
var closeRenderChange = renderKernel.NewProc("FindCloseChangeNotification")

// Уведомления Windows заменяют повторный обход неизменившихся каталогов.
// Если ОС не поддерживает уведомления для диска, остаётся прежняя проверка.
func renderChanged(path string) bool {
	renderWatches.Lock()
	defer renderWatches.Unlock()
	h, ok := renderWatches.handles[path]
	if !ok {
		p, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return true
		}
		value, _, _ := firstRenderChange.Call(uintptr(unsafe.Pointer(p)), 1, 0x1|0x2|0x10)
		if value == ^uintptr(0) || value == 0 {
			return true
		}
		renderWatches.handles[path] = syscall.Handle(value)
		return true
	}
	status, err := syscall.WaitForSingleObject(h, 0)
	if err != nil || status == 0xffffffff {
		return true
	}
	if status != 0 {
		return false
	}
	if ok, _, _ := nextRenderChange.Call(uintptr(h)); ok == 0 {
		closeRenderChange.Call(uintptr(h))
		delete(renderWatches.handles, path)
	}
	return true
}

func pruneRenderWatches(paths map[string]bool) {
	renderWatches.Lock()
	defer renderWatches.Unlock()
	for path, h := range renderWatches.handles {
		if !paths[path] {
			closeRenderChange.Call(uintptr(h))
			delete(renderWatches.handles, path)
		}
	}
}
