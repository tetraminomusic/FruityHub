//go:build windows

package app

import (
	"errors"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// ---------- обычное окно Проводника для выбора папки и файла (IFileOpenDialog) ----------
//
// Раньше папка выбиралась старым деревом «Обзор папок» (FolderBrowserDialog из PowerShell). Теперь —
// тот же Проводник, что в любой программе Windows: адресная строка, поиск, избранное. Окно привязано
// к окну FruityHub (модальное), поэтому второе такое окно поверх не откроется. Не получилось — по-старому.

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	shell32              = syscall.NewLazyDLL("shell32.dll")
	pCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	pCoUninitialize      = ole32.NewProc("CoUninitialize")
	pCoCreateInstance    = ole32.NewProc("CoCreateInstance")
	pCoTaskMemFree       = ole32.NewProc("CoTaskMemFree")
	pSHCreateItemFromPar = shell32.NewProc("SHCreateItemFromParsingName")
)

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

var (
	clsidFileOpenDialog = guid{0xDC1C5A9C, 0xE88A, 0x4DDE, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = guid{0xD57C7288, 0xD4AD, 0x4768, [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = guid{0x43826D1E, 0xE718, 0x42EE, [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosPathMustExist   = 0x800
	fosFileMustExist   = 0x1000
	sigdnFileSysPath   = 0x80058000
	errCancelledHR     = 0x800704C7
	clsctxInprocServer = 0x1
	coinitApartment    = 0x2
)

// номера методов в таблице IFileOpenDialog / IShellItem
const (
	vRelease        = 2
	vShow           = 3
	vSetFileTypes   = 4
	vSetOptions     = 9
	vGetOptions     = 10
	vSetFolder      = 12
	vSetTitle       = 17
	vGetResult      = 20
	vItemGetDisplay = 5
)

type comObj struct{ p unsafe.Pointer }

func (o comObj) call(i int, args ...uintptr) uintptr {
	vtbl := *(**[64]uintptr)(o.p)
	r, _, _ := syscall.SyscallN(vtbl[i], append([]uintptr{uintptr(o.p)}, args...)...)
	return r
}

func (o comObj) release() {
	if o.p != nil {
		o.call(vRelease)
	}
}

type filterSpec struct {
	name, spec *uint16
}

// nativePick показывает Проводник. folder — выбрать папку; filters — пары «название», «*.jpg;*.png».
func nativePick(title string, folder bool, filters [][2]string, start string) (path string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("окно выбора не открылось")
		}
	}()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := pCoInitializeEx.Call(0, coinitApartment)
	if int32(hr) >= 0 {
		defer pCoUninitialize.Call()
	}
	var dlgp unsafe.Pointer
	if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlgp))); int32(hr) < 0 || dlgp == nil {
		return "", errors.New("нет IFileOpenDialog")
	}
	dlg := comObj{dlgp}
	defer dlg.release()
	var opts uint32
	dlg.call(vGetOptions, uintptr(unsafe.Pointer(&opts)))
	opts |= fosForceFileSystem | fosPathMustExist
	if folder {
		opts |= fosPickFolders
	} else {
		opts |= fosFileMustExist
	}
	dlg.call(vSetOptions, uintptr(opts))
	if t, err := syscall.UTF16PtrFromString(title); err == nil {
		dlg.call(vSetTitle, uintptr(unsafe.Pointer(t)))
	}
	if !folder && len(filters) > 0 {
		specs := make([]filterSpec, 0, len(filters))
		for _, f := range filters {
			n, _ := syscall.UTF16PtrFromString(f[0])
			s, _ := syscall.UTF16PtrFromString(f[1])
			specs = append(specs, filterSpec{n, s})
		}
		dlg.call(vSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
		runtime.KeepAlive(specs)
	}
	if start != "" && dirExists(start) {
		if sp, err := syscall.UTF16PtrFromString(start); err == nil {
			var item unsafe.Pointer
			if hr, _, _ := pSHCreateItemFromPar.Call(uintptr(unsafe.Pointer(sp)), 0, uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item))); int32(hr) >= 0 && item != nil {
				dlg.call(vSetFolder, uintptr(item))
				comObj{item}.release()
			}
		}
	}
	var owner uintptr
	if PickerOwner != nil {
		owner = PickerOwner()
	}
	if hr := dlg.call(vShow, owner); uint32(hr) == errCancelledHR {
		return "", nil
	} else if int32(hr) < 0 {
		return "", errors.New("окно выбора не открылось")
	}
	var itemp unsafe.Pointer
	if hr := dlg.call(vGetResult, uintptr(unsafe.Pointer(&itemp))); int32(hr) < 0 || itemp == nil {
		return "", nil
	}
	item := comObj{itemp}
	defer item.release()
	var name *uint16
	if hr := item.call(vItemGetDisplay, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); int32(hr) < 0 || name == nil {
		return "", errors.New("не получилось узнать путь")
	}
	defer pCoTaskMemFree.Call(uintptr(unsafe.Pointer(name)))
	return strings.TrimSpace(utf16PtrToString(name)), nil
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	var s []uint16
	for ptr := unsafe.Pointer(p); ; ptr = unsafe.Add(ptr, 2) {
		c := *(*uint16)(ptr)
		if c == 0 {
			break
		}
		s = append(s, c)
	}
	return syscall.UTF16ToString(s)
}
