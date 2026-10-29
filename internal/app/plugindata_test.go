package app

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginLicenseDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	name, vendor := "LicenseTransferTestSynth", "LicenseTransferTestVendor"
	plug := filepath.Join(home, "plugins", name+".vst3")
	writeF(t, plug, "bin")
	docs := documentsDir()
	want := []string{
		name + ".license",
		name + "/license.dat",
		vendor + "/" + name + "/presets/default.fxp",
		vendor + "/Licenses/" + name + ".lic",
		"Licenses/" + name + ".key",
	}
	for _, rel := range want {
		writeF(t, filepath.Join(docs, filepath.FromSlash(rel)), "data")
	}
	writeF(t, filepath.Join(docs, vendor, "Licenses", "OtherSynth.lic"), "other")
	writeF(t, filepath.Join(docs, "Licenses", "OtherSynth.key"), "other")
	writeF(t, filepath.Join(filepath.Dir(plug), name+".key"), "side-license")
	writeF(t, filepath.Join(filepath.Dir(plug), "OtherSynth.key"), "other")
	writeF(t, filepath.Join(filepath.Dir(plug), "license.dat"), "unattributed")

	data, skipped := findPluginData(name, vendor, plug)
	if len(skipped) != 0 {
		t.Fatalf("unexpected skipped data: %+v", skipped)
	}
	got := map[string]bool{}
	for i := range data {
		d := &data[i]
		_, files := dataFiles(d)
		for _, f := range files {
			got[d.Root+":"+strings.TrimSuffix(d.Rel+"/"+f, "/")] = true
		}
	}
	for _, rel := range want {
		if !got["DOCS:"+rel] {
			t.Errorf("missing %s in %+v", rel, data)
		}
	}
	if !got[sideRoot+":"+name+".key"] || len(got) != len(want)+1 {
		t.Fatalf("wrong license selection: %+v", got)
	}

	// Одна лицензия не должна мешать переносу остальных общих данных производителя.
	otherVendor := "LicenseTransferOtherVendor"
	writeF(t, filepath.Join(docs, otherVendor, name+".lic"), "license")
	writeF(t, filepath.Join(docs, otherVendor, "resources.dat"), "resources")
	data, _ = findPluginData(name, otherVendor, plug)
	found := false
	for _, d := range data {
		found = found || d.Root == "DOCS" && d.Rel == otherVendor
	}
	if !found {
		t.Fatalf("vendor resources omitted: %+v", data)
	}
}

func TestLicenseDataRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	src := filepath.Join(root, "source")
	writeF(t, filepath.Join(src, "Synth.key"), "sender-license")
	writeF(t, filepath.Join(src, "license.dat"), "activation-data")
	writeF(t, filepath.Join(src, "gui-activation-generated.xml"), "interface")
	if err := os.Symlink(filepath.Join(src, "Synth.key"), filepath.Join(src, "linked.key")); err != nil {
		t.Fatal(err)
	}
	data := []PluginData{{Root: sideRoot, Rel: "Synth", abs: src}}
	offer := buildOffer("VST3", "Synth.vst3", "", data)
	if len(offer.Items) != 1 || len(offer.Items[0].Files) != 3 {
		t.Fatalf("licenses missing from negotiation (or symlink included): %+v", offer)
	}
	if size := dirSizeData(src); size != int64(len("sender-licenseactivation-datainterface")) {
		t.Fatalf("license data size = %d", size)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zipData(zw, data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		writeF(t, filepath.Join(stage, filepath.FromSlash(strings.TrimPrefix(f.Name, dataPrefix))), string(b))
	}
	dst := filepath.Join(root, "destination")
	license := filepath.Join(dst, "Synth", "Synth.key")
	writeF(t, license, "recipient-license")
	_, problems := installData(&Job{}, stage, dst, nil, false)
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	for path, want := range map[string]string{license: "recipient-license", filepath.Join(dst, "Synth", "license.dat"): "activation-data"} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Fatalf("%s = %q (%v), want %q", path, b, err, want)
		}
	}
	_, problems = installData(&Job{}, stage, dst, nil, true)
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if b, _ := os.ReadFile(license); string(b) != "sender-license" {
		t.Fatalf("explicit reinstall did not replace license: %q", b)
	}
	backups, _ := filepath.Glob(filepath.Join(ConfigDir(), "plugin-backups", "*", filepath.Base(dst), "Synth", "Synth.key"))
	if len(backups) != 1 {
		t.Fatalf("missing license backup: %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != "recipient-license" {
		t.Fatalf("wrong license backup: %q", b)
	}
}

