package flp

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"io"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

// Keep the shipped expressions as a test-only compatibility oracle. Fixtures
// include binary/invalid UTF-8, Unicode folding, greedy matches and boundaries.
var legacyAudioRe = regexp.MustCompile(`(?i)[^\x00-\x1f"*<>|?]{1,400}\.(wav|mp3|flac|ogg|aif|aiff|m4a|wv|rx2|sf2|rex|opus)`)
var legacyBase64Re = regexp.MustCompile(`[A-Za-z0-9+/]{256,}={0,2}`)

func legacyFindPaths(b []byte) []string {
	var out []string
	for _, m := range legacyAudioRe.FindAll(b, -1) {
		s := strings.TrimSpace(string(m))
		if strings.ContainsAny(s, `/\`) || len(s) > 4 {
			if i := strings.LastIndexAny(s, "\x7f"); i >= 0 {
				s = s[i+1:]
			}
			out = append(out, s)
		}
	}
	return out
}

func comparePathScanner(t testing.TB, data []byte) {
	t.Helper()
	got, want := findPaths(data), legacyFindPaths(data)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input %q\ngot  %q\nwant %q", data, got, want)
	}
}

func TestPathScannerCompatibility(t *testing.T) {
	for _, s := range []string{
		"", ".wav", "x.wav", "a.aiff", "x.WAV y.MP3 z.flac", "\x00C:/Samples/кик.wav\x00",
		`"C:\Samples\a.wav" "D:\Samples\b.opus"`, "a.wav\x7fb.ogg", "x.ſf2 y.opuſ",
		"x.wav?y.mp3|z.ogg*a.flac<b.m4a>c.rx2", "\xff\xfe/a.WAV\x00b.ogg",
		strings.Repeat("я", 399) + ".wav.mp3", strings.Repeat("я", 400) + ".wav.mp3",
		strings.Repeat("x", 401) + ".wav", strings.Repeat("x", 1000) + ".wav y.opus",
		"a.wav" + strings.Repeat("x", 390) + ".mp3" + strings.Repeat("y", 400) + ".flac",
		"a\x00.wav b.wav", "\u2003a.wav\u2003", strings.Repeat("!", 2048),
	} {
		comparePathScanner(t, []byte(s))
	}
	rng := rand.New(rand.NewSource(73))
	alphabets := [][]byte{[]byte("abcXYZ./\\ \x00\x1f\x7f\"*<>|?"), []byte("abc.!"), []byte("яſ音🎹.wav\xff\xfe")}
	extensions := []string{".wav", ".MP3", ".aiff", ".flac", ".opuſ", ".ſf2", ".wv", ".m4a", ".rx2", ".rex", ".ogg"}
	for i := 0; i < 500; i++ {
		data := make([]byte, rng.Intn(1400))
		rng.Read(data)
		if i%4 != 0 {
			alphabet := alphabets[i%len(alphabets)]
			for j := range data {
				data[j] = alphabet[rng.Intn(len(alphabet))]
			}
		}
		for j := 0; j < 8 && len(data) > 0; j++ {
			copy(data[rng.Intn(len(data)):], extensions[rng.Intn(len(extensions))])
		}
		comparePathScanner(t, data)
	}
}

func FuzzPathScannerCompatibility(f *testing.F) {
	for _, s := range []string{"a.wav b.mp3", strings.Repeat("я", 401) + ".flac", "\xff\xfe.opuſ", "a.aiff\x00b.sf2"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			t.Skip()
		}
		comparePathScanner(t, data)
		if got, want := base64Runs(data), legacyBase64Re.FindAll(data, 32); !reflect.DeepEqual(got, want) {
			t.Fatalf("base64 input %q\ngot %q\nwant %q", data, got, want)
		}
	})
}

func TestBase64ScannerCompatibility(t *testing.T) {
	inputs := [][]byte{nil, []byte("short"), bytes.Repeat([]byte("A"), 255)}
	for _, n := range []int{256, 257, 1024} {
		for _, padding := range []string{"", "=", "==", "===", "==!BBBB"} {
			inputs = append(inputs, append(bytes.Repeat([]byte("A"), n), padding...))
		}
	}
	inputs = append(inputs, bytes.Repeat(append(bytes.Repeat([]byte("X+/0"), 64), '!'), 40))
	rng := rand.New(rand.NewSource(91))
	for i := 0; i < 200; i++ {
		data := make([]byte, 2048)
		for j := range data {
			alphabet := "aZ+/01=!"
			data[j] = alphabet[rng.Intn(len(alphabet))]
		}
		if i%2 == 0 {
			copy(data[100:], bytes.Repeat([]byte("Q"), 300+i))
		}
		inputs = append(inputs, data)
	}
	for _, data := range inputs {
		if got, want := base64Runs(data), legacyBase64Re.FindAll(data, 32); !reflect.DeepEqual(got, want) {
			t.Fatalf("base64 input %q\ngot %q\nwant %q", data, got, want)
		}
	}
}

func TestEmbeddedPathsEncodings(t *testing.T) {
	path := "C:/Bench/Samples/one.wav"
	plain := append([]byte{0}, []byte(path)...)
	plain = append(plain, 0)
	// Prevent the compressed/base64 fixture from falling below the historical
	// 256-character base64 threshold. No dots or path delimiters in this noise.
	rng := rand.New(rand.NewSource(27))
	for i := 0; i < 2048; i++ {
		plain = append(plain, byte('a'+rng.Intn(26)))
	}
	compress := func(newWriter func(io.Writer) io.WriteCloser) []byte {
		var buf bytes.Buffer
		w := newWriter(&buf)
		if _, err := w.Write(plain); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return append([]byte{0}, buf.Bytes()...)
	}
	zipped := compress(func(w io.Writer) io.WriteCloser { return zlib.NewWriter(w) })
	gzipped := compress(func(w io.Writer) io.WriteCloser { return gzip.NewWriter(w) })
	var wide []byte
	for _, r := range utf16.Encode([]rune("\x00" + path + "\x00")) {
		wide = append(wide, byte(r), byte(r>>8))
	}
	for name, data := range map[string][]byte{
		"plain": plain, "utf16": wide, "zlib": zipped, "gzip": gzipped,
		"base64":      []byte(base64.StdEncoding.EncodeToString(plain)),
		"base64-zlib": []byte(base64.StdEncoding.EncodeToString(zipped)),
	} {
		t.Run(name, func(t *testing.T) {
			if got := embeddedPaths(data); !reflect.DeepEqual(got, []string{path}) {
				t.Fatalf("got %q, want %q", got, path)
			}
		})
	}
}

func TestUnpackStreamsReuseAfterErrors(t *testing.T) {
	for _, kind := range []string{"zlib", "gzip"} {
		t.Run(kind, func(t *testing.T) {
			pack := func(plain []byte) []byte {
				var buf bytes.Buffer
				var w io.WriteCloser
				if kind == "gzip" {
					w = gzip.NewWriter(&buf)
				} else {
					w = zlib.NewWriter(&buf)
				}
				if _, err := w.Write(plain); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				return buf.Bytes()
			}
			first := []byte("C:/Samples/first.wav\x00" + strings.Repeat("a", 80))
			second := []byte("C:/Samples/second.flac\x00" + strings.Repeat("b", 90))
			bad := []byte{0x78, 0x9c, 0xff, 0xff, 0xff}
			if kind == "gzip" {
				bad = []byte{0x1f, 0x8b, 8, 0xff, 0, 0, 0, 0, 0, 0}
			}
			// A good stream, a failed decode/reset, then another good stream.
			var blob []byte
			for _, part := range [][]byte{bad, pack(first), bad, pack(second)} {
				blob = append(blob, part...)
				blob = append(blob, 0)
			}
			if got := unpackStreams(blob); !reflect.DeepEqual(got, [][]byte{first, second}) {
				t.Fatalf("reused reader lost data: %q", got)
			}
			for _, attempts := range []int{63, 64} {
				data := append(bytes.Repeat(bad, attempts), pack(first)...)
				got := unpackStreams(data)
				if attempts == 63 && !reflect.DeepEqual(got, [][]byte{first}) {
					t.Fatalf("stream after 63 bad headers: %q", got)
				}
				if attempts == 64 && len(got) != 0 {
					t.Fatal("exceeded 64 stream attempts")
				}
			}
		})
	}
}

func TestBase64DecodeCompatibility(t *testing.T) {
	for n := 256; n <= 270; n++ {
		for _, padding := range []string{"", "=", "==", "==="} {
			data := []byte(strings.Repeat("a", n) + padding)
			m := bytes.TrimRight(legacyBase64Re.Find(data), "=")
			decoded, err := base64.StdEncoding.DecodeString(string(m) + strings.Repeat("=", (4-len(m)%4)%4))
			var want [][]byte
			if err == nil && len(decoded) > 32 {
				want = [][]byte{decoded}
			}
			if got := unpackStreams(data); !reflect.DeepEqual(got, want) {
				t.Fatalf("base64 len=%d padding=%q: got %q, want %q", n, padding, got, want)
			}
		}
	}
}
