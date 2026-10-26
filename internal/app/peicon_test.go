package app

import (
	"os"
	"testing"
)

// иконки читаются прямо из собранного exe (FRUITYHUB_EXE=dist/FruityHub.exe go test -run TestExeIconGroup)
func TestExeIconGroup(t *testing.T) {
	exe := os.Getenv("FRUITYHUB_EXE")
	if exe == "" {
		t.Skip("нет собранного exe")
	}
	a, err := exeIconGroup(exe, iconGroupShown)
	if err != nil {
		t.Fatal(err)
	}
	tile, _ := exeIconGroup(exe, iconGroupTile)
	clear, _ := exeIconGroup(exe, iconGroupClear)
	if len(a) != 104 || len(tile) != 104 || len(clear) != 104 || string(a) == string(tile) || string(tile) == string(clear) {
		t.Fatalf("%d %x %x %x", len(a), a[:8], tile[:8], clear[:8])
	}
}
