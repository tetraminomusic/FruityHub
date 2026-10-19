package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFLPluginDB(t *testing.T) {
	home := t.TempDir()
	os.Setenv("HOME", home)
	custom := filepath.Join(home, "MyPlugins", "Xfer")
	os.MkdirAll(filepath.Join(custom, "Serum.vst3"), 0o755)
	db := filepath.Join(home, "Documents", "Image-Line", "FL Studio", "Presets", "Plugin database", "Installed", "Generators", "VST3")
	os.MkdirAll(db, 0o755)
	os.WriteFile(filepath.Join(db, "Serum.nfo"), []byte("ps_name=Serum\nps_file_name_0=Serum\nps_file_filename_0="+filepath.Join(custom, "Serum.vst3")+"\nps_file_vendorname_0=Xfer Records\n"), 0o644)
	fx := filepath.Join(filepath.Dir(filepath.Dir(db)), "Effects", "Fruity")
	os.MkdirAll(fx, 0o755)
	os.WriteFile(filepath.Join(fx, "Fruity Reeverb 2.nfo"), []byte("ps_name=Fruity Reeverb 2\n"), 0o644)

	cfg := &Config{}
	var fromDB bool
	for _, f := range PluginFolders(cfg) {
		if f.Path == custom && f.Source == "база FL" {
			fromDB = true
		}
	}
	if !fromDB {
		t.Fatalf("custom folder from FL database not found: %+v", PluginFolders(cfg))
	}
	all := ScanInstalled(cfg, true)
	var serum, reeverb *InstalledPlugin
	for i := range all {
		switch all[i].Name {
		case "Serum":
			serum = &all[i]
		case "Fruity Reeverb 2":
			reeverb = &all[i]
		}
	}
	if serum == nil || serum.Vendor != "Xfer Records" || serum.Path == "" || serum.Format != "VST3" {
		t.Fatalf("serum: %+v", serum)
	}
	if reeverb == nil || reeverb.Format != "FL" {
		t.Fatalf("reeverb: %+v", reeverb)
	}
}
