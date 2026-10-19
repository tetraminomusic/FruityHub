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
	"encoding/binary"
	"errors"
	"fmt"
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
	Samples   []string `json:"samples"` // explicit channel / audio clip paths
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
	for pos < end {
		id := b[pos]
		pos++
		var n int
		switch {
		case id < 64:
			n = 1
		case id < 128:
			n = 2
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
// (Slicex, DirectWave, Fruity Slicer, FPC...). Heuristic but cheap.
func embeddedPaths(d []byte) []string {
	var out []string
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
		out = append(out, findPaths(a)...)
	}
	out = append(out, findPaths(d)...)
	return out
}

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
	info := Info{FLVersion: f.Version()}
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
