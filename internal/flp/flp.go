// Package flp reads (and minimally rewrites) FL Studio project files.
//
// Format: "FLhd" chunk (6 bytes: format, channels, ppq) followed by an
// "FLdt" chunk holding a flat stream of events. Every event is one id byte
// followed by a payload whose size depends on the id range:
//
//	  0..63   1 byte
//	 64..127  2 bytes
//	128..191  4 bytes
//	192..255  varint length + data (text / blobs)
//
// We only need a handful of them: sample paths, plugin names, VST wrapper
// data and the FL version. Everything else is passed through untouched,
// which keeps rewriting safe.
package flp

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	evChanNew         = 64      // WORD: new channel (index)
	evSamplePath      = 192 + 4 // 196 TEXT: channel sample filename
	evFLVersion       = 192 + 7 // 199 TEXT (ascii)
	evPluginInternal  = 192 + 9 // 201 TEXT: e.g. "Fruity Wrapper", "Sytrus"
	evPluginName      = 192 + 11
	evPluginData      = 208 + 5 // 213 DATA: plugin state (VST wrapper info lives here)
	evMixerSlotIndex  = 98      // WORD: mixer slot index (marks plugin belongs to mixer)
	wrapperName       = 54
	wrapperPluginPath = 55
	wrapperVendor     = 56
)

// Event is one raw FLP event.
type Event struct {
	ID   byte
	Data []byte // for fixed-size events: little-endian value bytes
}

// File is a parsed FLP file.
type File struct {
	Format   int16
	Channels uint16
	PPQ      uint16
	Events   []Event
	utf16    bool
}

// Plugin is a plugin instance found in the project.
type Plugin struct {
	Name     string `json:"name"`               // display / VST name
	Internal string `json:"internal"`           // FL internal name ("Fruity Wrapper" for VST)
	Vendor   string `json:"vendor,omitempty"`   // VST vendor
	Path     string `json:"path,omitempty"`     // plugin binary path on the saving machine
	Format   string `json:"format"`             // FL, VST2, VST3, AU, CLAP
	Where    string `json:"where"`              // "channel" or "mixer"
	UserName string `json:"userName,omitempty"` // user-renamed channel name
}

// Info is what the app needs from a project.
type Info struct {
	FLVersion string   `json:"flVersion"`
	Title     string   `json:"title,omitempty"`
	Artist    string   `json:"artist,omitempty"`
	Tempo     float64  `json:"tempo,omitempty"`    // темп в начале проекта (BPM); автоматизация темпа не учитывается
	Length    float64  `json:"length,omitempty"`   // длительность, секунды: по выделению в плейлисте или до конца последнего клипа
	LengthBy  string   `json:"lengthBy,omitempty"` // selection | playlist
	Samples   []string `json:"samples"`            // explicit channel / audio clip paths
	Embedded  []string `json:"embedded"`
	Plugins   []Plugin `json:"plugins"`
}

func readVarint(b []byte, pos int) (int, int, error) {
	v, shift := 0, 0
	for {
		if pos >= len(b) {
			return 0, pos, errors.New("unexpected end of file in varint")
		}
		c := b[pos]
		pos++
		v |= int(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, pos, nil
		}
		shift += 7
		if shift > 35 {
			return 0, pos, errors.New("varint too long")
		}
	}
}

func writeVarint(buf *bytes.Buffer, v int) {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			buf.WriteByte(c | 0x80)
		} else {
			buf.WriteByte(c)
			return
		}
	}
}