func writeF(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Плагин уходит вместе с папками данных и лицензиями, получатель раскладывает их по тем же местам
// и не затирает свои файлы; отчёт сообщает о больших папках, которые не ушли.
func TestPluginDataTransfer(t *testing.T) {
	root := t.TempDir()
	send, recv := filepath.Join(root, "send"), filepath.Join(root, "recv")
	t.Setenv("HOME", send)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))

	plug := filepath.Join(send, ".vst3", "Serum.vst3")
	writeF(t, filepath.Join(plug, "Contents", "x86_64-linux", "Serum.so"), "bin")
	docs := filepath.Join(send, "Documents")
	writeF(t, filepath.Join(docs, "Xfer", "Serum Presets", "Presets", "Lead.fxp"), "preset")
	writeF(t, filepath.Join(docs, "Xfer", "Serum Presets", "Tables", "Saw.wav"), "table")
	writeF(t, filepath.Join(docs, "Xfer", "Serum Presets", "System", "Serum.key"), "LICENSE") // лицензия уходит с данными
	writeF(t, filepath.Join(docs, "Xfer", "Other Plugin", "x.dat"), "other")                  // чужая папка производителя — не уходит
	writeF(t, filepath.Join(docs, "Unrelated", "a.txt"), "x")
	writeF(t, filepath.Join(send, ".config", "Serum", "settings.json"), "{}")

	data, skip := findPluginData("Serum", "Xfer Records", plug)
	got := map[string]bool{}
	for _, d := range data {
		got[d.Root+":"+d.Rel] = true
	}
	if !got["DOCS:Xfer/Serum Presets"] || !got["CONFIG:Serum"] || len(data) != 2 || len(skip) != 0 {
		t.Fatalf("папки данных: %+v / %+v", data, skip)
	}
	zipFile := filepath.Join(root, "p.zip")
	skipped := []PluginData{{Root: "DOCS", Rel: "Xfer/Serum Library", Size: 5 << 30, Note: "больше 2 ГБ"}}
	if err := zipPlugin(plug, zipFile, nil, data, skipped, true); err != nil {
		t.Fatal(err)
	}

	// получатель: свой пресет с тем же именем не должен затереться
	t.Setenv("HOME", recv)
	writeF(t, filepath.Join(recv, "Documents", "Xfer", "Serum Presets", "Presets", "Lead.fxp"), "MINE")
	os.MkdirAll(filepath.Join(recv, ".vst3"), 0o755)
	rep, err := InstallPluginZipEx(&Config{}, &Job{}, zipFile, "VST3", "Xfer Records")
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(recv, ".vst3", "Serum.vst3", "Contents", "x86_64-linux", "Serum.so")) {
		t.Fatal("плагин не встал")
	}
	if b, _ := os.ReadFile(filepath.Join(recv, "Documents", "Xfer", "Serum Presets", "Presets", "Lead.fxp")); string(b) != "MINE" {
		t.Fatal("свой файл получателя затёрт")
	}
	if !exists(filepath.Join(recv, "Documents", "Xfer", "Serum Presets", "Tables", "Saw.wav")) {
		t.Fatal("недостающие данные не разложены")
	}
	if !exists(filepath.Join(recv, ".config", "Serum", "settings.json")) {
		t.Fatal("данные из второй папки не разложены")
	}
	if b, _ := os.ReadFile(filepath.Join(recv, "Documents", "Xfer", "Serum Presets", "System", "Serum.key")); string(b) != "LICENSE" {
		t.Fatal("лицензия должна передаваться вместе с данными")
	}
	if exists(filepath.Join(recv, "Documents", "Xfer", "Other Plugin")) {
		t.Fatal("чужие данные производителя не должны передаваться")
	}
	if len(rep.Data) != 2 {
		t.Fatalf("отчёт о данных: %+v", rep.Data)
	}
	var big, lic bool
	for _, w := range rep.Warnings {
		big = big || strings.Contains(w.Text, "Serum Library")
		lic = lic || strings.Contains(w.Text, "лицензией")
	}
	if !big || !lic {
		t.Fatalf("предупреждения: %+v", rep.Warnings)
	}
}

