package app

import (
	"path/filepath"
	"testing"
)

func TestUninstallPlugin(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Cleanup(func() { scanMu.Lock(); scanCache = nil; scanMu.Unlock() })
	plug := filepath.Join(home, ".vst3", "Acme", "Zapper.vst3")
	writeF(t, filepath.Join(plug, "Contents", "x86_64-linux", "Zapper.so"), "bin")
	other := filepath.Join(home, ".vst3", "Acme", "Other.vst3")
	writeF(t, filepath.Join(other, "Contents", "x86_64-linux", "Other.so"), "bin")
	cfg := &Config{}
	ScanInstalled(cfg, true)
	plan, err := uninstallPlan(cfg, "Zapper", "VST3")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range plan.Items {
		if it.Path == other || it.Path == filepath.Dir(plug) {
			t.Fatalf("в план попало чужое: %s", it.Path)
		}
		paths = append(paths, it.Path)
	}
	if len(paths) == 0 || plan.Items[0].Kind != "plugin" {
		t.Fatalf("плагина нет в плане: %+v", plan.Items)
	}
	// чужой путь в запросе игнорируется
	if _, err := UninstallPlugin(cfg, &Job{}, "Zapper", "VST3", append(paths, other)); err != nil {
		t.Fatal(err)
	}
	if exists(plug) || !exists(other) {
		t.Fatalf("удалить должен был только Zapper: zapper=%v other=%v", exists(plug), exists(other))
	}
	if FindInstalled(cfg, "Zapper", "VST3") != nil {
		t.Fatal("после удаления плагин всё ещё в списке")
	}
}
