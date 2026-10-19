package app

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flsync/internal/flp"
)

// synthFLP builds a small FL 21 project: one sampler channel with a sample,
// one VST3 synth (Serum) and one native mixer effect.
func synthFLP(sample string) []byte {
	u16 := func(s string) []byte {
		var b []byte
		for _, r := range s {
			b = append(b, byte(r), byte(r>>8))
		}
		return append(b, 0, 0)
	}
	wrapper := func(name, vendor, path string) []byte {
		b := []byte{10, 0, 0, 0}
		for _, kv := range []struct {
			id uint32
			v  string
		}{{54, name}, {56, vendor}, {55, path}} {
			hdr := make([]byte, 12)
			binary.LittleEndian.PutUint32(hdr, kv.id)
			binary.LittleEndian.PutUint64(hdr[4:], uint64(len(kv.v)))
			b = append(append(b, hdr...), kv.v...)
		}
		return b
	}
	f := &flp.File{Format: 0, Channels: 2, PPQ: 96, Events: []flp.Event{
		{ID: 199, Data: []byte("21.2.3.4004\x00")},
		{ID: 64, Data: []byte{0, 0}}, {ID: 201, Data: u16("Sampler")}, {ID: 196, Data: u16(sample)},
		{ID: 64, Data: []byte{1, 0}}, {ID: 201, Data: u16("Fruity Wrapper")}, {ID: 203, Data: u16("Serum")},
		{ID: 213, Data: wrapper("Serum", "Xfer Records", `C:\Program Files\Common Files\VST3\Serum.vst3`)},
		{ID: 201, Data: u16("Fruity Reeverb 2")}, {ID: 98, Data: []byte{0, 0}},
	}}
	return f.Bytes()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func dump(j *Job) string {
	return strings.Join(j.Steps, "\n") + "\n--- raw:\n" + strings.Join(j.raw, "\n")
}

func TestE2E(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	remote := filepath.Join(root, "remote.git")
	must(t, exec.Command("git", "init", "--bare", "-b", "main", remote).Run())

	// external sample + project with a .flp referencing it
	ext := filepath.Join(root, "Сэмплы", "kick 01.wav")
	os.MkdirAll(filepath.Dir(ext), 0o755)
	os.WriteFile(ext, []byte("RIFF....WAVEfake kick"), 0o644)
	aDir := filepath.Join(root, "A", "Мой трек")
	os.MkdirAll(filepath.Join(aDir, "Renders"), 0o755)
	os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP(ext), 0o644)
	var err error
	os.WriteFile(filepath.Join(aDir, "Renders", "mixdown.wav"), []byte("unused render"), 0o644)

	cfgA := &Config{UserName: "tetramino"}
	cfgB := &Config{UserName: "friend"}
	jA, jB := &Job{}, &Job{}
	A := &Project{Dir: aDir, Cfg: cfgA, Job: jA}
	must(t, A.Init(remote))
	_, err = A.commitAll("start")
	must(t, err)
	_, err = A.Send("Первая отправка", false)
	if err != nil {
		t.Fatal(err, dump(jA))
	}
	if !exists(filepath.Join(aDir, "Audio", "kick 01.wav")) {
		t.Fatal("sample not collected")
	}
	gi, _ := os.ReadFile(filepath.Join(aDir, ".gitignore"))
	gi, _ = os.ReadFile(filepath.Join(aDir, ".git", "info", "exclude"))
	if !strings.Contains(string(gi), "/Renders/mixdown.wav") {
		t.Fatal("unused not ignored:\n" + string(gi))
	}
	pl, _ := os.ReadFile(filepath.Join(aDir, "PLUGINS.txt"))
	t.Log("\n" + string(pl))

	// friend clones
	bParent := filepath.Join(root, "B")
	os.MkdirAll(bParent, 0o755)
	bDir, err := Clone(cfgB, jB, remote, bParent)
	if err != nil {
		t.Fatal(err, dump(jB))
	}
	B := &Project{Dir: bDir, Cfg: cfgB, Job: jB}
	data, _ := os.ReadFile(filepath.Join(bDir, "Audio", "kick 01.wav"))
	if string(data) != "RIFF....WAVEfake kick" {
		t.Fatalf("LFS content wrong: %q", data)
	}
	// relink: B's flp should point into B's folder... ext exists on same machine in test, so emulate missing:
	os.Rename(ext, ext+".moved")
	n, err := B.Relink()
	must(t, err)
	bf, _ := flp.ParseFile(filepath.Join(bDir, "Track.flp"))
	t.Log("relinked", n, bf.Info().Samples)
	if n != 1 || !strings.HasPrefix(bf.Info().Samples[0], bDir) {
		t.Fatal("relink failed")
	}
	_, err = B.Send("relink", false)
	must(t, err)

	// locking
	if err := A.TakeLock(false, ""); err != nil {
		t.Fatal(err, dump(jA))
	}
	if err := B.TakeLock(false, ""); err == nil {
		t.Fatal("B should not get the lock")
	} else {
		t.Log("B lock refused:", err)
	}

	// both edit the flp -> conflict
	af, _ := flp.ParseFile(filepath.Join(aDir, "Track.flp"))
	af.Events = append(af.Events, flp.Event{ID: 0xC0 + 15, Data: []byte("A\x00")})
	os.WriteFile(filepath.Join(aDir, "Track.flp"), af.Bytes(), 0o644)
	bf.Events = append(bf.Events, flp.Event{ID: 0xC0 + 15, Data: []byte("B\x00")})
	os.WriteFile(filepath.Join(bDir, "Track.flp"), bf.Bytes(), 0o644)
	_, err = A.Send("A edit", true)
	must(t, err)
	res, err := B.Send("B edit", false)
	if err != nil {
		t.Fatal(err, dump(jB))
	}
	t.Log("conflicts:", res.Conflicts)
	if len(res.Conflicts) != 1 {
		t.Fatal("expected conflict copy")
	}
	_, err = A.Receive()
	must(t, err)
	ents, _ := os.ReadDir(aDir)
	for _, e := range ents {
		t.Log("A has:", e.Name())
	}
	if A.ReadLock() != nil {
		t.Fatal("lock should be released")
	}
	h := A.git().Log(20)
	for _, c := range h {
		t.Log(c.Short, c.Author, c.Subject, c.Files)
	}
	dst, err := A.RestoreVersion(h[len(h)-1].Hash, "Track.flp")
	must(t, err)
	t.Log("restored", dst)
	st, _ := A.git().Status()
	t.Log("status after:", st)
}
