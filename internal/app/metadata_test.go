package app

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flsync/internal/flp"
)

func wavFile(t *testing.T, path string, extra []byte) []byte {
	var b bytes.Buffer
	fmtc := []byte{1, 0, 2, 0, 0x44, 0xac, 0, 0, 0x10, 0xb1, 2, 0, 4, 0, 16, 0}
	data := bytes.Repeat([]byte{1, 2, 3}, 1001) // нечётная длина — проверяем выравнивание
	body := &bytes.Buffer{}
	body.WriteString("WAVE")
	put := func(id string, d []byte) {
		body.WriteString(id)
		binary.Write(body, binary.LittleEndian, uint32(len(d)))
		body.Write(d)
		if len(d)%2 == 1 {
			body.WriteByte(0)
		}
	}
	put("fmt ", fmtc)
	put("data", data)
	if extra != nil {
		put("LIST", extra)
	}
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(body.Len()))
	b.Write(body.Bytes())
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

type riffChunk struct {
	id   string
	data []byte
}

func readRIFF(t *testing.T, path string) []riffChunk {
	b, _ := os.ReadFile(path)
	if string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatal("не RIFF")
	}
	if int(binary.LittleEndian.Uint32(b[4:])) != len(b)-8 {
		t.Fatalf("размер RIFF %d, файл %d", binary.LittleEndian.Uint32(b[4:]), len(b)-8)
	}
	var out []riffChunk
	for p := 12; p+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[p+4:]))
		out = append(out, riffChunk{string(b[p : p+4]), b[p+8 : p+8+n]})
		p += 8 + n + n%2
	}
	return out
}

func TestTagWAV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mix.wav")
	data := wavFile(t, path, []byte("INFOINAM\x04\x00\x00\x00old\x00"))
	m := TrackMeta{Title: "Дрифт", Artist: "tetramino", Album: "Night", Year: "2026", Genre: "Phonk", Comment: "привет"}
	cover := &coverImg{mime: "image/png", data: []byte("\x89PNGfake")}
	for i := 0; i < 2; i++ { // второй раз — старые теги заменяются, а не копятся
		if err := TagFile(path, m, cover); err != nil {
			t.Fatal(err)
		}
	}
	cs := readRIFF(t, path)
	ids := []string{}
	for _, c := range cs {
		ids = append(ids, c.id)
	}
	if strings.Join(ids, ",") != "fmt ,data,LIST,id3 " {
		t.Fatalf("куски: %v", ids)
	}
	if !bytes.Equal(cs[1].data, data) {
		t.Fatal("звук испорчен")
	}
	if !bytes.Contains(cs[2].data, []byte("Дрифт")) || bytes.Contains(cs[2].data, []byte("old")) {
		t.Fatalf("LIST INFO: %q", cs[2].data)
	}
	id3 := cs[3].data
	if string(id3[:3]) != "ID3" || !bytes.Contains(id3, []byte("TIT2")) || !bytes.Contains(id3, []byte("APIC")) || !bytes.Contains(id3, []byte("\x89PNGfake")) {
		t.Fatal("нет ID3 с обложкой")
	}
}

