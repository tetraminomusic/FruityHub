//go:build windows

package ui

import (
	"strings"
	"testing"
	"unsafe"
)

func TestTrayABIAndGPU(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && unsafe.Sizeof(trayIconData{}) != 976 {
		t.Fatal("NOTIFYICONDATAW layout does not match Windows x64 ABI")
	}
	for _, lite := range []bool{true, false} {
		args := webviewArgs(lite)
		if strings.Contains(args, "--disable-gpu") {
			t.Fatal("hardware scrolling disabled")
		}
	}
}

func TestAltF4ClosesInsteadOfTray(t *testing.T) {
	for _, c := range []struct {
		msg, key, flags uintptr
		alt, f4, want   bool
	}{
		{0x104, 0x73, 1 << 29, false, false, true},
		{0x112, 0xf060, 0, true, true, true},
		{0x10, 0, 0, true, true, true},
		{0x10, 0, 0, false, false, false},          // крестик
		{0x112, 0xf060, 0, false, false, false},    // мышь / системное меню
		{0x104, 0x73, 0, false, true, false},       // F4 без Alt
		{0x104, 0x74, 1 << 29, true, false, false}, // Alt+F5
	} {
		if got := altF4Close(c.msg, c.key, c.flags, c.alt, c.f4); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}
