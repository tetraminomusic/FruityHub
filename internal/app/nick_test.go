package app

import (
	"os"
	"path/filepath"
	"testing"
)

// Файл компьютера под прежним ником (без id, записан старой версией) показывается под нынешним ником владельца,
// а у самого владельца при отправке удаляется — остаётся один файл под нынешним ником.
func TestMachinesAfterRename(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("HOME", dir)
	md := filepath.Join(dir, ".flsync", "machines")
	os.MkdirAll(md, 0o755)
	var pl []InstalledPlugin
	for _, n := range []string{"Serum", "Vital", "Pro-Q 3", "Sylenth1", "Kontakt", "OTT"} {
		pl = append(pl, InstalledPlugin{Name: n, Format: "VST3"})
	}
	writeJSON(filepath.Join(md, "пидор.json"), Machine{User: "пидор", OS: "macOS", Updated: "2026-10-01 10:00", Plugins: pl})
	writeJSON(filepath.Join(md, "друг.json"), Machine{User: "друг", UserID: "u2", OS: "Windows", Updated: "2026-10-02 10:00", Plugins: pl[:2]})

	oldTaken, oldOwner, oldNick := nickTaken, ownerByPlugins, currentNick
	t.Cleanup(func() { nickTaken, ownerByPlugins, currentNick = oldTaken, oldOwner, oldNick })
	nickTaken = func(n string) (bool, bool) {
		return n == "друг" || n == "кирюха на позитроне", true
	}
	ownerByPlugins = func(m Machine) string {
		if len(m.Plugins) == len(pl) {
			return "u1"
		}
		return ""
	}
	currentNick = func(id, nick string) string {
		return map[string]string{"u1": "кирюха на позитроне", "u2": "друг"}[id]
	}

	// глазами друга
	friend := &Project{Dir: dir, Cfg: &Config{UserID: "u2", UserName: "друг"}}
	got := map[string]bool{}
	for _, m := range friend.Machines() {
		got[m.User] = true
	}
	if !got["кирюха на позитроне"] || got["пидор"] || len(got) != 2 {
		t.Fatalf("у друга: %v", got)
	}
	// у владельца старый файл убирается
	me := &Project{Dir: dir, Cfg: &Config{UserID: "u1", UserName: "кирюха на позитроне"}}
	writeJSON(filepath.Join(md, "кирюха на позитроне.json"), Machine{User: "кирюха на позитроне", UserID: "u1", OS: "macOS", Updated: "2026-10-27 10:00"})
	me.dropMyOldMachines("кирюха на позитроне.json")
	if exists(filepath.Join(md, "пидор.json")) {
		t.Fatal("старый файл остался")
	}
	if !exists(filepath.Join(md, "друг.json")) {
		t.Fatal("удалён чужой файл")
	}
	if len(me.Cfg.OldNicks) != 1 || me.Cfg.OldNicks[0] != "пидор" {
		t.Fatalf("прежний ник не запомнен: %v", me.Cfg.OldNicks)
	}
}