func TestTagMP3AndFLAC(t *testing.T) {
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "a.mp3")
	audio := bytes.Repeat([]byte{0xff, 0xfb, 0x90, 0x00}, 100)
	old := id3v23(TrackMeta{Title: "old"}, nil)
	os.WriteFile(mp3, append(old, audio...), 0o644)
	if err := TagFile(mp3, TrackMeta{Title: "New", Artist: "Кирюха"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(mp3)
	if !bytes.HasSuffix(b, audio) || bytes.Contains(b, []byte("old")) || bytes.Count(b, []byte("ID3")) != 1 {
		t.Fatal("mp3: тег не заменён")
	}

	fl := filepath.Join(dir, "a.flac")
	si := make([]byte, 34)
	frames := []byte("FRAMEDATA")
	var f bytes.Buffer
	f.WriteString("fLaC")
	f.Write([]byte{0x80, 0, 0, 34})
	f.Write(si)
	f.Write(frames)
	os.WriteFile(fl, f.Bytes(), 0o644)
	if err := TagFile(fl, TrackMeta{Title: "Т", Artist: "A"}, &coverImg{mime: "image/jpeg", data: []byte("jpg")}); err != nil {
		t.Fatal(err)
	}
	if err := TagFile(fl, TrackMeta{Title: "Т2", Artist: "A"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(fl)
	if !bytes.HasSuffix(b, frames) || !bytes.Contains(b, []byte("TITLE=Т2")) || bytes.Count(b, []byte("TITLE=")) != 1 {
		t.Fatalf("flac: %q", b)
	}
	// блоки: STREAMINFO, VORBIS_COMMENT, PADDING(последний)
	p, types := 4, []byte{}
	for {
		h := b[p]
		n := int(b[p+1])<<16 | int(b[p+2])<<8 | int(b[p+3])
		types = append(types, h&0x7f)
		p += 4 + n
		if h&0x80 != 0 {
			break
		}
	}
	if !bytes.Equal(types, []byte{0, 4, 1}) {
		t.Fatalf("блоки FLAC: %v", types)
	}
}

func TestTagRendersOnlyNewAndReadme(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(dir, "Beat.flp"), synthFLP(filepath.Join(dir, "Audio", "kick.wav")), 0o644)
	wavFile(t, filepath.Join(dir, "Audio", "kick.wav"), nil)
	wavFile(t, filepath.Join(dir, "old render.wav"), nil)
	cfg := &Config{}
	p := &Project{Dir: dir, Cfg: cfg, Job: &Job{}}
	SaveTrackMeta(dir, TrackMeta{Title: "Beat", Artist: "tetramino", Album: "LP"})
	if done, _ := TagRenders(cfg, p, false); len(done) != 0 {
		t.Fatalf("старые рендеры трогать не надо: %v", done)
	}
	wavFile(t, filepath.Join(dir, "Beat_master.wav"), nil)
	past := mustOld(filepath.Join(dir, "Beat_master.wav"))
	_ = past
	done, err := TagRenders(cfg, p, false)
	if err != nil || len(done) != 1 || done[0] != "Beat_master.wav" {
		t.Fatalf("новый рендер: %v %v", done, err)
	}
	if again, _ := TagRenders(cfg, p, false); len(again) != 0 {
		t.Fatalf("второй раз не подписываем: %v", again)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Audio", "kick.wav")); bytes.Contains(b, []byte("ID3")) {
		t.Fatal("сэмпл проекта трогать нельзя")
	}
	// README
	SetCover(dir, writeTmp(t, "c.png", "png"))
	if !p.WriteReadme(p.Analyze()) {
		t.Fatal("README не записан")
	}
	rd, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	for _, w := range []string{"# Beat", "**tetramino**", "| Альбом | LP |", "Metadata/cover.png"} {
		if !strings.Contains(string(rd), w) {
			t.Fatalf("в README нет %q:\n%s", w, rd)
		}
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("мой README"), 0o644)
	if p.WriteReadme(p.Analyze()) {
		t.Fatal("свой README перезаписывать нельзя")
	}
}

func mustOld(path string) error { // файл «дописан» — старше 4 секунд
	return os.Chtimes(path, timeAgo(10), timeAgo(10))
}

func writeTmp(t *testing.T, name, content string) string {
	p := filepath.Join(t.TempDir(), name)
	os.WriteFile(p, []byte(content), 0o644)
	return p
}

func TestEmbeddedInCompressedState(t *testing.T) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write([]byte(`<state><sample path="/Users/me/Music/Arturia/Samples/User/vox chop.wav"/></state>`))
	w.Close()
	blob := append([]byte{1, 2, 3, 4}, z.Bytes()...)
	got := flp.EmbeddedPathsForTest(blob)
	if len(got) != 1 || !strings.HasSuffix(got[0], "vox chop.wav") {
		t.Fatalf("%v", got)
	}
}

func TestPresetPathAndSampleTarget(t *testing.T) {
	for p, want := range map[string]bool{"Serum Presets/Presets/Bass/x.fxp": true, "Pigments/Presets/a.pgtx": true, "Data/wavetables/a.wav": false, "Library/Banks/x": true, "Kontakt/inst.nki": true} {
		if isPresetPath(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, "Music", "Arturia"), 0o755)
	if got := pluginSampleTarget("/Users/other/Music/Arturia/Samples/User/a.wav"); got != filepath.Join(home, "Music", "Arturia", "Samples", "User", "a.wav") {
		t.Fatalf("цель: %q", got)
	}
	if got := pluginSampleTarget("/Users/other/Music/Xfer/a.wav"); got != "" {
		t.Fatalf("чужую папку производителя не создаём: %q", got)
	}
	if got := pluginSampleTarget(`C:\Users\x\Music\Arturia\a.wav`); got != "" {
		t.Fatalf("путь Windows на этой системе: %q", got)
	}
}

func timeAgo(sec int) time.Time { return time.Now().Add(-time.Duration(sec) * time.Second) }

// «Отменить» у скачивания: команды git прерываются, недокачанная папка удаляется.
func TestCloneCancel(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfgA := &Config{Server: hubURL}
	must(t, func() error { _, err := cfgA.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
	aDir := filepath.Join(root, "A", "Beat")
	os.MkdirAll(filepath.Join(aDir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP(filepath.Join(aDir, "Audio", "vox.wav")), 0o644)
	os.WriteFile(filepath.Join(aDir, "Audio", "vox.wav"), bytes.Repeat([]byte{1}, 1<<20), 0o644)
	A := &Project{Dir: aDir, Cfg: cfgA, Job: &Job{}}
	url, err := A.CreateOnGitHub("Beat", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = url
	j := &Job{}
	j.ctx, j.cancel = contextWithCancel()
	j.Cancel() // нажали «Отменить» сразу
	dir, err := CloneNamed(cfgA, j, (&Git{Dir: aDir, Cfg: cfgA}).Remote(), filepath.Join(root, "B"), "Beat")
	if !errors.Is(err, errStopped) || dir != "" {
		t.Fatalf("ждали отмену: %q %v", dir, err)
	}
	if exists(filepath.Join(root, "B", "Beat")) {
		t.Fatal("недокачанная папка осталась")
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
