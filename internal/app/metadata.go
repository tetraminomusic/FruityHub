package app

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"flsync/internal/flp"
)

// ---------- метаданные трека: название, артист, альбом, обложка ----------
//
// Лежат в папке проекта: Metadata/metadata.json и Metadata/cover.jpg (или .png) — уходят участникам
// вместе с проектом. Когда FL Studio рендерит трек в папку проекта, FruityHub сам записывает их
// в новый файл (WAV — ID3 и LIST INFO, MP3 — ID3v2.3, FLAC — Vorbis comment и PICTURE).
// Из них же собирается README.md проекта — чтобы он красиво выглядел на GitHub.

const metaDir = "Metadata"

type TrackMeta struct {
	Title   string `json:"title,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Album   string `json:"album,omitempty"`
	Year    string `json:"year,omitempty"`
	Genre   string `json:"genre,omitempty"`
	Track   string `json:"track,omitempty"`
	Comment string `json:"comment,omitempty"`
}

func (m TrackMeta) empty() bool { return m == TrackMeta{} }

func metaPath(dir string) string { return filepath.Join(dir, metaDir, "metadata.json") }

// LoadTrackMeta — метаданные проекта; название и артист, если их нет в файле, — из .flp (Project info).
func LoadTrackMeta(dir string) TrackMeta {
	var m TrackMeta
	readJSON(metaPath(dir), &m)
	if m.Title == "" || m.Artist == "" {
		if f := mainFLP(dir); f != "" {
			if info, err := parseFLPCached(f); err == nil {
				if m.Title == "" {
					m.Title = info.Title
				}
				if m.Artist == "" {
					m.Artist = info.Artist
				}
			}
		}
	}
	return m
}

// rawTrackMeta — только то, что записано в metadata.json (без подстановки из .flp).
func rawTrackMeta(dir string) TrackMeta {
	var m TrackMeta
	readJSON(metaPath(dir), &m)
	return m
}

func SaveTrackMeta(dir string, m TrackMeta) error {
	os.MkdirAll(filepath.Join(dir, metaDir), 0o755)
	return writeJSON(metaPath(dir), m)
}

// coverPath — обложка проекта ("" — нет).
func coverPath(dir string) string {
	for _, n := range []string{"cover.jpg", "cover.jpeg", "cover.png"} {
		if p := filepath.Join(dir, metaDir, n); exists(p) {
			return p
		}
	}
	return ""
}

// SetCover кладёт картинку обложкой проекта (src == "" — убрать обложку).
func SetCover(dir, src string) error {
	for _, n := range []string{"cover.jpg", "cover.jpeg", "cover.png"} {
		os.Remove(filepath.Join(dir, metaDir, n))
	}
	if src == "" {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(src))
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	if ext != ".jpg" && ext != ".png" {
		return errors.New("обложка — картинка JPG или PNG")
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st.Size() > 10<<20 {
		return errors.New("картинка больше 10 МБ — уменьши её (для обложки хватит 1500×1500)")
	}
	os.MkdirAll(filepath.Join(dir, metaDir), 0o755)
	return copyFile(src, filepath.Join(dir, metaDir, "cover"+ext))
}

func mainFLP(dir string) string {
	ps := (&Project{Dir: dir}).FLPs()
	best, bestT := "", time.Time{}
	for _, rel := range ps {
		if strings.Contains(rel, "/") {
			continue
		}
		p := filepath.Join(dir, rel)
		if st, err := os.Stat(p); err == nil && st.ModTime().After(bestT) {
			best, bestT = p, st.ModTime()
		}
	}
	return best
}

// ---------- запись тегов ----------

type coverImg struct {
	mime string
	data []byte
}

func loadCover(dir string) *coverImg {
	p := coverPath(dir)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	mime := "image/jpeg"
	if strings.HasSuffix(strings.ToLower(p), ".png") {
		mime = "image/png"
	}
	return &coverImg{mime: mime, data: b}
}

// TagFile записывает метаданные (и обложку) в аудиофайл. Поддерживаются WAV, MP3 и FLAC.
func TagFile(path string, m TrackMeta, cover *coverImg) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return tagMP3(path, m, cover)
	case ".wav":
		return tagWAV(path, m, cover)
	case ".flac":
		return tagFLAC(path, m, cover)
	}
	return fmt.Errorf("метаданные пишутся только в WAV, MP3 и FLAC")
}

// id3v23 — тег ID3v2.3 (его понимают и Windows, и macOS, и плееры).
func id3v23(m TrackMeta, cover *coverImg) []byte {
	var frames bytes.Buffer
	text := func(id, v string) {
		if v = strings.TrimSpace(v); v == "" {
			return
		}
		frame(&frames, id, append([]byte{}, encText(v, true)...))
	}
	text("TIT2", m.Title)
	text("TPE1", m.Artist)
	text("TALB", m.Album)
	text("TYER", m.Year)
	text("TCON", m.Genre)
	text("TRCK", m.Track)
	if c := strings.TrimSpace(m.Comment); c != "" {
		var body []byte
		if needUTF16(c) {
			body = append([]byte{1, 'e', 'n', 'g'}, utf16z("")...)
			body = append(body, utf16raw(c)...)
		} else {
			body = append([]byte{0, 'e', 'n', 'g', 0}, encText(c, false)[1:]...)
		}
		frame(&frames, "COMM", body)
	}
	if cover != nil {
		body := []byte{0}
		body = append(body, cover.mime...)
		body = append(body, 0, 3, 0) // конец MIME, тип 3 — обложка (front cover), пустое описание
		body = append(body, cover.data...)
		frame(&frames, "APIC", body)
	}
	size := frames.Len()
	h := []byte{'I', 'D', '3', 3, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	return append(h, frames.Bytes()...)
}

func frame(w *bytes.Buffer, id string, body []byte) {
	w.WriteString(id)
	binary.Write(w, binary.BigEndian, uint32(len(body)))
	w.Write([]byte{0, 0})
	w.Write(body)
}

func needUTF16(s string) bool {
	for _, r := range s {
		if r > 0xff {
			return true
		}
	}
	return false
}

// encText: байт кодировки + текст (ISO-8859-1, а если есть не-латиница — UTF-16 с BOM).
func encText(s string, term bool) []byte {
	if needUTF16(s) {
		b := append([]byte{1}, utf16raw(s)...)
		if term {
			b = append(b, 0, 0)
		}
		return b
	}
	b := []byte{0}
	for _, r := range s {
		b = append(b, byte(r))
	}
	if term {
		b = append(b, 0)
	}
	return b
}

func utf16raw(s string) []byte {
	b := []byte{0xff, 0xfe}
	for _, c := range utf16.Encode([]rune(s)) {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func utf16z(s string) []byte { return append(utf16raw(s), 0, 0) }

// rewrite пишет новый файл рядом и подменяет им старый (дата изменения — как у старого).
func rewrite(path string, fn func(in *os.File, out *bufio.Writer) error) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	st, _ := in.Stat()
	tmp := path + ".fruityhub-tag"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	err = fn(in, w)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	in.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if st != nil {
		os.Chtimes(path, time.Now(), st.ModTime())
	}
	return nil
}

func tagMP3(path string, m TrackMeta, cover *coverImg) error {
	return rewrite(path, func(in *os.File, out *bufio.Writer) error {
		h := make([]byte, 10)
		if _, err := io.ReadFull(in, h); err != nil {
			return errors.New("файл слишком короткий")
		}
		skip := int64(0)
		if string(h[:3]) == "ID3" { // старый тег — заменяем
			skip = 10 + (int64(h[6])<<21 | int64(h[7])<<14 | int64(h[8])<<7 | int64(h[9]))
			if h[5]&0x10 != 0 {
				skip += 10
			}
		}
		out.Write(id3v23(m, cover))
		in.Seek(skip, io.SeekStart)
		_, err := io.Copy(out, in)
		return err
	})
}

func tagWAV(path string, m TrackMeta, cover *coverImg) error {
	return rewrite(path, func(in *os.File, out *bufio.Writer) error {
		h := make([]byte, 12)
		if _, err := io.ReadFull(in, h); err != nil || string(h[:4]) != "RIFF" || string(h[8:]) != "WAVE" {
			return errors.New("это не WAV (RIFF/WAVE)")
		}
		// новые куски: LIST/INFO (его читает Windows) и id3 (его читают macOS, плееры, стриминги)
		var info bytes.Buffer
		info.WriteString("INFO")
		sub := func(id, v string) {
			if v = strings.TrimSpace(v); v == "" {
				return
			}
			b := append([]byte(v), 0)
			info.WriteString(id)
			binary.Write(&info, binary.LittleEndian, uint32(len(b)))
			info.Write(b)
			if len(b)%2 == 1 {
				info.WriteByte(0)
			}
		}
		sub("INAM", m.Title)
		sub("IART", m.Artist)
		sub("IPRD", m.Album)
		sub("ICRD", m.Year)
		sub("IGNR", m.Genre)
		sub("ITRK", m.Track)
		sub("ICMT", m.Comment)
		id3 := id3v23(m, cover)
		// RIFF-размер считаем заранее: старые куски LIST/INFO и id3 выбрасываем, остальные — как есть
		type chunk struct {
			id   string
			off  int64
			size uint32
		}
		var keep []chunk
		total := int64(4)
		pos := int64(12)
		for {
			ch := make([]byte, 8)
			if _, err := in.ReadAt(ch, pos); err != nil {
				break
			}
			id, size := string(ch[:4]), binary.LittleEndian.Uint32(ch[4:])
			drop := strings.EqualFold(id, "id3 ")
			if id == "LIST" {
				t := make([]byte, 4)
				in.ReadAt(t, pos+8)
				drop = string(t) == "INFO"
			}
			if !drop {
				keep = append(keep, chunk{id, pos, size})
				total += 8 + int64(size) + int64(size%2)
			}
			pos += 8 + int64(size) + int64(size%2)
		}
		if len(keep) == 0 {
			return errors.New("в WAV нет данных")
		}
		pad := func(n int) int64 { return int64(n % 2) }
		if info.Len() > 4 {
			total += 8 + int64(info.Len()) + pad(info.Len())
		}
		total += 8 + int64(len(id3)) + pad(len(id3))
		if total > 0xffffffff {
			return errors.New("WAV больше 4 ГБ")
		}
		out.WriteString("RIFF")
		binary.Write(out, binary.LittleEndian, uint32(total))
		out.WriteString("WAVE")
		for _, c := range keep {
			n := 8 + int64(c.size) + int64(c.size%2)
			if _, err := io.Copy(out, io.NewSectionReader(in, c.off, n)); err != nil {
				return err
			}
		}
		put := func(id string, b []byte) {
			out.WriteString(id)
			binary.Write(out, binary.LittleEndian, uint32(len(b)))
			out.Write(b)
			if len(b)%2 == 1 {
				out.WriteByte(0)
			}
		}
		if info.Len() > 4 {
			put("LIST", info.Bytes())
		}
		put("id3 ", id3)
		return nil
	})
}

func tagFLAC(path string, m TrackMeta, cover *coverImg) error {
	return rewrite(path, func(in *os.File, out *bufio.Writer) error {
		h := make([]byte, 4)
		if _, err := io.ReadFull(in, h); err != nil || string(h) != "fLaC" {
			return errors.New("это не FLAC")
		}
		type block struct {
			typ  byte
			data []byte
		}
		var keep []block
		for {
			bh := make([]byte, 4)
			if _, err := io.ReadFull(in, bh); err != nil {
				return errors.New("FLAC повреждён")
			}
			last, typ := bh[0]&0x80 != 0, bh[0]&0x7f
			n := int(bh[1])<<16 | int(bh[2])<<8 | int(bh[3])
			data := make([]byte, n)
			if _, err := io.ReadFull(in, data); err != nil {
				return errors.New("FLAC повреждён")
			}
			if typ != 1 && typ != 4 && typ != 6 { // padding, vorbis comment, picture — заменяем своими
				keep = append(keep, block{typ, data})
			}
			if last {
				break
			}
		}
		var vc bytes.Buffer
		le := func(v int) { binary.Write(&vc, binary.LittleEndian, uint32(v)) }
		vendor := "FruityHub"
		le(len(vendor))
		vc.WriteString(vendor)
		var cs []string
		add := func(k, v string) {
			if v = strings.TrimSpace(v); v != "" {
				cs = append(cs, k+"="+v)
			}
		}
		add("TITLE", m.Title)
		add("ARTIST", m.Artist)
		add("ALBUM", m.Album)
		add("DATE", m.Year)
		add("GENRE", m.Genre)
		add("TRACKNUMBER", m.Track)
		add("COMMENT", m.Comment)
		le(len(cs))
		for _, c := range cs {
			le(len(c))
			vc.WriteString(c)
		}
		keep = append(keep, block{4, vc.Bytes()})
		if cover != nil {
			var pb bytes.Buffer
			be := func(v int) { binary.Write(&pb, binary.BigEndian, uint32(v)) }
			w, hh := 0, 0
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(cover.data)); err == nil {
				w, hh = cfg.Width, cfg.Height
			}
			be(3)
			be(len(cover.mime))
			pb.WriteString(cover.mime)
			be(0)
			be(w)
			be(hh)
			be(24)
			be(0)
			be(len(cover.data))
			pb.Write(cover.data)
			if pb.Len() < 1<<24 {
				keep = append(keep, block{6, pb.Bytes()})
			}
		}
		keep = append(keep, block{1, make([]byte, 1024)}) // запас, чтобы следующая правка тегов была быстрой
		out.WriteString("fLaC")
		for i, b := range keep {
			t := b.typ
			if i == len(keep)-1 {
				t |= 0x80
			}
			n := len(b.data)
			out.Write([]byte{t, byte(n >> 16), byte(n >> 8), byte(n)})
			out.Write(b.data)
		}
		_, err := io.Copy(out, in)
		return err
	})
}

// ---------- новые рендеры: найти и подписать ----------

// metaState — что уже подписано (по папке проекта): относительный путь → время изменения после подписи.
type metaState struct {
	Started bool             `json:"started"` // первый проход сделан: всё, что было до него, — старое
	Files   map[string]int64 `json:"files"`
}

func metaStatePath(dir string) string { return filepath.Join(dir, ".flsync-local", "meta-state.json") }

var metaMu sync.Mutex

// renderCandidates — аудио в папке проекта, которое не используется в проекте (рендеры, сведения),
// с нужными расширениями. Папки Audio/ и Backup/ не смотрим: там сэмплы и автосохранения.
func renderCandidates(p *Project, exts []string) []string {
	want := map[string]bool{}
	for _, e := range exts {
		want["."+strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	used := map[string]bool{}
	a := p.Analyze()
	for _, s := range a.Samples {
		used[strings.ToLower(s.Name)] = true
	}
	var out []string
	p.walk(func(rel, abs string, info os.FileInfo) {
		low := strings.ToLower(rel)
		if !want[filepath.Ext(low)] || strings.HasPrefix(low, "audio/") || strings.HasPrefix(low, "backup/") || strings.HasPrefix(low, "metadata/") {
			return
		}
		if used[strings.ToLower(flp.BaseName(rel))] {
			return
		}
		out = append(out, rel)
	})
	sort.Strings(out)
	return out
}

// TagRenders подписывает новые рендеры проекта. all — подписать и старые. Возвращает подписанные файлы.
func TagRenders(cfg *Config, p *Project, all bool) ([]string, error) {
	ms := cfg.Meta
	if ms.Off && !all {
		return nil, nil
	}
	m := LoadTrackMeta(p.Dir)
	if m.Title == "" {
		m.Title = strings.TrimSuffix(filepath.Base(mainFLP(p.Dir)), filepath.Ext(mainFLP(p.Dir)))
	}
	var cover *coverImg
	if !ms.NoCover {
		cover = loadCover(p.Dir)
	}
	metaMu.Lock()
	defer metaMu.Unlock()
	var st metaState
	readJSON(metaStatePath(p.Dir), &st)
	if st.Files == nil {
		st.Files = map[string]int64{}
	}
	first := !st.Started
	var done []string
	var lastErr error
	for _, rel := range renderCandidates(p, ms.Extensions()) {
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		mt := fi.ModTime().Unix()
		if t, ok := st.Files[rel]; ok && t == mt {
			continue // уже подписан и с тех пор не менялся
		}
		if first && !all && !ms.All {
			st.Files[rel] = mt // был до включения — не трогаем
			continue
		}
		if time.Since(fi.ModTime()) < 4*time.Second && !all { // FL ещё пишет файл
			continue
		}
		if err := TagFile(abs, m, cover); err != nil {
			lastErr = fmt.Errorf("%s: %w", rel, err)
			st.Files[rel] = mt
			continue
		}
		if fi2, err := os.Stat(abs); err == nil {
			st.Files[rel] = fi2.ModTime().Unix()
		}
		done = append(done, rel)
	}
	st.Started = true
	os.MkdirAll(filepath.Dir(metaStatePath(p.Dir)), 0o755)
	writeJSON(metaStatePath(p.Dir), st)
	return done, lastErr
}

// MetaWatcher раз в несколько секунд смотрит, не появился ли в проектах новый рендер, и подписывает его.
// Чтобы не нагружать компьютер, полный разбор проекта идёт, только если в папке появился (или изменился) аудиофайл.
func (s *Server) MetaWatcher(every time.Duration) {
	seen := map[string]time.Time{}
	for {
		time.Sleep(every)
		if s.cfg.Meta.Off {
			continue
		}
		for _, lp := range append([]LocalProject{}, s.cfg.Projects...) {
			if !dirExists(lp.Path) {
				continue
			}
			newest := newestRender(lp.Path, s.cfg.Meta.Extensions())
			if last, ok := seen[lp.Path]; ok && !newest.After(last) {
				continue
			}
			p := &Project{Dir: lp.Path, Cfg: s.cfg}
			done, _ := TagRenders(s.cfg, p, false)
			for _, rel := range done {
				LogActivity(lp.Path, "ok", "Метаданные записаны в рендер "+rel)
			}
			if time.Since(newest) > 5*time.Second { // файл ещё пишется — проверим на следующем круге
				seen[lp.Path] = newestRender(lp.Path, s.cfg.Meta.Extensions())
			}
		}
	}
}

// newestRender — самое свежее время изменения аудио (нужных расширений) в папке проекта и папках на 2 уровня вниз,
// кроме Audio/, Backup/ и служебных. Дёшево: без разбора .flp.
func newestRender(dir string, exts []string) time.Time {
	want := map[string]bool{}
	for _, e := range exts {
		want["."+strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	var best time.Time
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() {
				if depth < 2 && !strings.HasPrefix(n, ".") && !(depth == 0 && (strings.EqualFold(n, "Audio") || strings.EqualFold(n, "Backup") || strings.EqualFold(n, metaDir))) {
					walk(filepath.Join(d, n), depth+1)
				}
				continue
			}
			if want[strings.ToLower(filepath.Ext(n))] {
				if fi, err := e.Info(); err == nil && fi.ModTime().After(best) {
					best = fi.ModTime()
				}
			}
		}
	}
	walk(dir, 0)
	return best
}

// ---------- README.md проекта для GitHub ----------

const readmeMark = "<!-- FruityHub: этот файл собирается сам из метаданных проекта. Чтобы писать свой README, удали эту строку. -->"

// WriteReadme собирает README.md проекта (если его не писали руками). Возвращает true, если файл изменился.
func (p *Project) WriteReadme(a *Analysis) bool {
	path := filepath.Join(p.Dir, "README.md")
	if old, err := os.ReadFile(path); err == nil && !bytes.Contains(old, []byte(readmeMark)) {
		return false // README написан руками — не трогаем
	}
	m := LoadTrackMeta(p.Dir)
	title := m.Title
	if title == "" {
		if f := mainFLP(p.Dir); f != "" {
			title = strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		} else {
			title = filepath.Base(p.Dir)
		}
	}
	var b strings.Builder
	b.WriteString(readmeMark + "\n\n")
	if c := coverPath(p.Dir); c != "" {
		fmt.Fprintf(&b, "<img src=\"%s/%s\" alt=\"Обложка\" width=\"320\">\n\n", metaDir, filepath.Base(c))
	}
	b.WriteString("# " + mdEsc(title) + "\n\n")
	if m.Artist != "" {
		b.WriteString("**" + mdEsc(m.Artist) + "**\n\n")
	}
	var rows [][2]string
	add := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			rows = append(rows, [2]string{k, v})
		}
	}
	add("Альбом", m.Album)
	add("Год", m.Year)
	add("Жанр", m.Genre)
	add("Номер трека", m.Track)
	if f := mainFLP(p.Dir); f != "" {
		if info, err := parseFLPCached(f); err == nil && info.FLVersion != "" {
			add("FL Studio", info.FLVersion)
		}
		add("Проект", filepath.Base(f))
	}
	if len(rows) > 0 {
		b.WriteString("| | |\n|---|---|\n")
		for _, r := range rows {
			b.WriteString("| " + r[0] + " | " + mdEsc(r[1]) + " |\n")
		}
		b.WriteString("\n")
	}
	if m.Comment != "" {
		b.WriteString(mdEsc(m.Comment) + "\n\n")
	}
	if a != nil {
		var names []string
		seen := map[string]bool{}
		for _, pl := range a.Plugins {
			n := pl.Name
			if n == "" {
				n = pl.Internal
			}
			if n == "" || pl.Format == "FL" || seen[strings.ToLower(n)] {
				continue
			}
			seen[strings.ToLower(n)] = true
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
		if len(names) > 0 {
			b.WriteString("## Плагины\n\n")
			for _, n := range names {
				b.WriteString("- " + mdEsc(n) + "\n")
			}
			b.WriteString("\nПодробнее, у кого какие версии — в [PLUGINS.txt](PLUGINS.txt).\n\n")
		}
	}
	b.WriteString("---\n\nПроект FL Studio, синхронизируется через FruityHub: открой его в FruityHub, чтобы скачать вместе с аудио и плагинами.\n")
	nw := []byte(b.String())
	if old, _ := os.ReadFile(path); bytes.Equal(old, nw) {
		return false
	}
	return os.WriteFile(path, nw, 0o644) == nil
}

func mdEsc(s string) string {
	r := strings.NewReplacer("|", "\\|", "<", "&lt;", ">", "&gt;", "\n", " ")
	return r.Replace(strings.TrimSpace(s))
}
