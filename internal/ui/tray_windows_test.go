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