// Parse decodes raw FLP bytes.
func Parse(b []byte) (*File, error) {
	if len(b) < 22 || string(b[0:4]) != "FLhd" {
		return nil, errors.New("не похоже на .flp (нет заголовка FLhd)")
	}
	hsize := binary.LittleEndian.Uint32(b[4:8])
	if hsize != 6 {
		return nil, fmt.Errorf("неожиданный размер заголовка %d", hsize)
	}
	f := &File{
		Format:   int16(binary.LittleEndian.Uint16(b[8:10])),
		Channels: binary.LittleEndian.Uint16(b[10:12]),
		PPQ:      binary.LittleEndian.Uint16(b[12:14]),
	}
	if string(b[14:18]) != "FLdt" {
		return nil, errors.New("нет блока FLdt")
	}
	pos := 22
	end := len(b)
	if dsz := int(binary.LittleEndian.Uint32(b[18:22])); 22+dsz <= len(b) {
		end = 22 + dsz
	}
	major := 0 // версия FL из события 199 — у FL 2026 событие 172 занимает 1 байт, а не 4
	for pos < end {
		id := b[pos]
		pos++
		var n int
		switch {
		case id < 64:
			n = 1
		case id < 128:
			n = 2
		case id == 172 && major >= 26:
			n = 1
		case id < 192:
			n = 4
		default:
			var err error
			n, pos, err = readVarint(b, pos)
			if err != nil {
				return nil, err
			}
		}
		if pos+n > end {
			return nil, fmt.Errorf("событие %d выходит за конец файла", id)
		}
		f.Events = append(f.Events, Event{ID: id, Data: b[pos : pos+n]})
		if id == evFLVersion && major == 0 {
			v := strings.TrimRight(string(b[pos:pos+n]), "\x00")
			major, _ = strconv.Atoi(strings.SplitN(v, ".", 2)[0])
		}
		pos += n
	}
	// Text encoding: UTF-16LE since FL 11.5.
	f.utf16 = true
	if v := f.Version(); v != "" {
		parts := strings.Split(v, ".")
		if len(parts) >= 2 {
			maj, _ := strconv.Atoi(parts[0])
			min, _ := strconv.Atoi(parts[1])
			f.utf16 = maj > 11 || (maj == 11 && min >= 5)
		}
	}
	return f, nil
}