func TestBinArch(t *testing.T) {
	dir := t.TempDir()
	pe := make([]byte, 512)
	pe[0], pe[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(pe[0x3c:], 0x80)
	copy(pe[0x80:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[0x84:], 0x14c)
	os.WriteFile(filepath.Join(dir, "a.dll"), pe, 0o644)
	if a := binArch(filepath.Join(dir, "a.dll")); len(a) != 1 || a[0] != archX86 {
		t.Fatalf("PE x86: %v", a)
	}
	binary.LittleEndian.PutUint16(pe[0x84:], 0x8664)
	os.WriteFile(filepath.Join(dir, "b.dll"), pe, 0o644)
	if a := binArch(filepath.Join(dir, "b.dll")); len(a) != 1 || a[0] != archX64 {
		t.Fatalf("PE x64: %v", a)
	}
	fat := make([]byte, 128)
	binary.BigEndian.PutUint32(fat, 0xcafebabe)
	binary.BigEndian.PutUint32(fat[4:], 2)
	binary.BigEndian.PutUint32(fat[8:], 0x01000007)
	binary.BigEndian.PutUint32(fat[28:], 0x0100000c)
	os.WriteFile(filepath.Join(dir, "u"), fat, 0o644)
	if a := binArch(filepath.Join(dir, "u")); !hasArch(a, archX64) || !hasArch(a, archARM64) {
		t.Fatalf("universal: %v", a)
	}
}

func TestNeedsActivation(t *testing.T) {
	if !needsActivation("Serum", "Xfer Records") || !needsActivation("Kontakt 7", "Native Instruments") {
		t.Fatal("платные должны требовать активацию")
	}
	if needsActivation("Vital", "Vital Audio") || needsActivation("OTT", "Xfer Records") || needsActivation("Some Free", "") {
		t.Fatal("бесплатные — без активации")
	}
}

// Плагин встаёт в ту же подпапку производителя, что у отправителя (VST3/Arturia/…),
// а не в подпапку, где у получателя больше всего плагинов (VST3/iZotope).
func TestPluginKeepsVendorSubfolder(t *testing.T) {
	root := t.TempDir()
	send, recv := filepath.Join(root, "send"), filepath.Join(root, "recv")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", send)
	plug := filepath.Join(send, ".vst3", "Arturia", "DX7 V.vst3")
	writeF(t, filepath.Join(plug, "Contents", "x86_64-linux", "DX7 V.so"), "bin")
	cfg := &Config{}
	ScanInstalled(cfg, true)
	r, sub := pluginRootOf(cfg, plug)
	if r != filepath.Join(send, ".vst3") || sub != "Arturia" {
		t.Fatalf("корень %q, подпапка %q", r, sub)
	}
	zipFile := filepath.Join(root, "p.zip")
	if err := zipPlugin(plug, zipFile, map[string][]byte{subdirKey: []byte(sub)}, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", recv)
	for _, n := range []string{"Ozone", "Neutron", "RX"} {
		writeF(t, filepath.Join(recv, ".vst3", "iZotope", n+".vst3", "Contents", "x86_64-linux", n+".so"), "bin")
	}
	ScanInstalled(cfg, true)
	rep, err := InstallPluginZipEx(cfg, &Job{}, zipFile, "VST3", "Arturia")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(recv, ".vst3", "Arturia", "DX7 V.vst3"); rep.Path != want {
		t.Fatalf("встал в %s, ждали %s", rep.Path, want)
	}
	// та же папка с другой версией — заменяется на месте, а не рядом
	if _, err := InstallPluginZipEx(cfg, &Job{}, zipFile, "VST3", "Arturia"); err != nil {
		t.Fatal(err)
	}
	if ms, _ := filepath.Glob(filepath.Join(recv, ".vst3", "*", "DX7 V.vst3")); len(ms) != 1 {
		t.Fatalf("должна быть одна копия: %v", ms)
	}
}

// Плагин, который раньше положили не в ту папку (D-50 в iZotope), при новой передаче переезжает в правильную,
// а старая копия уходит в резервную; опустевшая чужая папка удаляется.
func TestPluginMovesFromWrongFolder(t *testing.T) {
	root := t.TempDir()
	send, recv := filepath.Join(root, "send"), filepath.Join(root, "recv")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("HOME", send)
	plug := filepath.Join(send, ".vst3", "Roland Cloud", "D-50.vst3")
	writeF(t, filepath.Join(plug, "Contents", "x86_64-linux", "D-50.so"), "new")
	zipFile := filepath.Join(root, "p.zip")
	if err := zipPlugin(plug, zipFile, map[string][]byte{subdirKey: []byte("Roland Cloud")}, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", recv)
	writeF(t, filepath.Join(recv, ".vst3", "iZotope", "Ozone.vst3", "Contents", "x86_64-linux", "Ozone.so"), "bin")
	writeF(t, filepath.Join(recv, ".vst3", "Wrong", "D-50.vst3", "Contents", "x86_64-linux", "D-50.so"), "old")
	cfg := &Config{}
	ScanInstalled(cfg, true)
	rep, err := InstallPluginZipEx(cfg, &Job{}, zipFile, "VST3", "Roland Cloud")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(recv, ".vst3", "Roland Cloud", "D-50.vst3"); rep.Path != want {
		t.Fatalf("встал в %s, ждали %s", rep.Path, want)
	}
	if ms, _ := filepath.Glob(filepath.Join(recv, ".vst3", "*", "D-50.vst3")); len(ms) != 1 {
		t.Fatalf("должна остаться одна копия: %v", ms)
	}
	if exists(filepath.Join(recv, ".vst3", "Wrong")) {
		t.Fatal("опустевшая папка старой копии должна удалиться")
	}
	if !exists(filepath.Join(recv, ".vst3", "iZotope", "Ozone.vst3")) {
		t.Fatal("чужие плагины трогать нельзя")
	}
}

func TestSubdirAndLicenseRules(t *testing.T) {
	for in, want := range map[string]string{"Arturia": "Arturia", "Native Instruments/Kontakt": filepath.Join("Native Instruments", "Kontakt"),
		"../x": "", "/abs": "", "VST3/Arturia": "", "C:/x": "", "a/b/c/d": ""} {
		if got := cleanSubdir(in); got != want {
			t.Errorf("cleanSubdir(%q) = %q, ждали %q", in, got, want)
		}
	}
	if isLicenseFile("gui-activation-generated.xml") || isLicenseFile("activation.png") {
		t.Fatal("ресурсы интерфейса — не лицензия (Arturia без них не открывается)")
	}
	if !isLicenseFile("Serum.key") || !isLicenseFile("license.dat") {
		t.Fatal("файлы лицензий должны распознаваться")
	}
}