// ParseFile reads and parses a .flp from disk.
func ParseFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Bytes encodes the file back to FLP bytes.
func (f *File) Bytes() []byte {
	var body bytes.Buffer
	for _, e := range f.Events {
		body.WriteByte(e.ID)
		if e.ID >= 192 {
			writeVarint(&body, len(e.Data))
		}
		body.Write(e.Data)
	}
	var out bytes.Buffer
	out.WriteString("FLhd")
	binary.Write(&out, binary.LittleEndian, uint32(6))
	binary.Write(&out, binary.LittleEndian, f.Format)
	binary.Write(&out, binary.LittleEndian, f.Channels)
	binary.Write(&out, binary.LittleEndian, f.PPQ)
	out.WriteString("FLdt")
	binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// Version returns the FL Studio version that saved the project.
func (f *File) Version() string {
	for _, e := range f.Events {
		if e.ID == evFLVersion {
			return strings.TrimRight(string(e.Data), "\x00")
		}
	}
	return ""
}

func (f *File) text(d []byte) string {
	if f.utf16 && len(d)%2 == 0 {
		u := make([]uint16, 0, len(d)/2)
		for i := 0; i+1 < len(d); i += 2 {
			u = append(u, binary.LittleEndian.Uint16(d[i:]))
		}
		return strings.TrimRight(string(utf16.Decode(u)), "\x00")
	}
	return strings.TrimRight(string(d), "\x00")
}

func (f *File) encodeText(s string) []byte {
	if !f.utf16 {
		return append([]byte(s), 0)
	}
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2+2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return append(b, 0, 0)
}

// AudioExts are file extensions we consider "audio / sample data".
var AudioExts = []string{".wav", ".mp3", ".flac", ".ogg", ".aif", ".aiff", ".m4a", ".wv", ".rx2", ".sf2", ".rex", ".opus", ".mid", ".midi", ".ds", ".dwp", ".fst", ".zip"}

var audioRe = regexp.MustCompile(`(?i)[^\x00-\x1f"*<>|?]{1,400}\.(wav|mp3|flac|ogg|aif|aiff|m4a|wv|rx2|sf2|rex|opus)`)

// embeddedPaths looks for audio file paths stored inside plugin blobs
// (Slicex, DirectWave, Fruity Slicer, FPC, and VST/AU states — Pigments, Serum, Kontakt…).
// Many third-party plugins keep their state compressed (zlib/gzip) or base64-encoded:
// such streams are unpacked and searched too. Heuristic but cheap.
func embeddedPaths(d []byte) []string {
	seen := map[string]bool{}
	var out []string
	add := func(l []string) {
		for _, p := range l {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	scanBlob(d, 0, add)
	return out
}

const maxUnpacked = 32 << 20

func scanBlob(d []byte, depth int, add func([]string)) {
	// UTF-16LE: drop zero high bytes when the blob looks like UTF-16
	if bytes.Count(d, []byte{0}) > len(d)/4 {
		var a []byte
		for i := 0; i+1 < len(d); i += 2 {
			if d[i+1] == 0 && d[i] >= 0x20 {
				a = append(a, d[i])
			} else {
				a = append(a, 0)
			}
		}
		add(findPaths(a))
	}
	add(findPaths(d))
	if depth >= 2 {
		return
	}
	for _, u := range unpackStreams(d) {
		scanBlob(u, depth+1, add)
	}
}

// unpackStreams finds zlib / gzip / base64 streams inside a blob and returns them unpacked.
func unpackStreams(d []byte) [][]byte {
	var out [][]byte
	total := 0
	keep := func(b []byte) {
		if len(b) > 0 && total+len(b) <= maxUnpacked {
			total += len(b)
			out = append(out, b)
		}
	}
	read := func(r io.Reader) []byte {
		b, _ := io.ReadAll(io.LimitReader(r, maxUnpacked))
		return b
	}
	tries := 0
	for i := 0; i+2 < len(d) && tries < 64; i++ {
		switch {
		case d[i] == 0x1f && d[i+1] == 0x8b && d[i+2] == 8: // gzip
			tries++
			if zr, err := gzip.NewReader(bytes.NewReader(d[i:])); err == nil {
				if b := read(zr); len(b) > 16 {
					keep(b)
				}
			}
		case d[i] == 0x78 && (d[i+1] == 0x01 || d[i+1] == 0x5e || d[i+1] == 0x9c || d[i+1] == 0xda) && (uint16(d[i])<<8|uint16(d[i+1]))%31 == 0: // zlib
			tries++
			if zr, err := zlib.NewReader(bytes.NewReader(d[i:])); err == nil {
				if b := read(zr); len(b) > 16 {
					keep(b)
				}
			}
		}
	}
	// base64 (длинные куски из букв, цифр, +/=) — некоторые плагины хранят так состояние в XML
	for _, m := range b64Re.FindAll(d, 32) {
		if b, err := base64.StdEncoding.DecodeString(string(bytes.TrimRight(m, "=")) + pad(len(bytes.TrimRight(m, "=")))); err == nil && len(b) > 32 {
			keep(b)
		}
	}
	return out
}

var b64Re = regexp.MustCompile(`[A-Za-z0-9+/]{256,}={0,2}`)

func pad(n int) string { return strings.Repeat("=", (4-n%4)%4) }

func findPaths(b []byte) []string {
	var out []string
	for _, m := range audioRe.FindAll(b, -1) {
		s := strings.TrimSpace(string(m))
		if strings.ContainsAny(s, `/\`) || len(s) > 4 {
			// keep only the part that looks like a path or filename
			if i := strings.LastIndexAny(s, "\x7f"); i >= 0 {
				s = s[i+1:]
			}
			out = append(out, s)
		}
	}
	return out
}

func formatFromPath(p string) string {
	l := strings.ToLower(strings.TrimRight(p, `/\`))
	switch {
	case strings.HasSuffix(l, ".vst3"):
		return "VST3"
	case strings.HasSuffix(l, ".component"):
		return "AU"
	case strings.HasSuffix(l, ".clap"):
		return "CLAP"
	case strings.HasSuffix(l, ".dll"), strings.HasSuffix(l, ".vst"):
		return "VST2"
	}
	return "VST"
}

func parseWrapper(d []byte) (name, vendor, path string) {
	if len(d) < 4 {
		return
	}
	pos := 4 // u32 kind
	for pos+12 <= len(d) {
		id := binary.LittleEndian.Uint32(d[pos:])
		sz := binary.LittleEndian.Uint64(d[pos+4:])
		pos += 12
		if sz > uint64(len(d)-pos) {
			return
		}
		v := strings.TrimRight(string(d[pos:pos+int(sz)]), "\x00")
		switch id {
		case wrapperName:
			name = v
		case wrapperVendor:
			vendor = v
		case wrapperPluginPath:
			path = v
		}
		pos += int(sz)
	}
	return
}

// Info extracts samples, plugins and version.
func (f *File) Info() Info {
	info := Info{FLVersion: f.Version(), Title: f.Title(), Artist: f.Artist(), Tempo: f.Tempo()}
	info.Length, info.LengthBy = f.LengthSeconds()
	seenS := map[string]bool{}
	seenE := map[string]bool{}
	where := "channel"
	var cur *Plugin
	flush := func() {
		if cur != nil && (cur.Internal != "" || cur.Name != "") {
			if cur.Name == "" {
				cur.Name = cur.Internal
			}
			// Sampler / audio clip channels are not plugins
			li := strings.ToLower(cur.Internal)
			if li != "" && li != "sampler" && li != "audio clip" && li != "automation clip" && li != "layer" {
				info.Plugins = append(info.Plugins, *cur)
			}
		}
		cur = nil
	}
	for _, e := range f.Events {
		switch e.ID {
		case evChanNew:
			flush()
			where = "channel"
		case evMixerSlotIndex:
			// slot index event closes a mixer slot's plugin
			if cur != nil {
				cur.Where = "mixer"
			}
			flush()
			where = "mixer"
		case evSamplePath:
			p := f.text(e.Data)
			if p != "" && !seenS[p] {
				seenS[p] = true
				info.Samples = append(info.Samples, p)
			}
		case evPluginInternal:
			flush()
			cur = &Plugin{Internal: f.text(e.Data), Format: "FL", Where: where}
		case evPluginName:
			if cur != nil {
				cur.UserName = f.text(e.Data)
			}
		case evPluginData:
			if cur != nil && strings.EqualFold(cur.Internal, "Fruity Wrapper") {
				n, v, p := parseWrapper(e.Data)
				cur.Name, cur.Vendor, cur.Path = n, v, p
				cur.Format = formatFromPath(p)
			}
			for _, p := range embeddedPaths(e.Data) {
				if !seenE[p] {
					seenE[p] = true
					info.Embedded = append(info.Embedded, p)
				}
			}
		}
	}
	flush()
	// Remove embedded paths that duplicate explicit ones
	var emb []string
	for _, p := range info.Embedded {
		if !seenS[p] {
			emb = append(emb, p)
		}
	}
	info.Embedded = emb
	return info
}

// UniquePlugins de-duplicates plugin instances by name+format.
func UniquePlugins(ps []Plugin) []Plugin {
	m := map[string]Plugin{}
	for _, p := range ps {
		k := strings.ToLower(p.Format + "|" + p.Name)
		if _, ok := m[k]; !ok {
			m[k] = p
		}
	}
	out := make([]Plugin, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Format != out[j].Format {
			return out[i].Format > out[j].Format
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// RewriteSamplePaths replaces channel sample paths using fn (return "" to keep).
// Returns number of changed paths.
func (f *File) RewriteSamplePaths(fn func(old string) string) int {
	n := 0
	for i, e := range f.Events {
		if e.ID != evSamplePath {
			continue
		}
		old := f.text(e.Data)
		if nw := fn(old); nw != "" && nw != old {
			f.Events[i].Data = f.encodeText(nw)
			n++
		}
	}
	return n
}

// BaseName returns the file name of a path written on any OS.
func BaseName(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// IsAudio reports whether a file name looks like audio/sample data.
func IsAudio(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range AudioExts {
		if e == ext && e != ".mid" && e != ".midi" && e != ".fst" && e != ".zip" {
			return true
		}
	}
	return false
}

// RewritePluginPaths changes the plugin binary path stored in VST/CLAP wrapper
// data (FL's plugin presets .fst and projects). Returns how many were changed.
func (f *File) RewritePluginPaths(fn func(old string) string) int {
	n := 0
	for i, e := range f.Events {
		if e.ID != evPluginData || len(e.Data) < 4 {
			continue
		}
		d := e.Data
		var out bytes.Buffer
		out.Write(d[:4])
		pos, changed, ok := 4, false, true
		for pos < len(d) {
			if pos+12 > len(d) {
				ok = false
				break
			}
			id := binary.LittleEndian.Uint32(d[pos:])
			sz := binary.LittleEndian.Uint64(d[pos+4:])
			if sz > uint64(len(d)-pos-12) {
				ok = false
				break
			}
			val := d[pos+12 : pos+12+int(sz)]
			if id == wrapperPluginPath {
				old := strings.TrimRight(string(val), "\x00")
				if nv := fn(old); nv != old {
					val = []byte(nv)
					changed = true
				}
			}
			binary.Write(&out, binary.LittleEndian, id)
			binary.Write(&out, binary.LittleEndian, uint64(len(val)))
			out.Write(val)
			pos += 12 + int(sz)
		}
		if ok && changed {
			f.Events[i].Data = out.Bytes()
			n++
		}
	}
	return n
}

const (
	evTitle   = 192 + 2  // 194 TEXT: Project info → Title
	evFLBuild = 128 + 31 // 159 DWORD: номер сборки FL
)

const (
	EvTitle   = evTitle
	EvGenre   = 192 + 14 // 206 TEXT: Project info → Genre
	EvArtists = 192 + 15 // 207 TEXT: Project info → Author (артист)
)

// Title — название проекта из окна Project info в FL Studio.
func (f *File) Title() string { return f.InfoText(evTitle) }

// Tempo — темп проекта (BPM): событие 156 (темп×1000, FL 12+), в старых проектах — 66 (целый темп) и 93 (поправка).
func (f *File) Tempo() float64 {
	var old, fine float64
	for _, e := range f.Events {
		switch {
		case e.ID == evTempo && len(e.Data) >= 4:
			if t := float64(binary.LittleEndian.Uint32(e.Data)) / 1000; t >= 10 && t <= 999 {
				return t
			}
		case e.ID == evTempoOld && len(e.Data) >= 2 && old == 0:
			old = float64(binary.LittleEndian.Uint16(e.Data))
		case e.ID == evTempoFine && len(e.Data) >= 2:
			fine = float64(int16(binary.LittleEndian.Uint16(e.Data))) / 1000
		}
	}
	if old >= 10 && old <= 999 {
		return old + fine
	}
	return 0
}

const (
	evTempo     = 128 + 28 // 156 DWORD: темп × 1000
	evTempoOld  = 64 + 2   // 66 WORD: темп (старые версии FL)
	evTempoFine = 64 + 29  // 93 WORD: поправка темпа в тысячных (старые версии)
)

// Artist — артист (поле Author в Project info).
func (f *File) Artist() string { return f.InfoText(EvArtists) }

// InfoText — текстовое поле Project info (название, артист, жанр).
func (f *File) InfoText(id byte) string {
	for _, e := range f.Events {
		if e.ID == id {
			return f.text(e.Data)
		}
	}
	return ""
}

// SetTitle пишет название в Project info. Если такого события нет, вставляет его сразу после версии FL
// (там, где его кладёт сама FL Studio). Возвращает true, если файл изменился.
func (f *File) SetTitle(s string) bool { return f.SetInfoText(evTitle, s) }

// SetArtist пишет артиста (Author) в Project info.
func (f *File) SetArtist(s string) bool { return f.SetInfoText(EvArtists, s) }

// SetInfoText пишет текстовое поле Project info. Новое событие кладётся после версии FL и уже
// записанных полей Project info (FL читает их в любом порядке, но так файл похож на сохранённый самой FL).
func (f *File) SetInfoText(id byte, s string) bool {
	data := f.encodeText(s)
	for i, e := range f.Events {
		if e.ID == id {
			if bytes.Equal(e.Data, data) {
				return false
			}
			f.Events[i].Data = data
			return true
		}
	}
	if s == "" {
		return false
	}
	at := 0
	for i, e := range f.Events {
		if i > 60 {
			break
		}
		if e.ID == evFLVersion || e.ID == evFLBuild || e.ID == evTitle || e.ID == EvGenre || e.ID == EvArtists {
			at = i + 1
		}
	}
	ev := Event{ID: id, Data: data}
	f.Events = append(f.Events[:at], append([]Event{ev}, f.Events[at:]...)...)
	return true
}

// Build — номер сборки FL, в которой сохранён проект (0, если не записан).
func (f *File) Build() uint32 {
	for _, e := range f.Events {
		if e.ID == evFLBuild && len(e.Data) == 4 {
			return binary.LittleEndian.Uint32(e.Data)
		}
	}
	return 0
}

// EmbeddedPathsForTest — для тестов приложения.
func EmbeddedPathsForTest(d []byte) []string { return embeddedPaths(d) }

// ---------- длительность трека ----------

const evTimeSelection = 192 + 25 // 217 DATA: выделение времени в плейлисте (красная полоса): начало и конец, тики
const evPlaylist = 208 + 25      // 233 DATA: клипы плейлиста

// Selection — выделение времени в плейлисте (красная полоса сверху), в тиках. ok=false — выделения нет.
func (f *File) Selection() (start, end int, ok bool) {
	for _, e := range f.Events {
		if e.ID == evTimeSelection && len(e.Data) >= 8 {
			s := int(int32(binary.LittleEndian.Uint32(e.Data)))
			en := int(int32(binary.LittleEndian.Uint32(e.Data[4:])))
			if en > s && s >= 0 {
				return s, en, true
			}
		}
	}
	return 0, 0, false
}

// PlaylistEnd — где кончается последний клип в плейлисте (тики). Размер записи клипа менялся
// от версии к версии FL (32, 60, 80, 88 байт) — подбираем тот, при котором записи выглядят разумно.
func (f *File) PlaylistEnd() int {
	best := 0
	for _, e := range f.Events {
		if e.ID != evPlaylist || len(e.Data) == 0 {
			continue
		}
		d := e.Data
		for _, stride := range []int{88, 80, 60, 32} {
			if len(d)%stride != 0 {
				continue
			}
			end, sane := 0, true
			for i := 0; i+stride <= len(d); i += stride {
				pos := binary.LittleEndian.Uint32(d[i:])
				base := binary.LittleEndian.Uint16(d[i+4:])
				ln := binary.LittleEndian.Uint32(d[i+8:])
				if base != 20480 || pos > 1<<26 || ln > 1<<26 { // у каждого клипа pattern base = 20480
					sane = false
					break
				}
				if int(pos+ln) > end {
					end = int(pos + ln)
				}
			}
			if sane {
				if end > best {
					best = end
				}
				break
			}
		}
	}
	return best
}

// LengthSeconds — длительность трека: по выделению в плейлисте, а если его нет — до конца последнего клипа.
// from: "selection" или "playlist" ("" — посчитать не вышло). Темп берётся начальный.
func (f *File) LengthSeconds() (sec float64, from string) {
	tempo := f.Tempo()
	if tempo <= 0 {
		tempo = 140 // FL не пишет темп, если он по умолчанию (140)
	}
	ppq := float64(f.PPQ)
	if ppq <= 0 {
		return 0, ""
	}
	ticks, from := 0, ""
	if s, e, ok := f.Selection(); ok {
		ticks, from = e-s, "selection"
	} else if e := f.PlaylistEnd(); e > 0 {
		ticks, from = e, "playlist"
	}
	if ticks <= 0 {
		return 0, ""
	}
	return float64(ticks) / ppq * 60 / tempo, from
}
