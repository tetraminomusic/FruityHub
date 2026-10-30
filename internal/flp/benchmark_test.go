package flp

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// Generated inputs only: never open a user's projects for benchmarks.
func benchmarkBlob(kind string, size int) []byte {
	d := make([]byte, size)
	rand.New(rand.NewSource(42)).Read(d)
	switch kind {
	case "text-no-path":
		for i := range d {
			d[i] = '!' // printable, but neither a path nor base64
		}
	case "utf16":
		for i := 1; i < len(d); i += 2 {
			d[i] = 0
		}
	case "zlib", "base64":
		plain := bytes.Repeat([]byte("\x00C:/Bench/Samples/kick.wav\x00"), size/26+1)
		var compressed bytes.Buffer
		w := zlib.NewWriter(&compressed)
		if _, err := w.Write(plain[:size]); err != nil {
			panic(err)
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
		if kind == "base64" {
			return []byte(base64.StdEncoding.EncodeToString(compressed.Bytes()))
		}
		return compressed.Bytes()
	}
	return d
}

func benchmarkFLP(events, plugins, blobSize int) []byte {
	f := &File{PPQ: 96, Channels: uint16(plugins), utf16: true}
	f.Events = append(f.Events, Event{ID: evFLVersion, Data: []byte("24.1.1.4234\x00")})
	for i := 0; i < events; i++ {
		f.Events = append(f.Events, Event{ID: 0, Data: []byte{1}})
	}
	for i := 0; i < plugins; i++ {
		blob := benchmarkBlob("random", blobSize)
		copy(blob, []byte("\x00C:/Bench/Samples/embedded.wav\x00"))
		f.Events = append(f.Events,
			Event{ID: evChanNew, Data: []byte{byte(i), 0}},
			Event{ID: evSamplePath, Data: f.encodeText(fmt.Sprintf("C:/Bench/Samples/sample-%d.wav", i))},
			Event{ID: evPluginInternal, Data: f.encodeText("Benchmark Synth")},
			Event{ID: evPluginData, Data: blob},
		)
	}
	return f.Bytes()
}

var benchmarkFileSink *File
var benchmarkInfoSink Info
var benchmarkBytesSink []byte
var benchmarkPathsSink []string
var benchmarkStreamsSink [][]byte

func BenchmarkFLP(b *testing.B) {
	for _, tc := range []struct {
		name                   string
		events, plugins, bytes int
	}{
		{"Small", 1000, 8, 4 << 10},
		{"Blob1MiB", 1000, 16, 64 << 10},
		{"Blob16MiB", 1000, 16, 1 << 20},
		{"Events100k", 100000, 8, 1024},
	} {
		b.Run(tc.name, func(b *testing.B) {
			raw := benchmarkFLP(tc.events, tc.plugins, tc.bytes)
			f, err := Parse(raw)
			if err != nil {
				b.Fatal(err)
			}
			info := f.Info()
			if len(info.Samples) != tc.plugins || len(info.Plugins) != tc.plugins || len(info.Embedded) == 0 {
				b.Fatal("invalid synthetic fixture")
			}
			path := filepath.Join(b.TempDir(), "Generated.flp")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				b.Fatal(err)
			}
			for _, stage := range []string{"Parse", "Info", "ParseInfo", "ReadParseInfo", "Bytes"} {
				b.Run(stage, func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(raw)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						switch stage {
						case "Parse":
							benchmarkFileSink, err = Parse(raw)
						case "Info":
							benchmarkInfoSink = f.Info()
						case "ParseInfo", "ReadParseInfo":
							var parsed *File
							if stage == "ReadParseInfo" {
								parsed, err = ParseFile(path)
							} else {
								parsed, err = Parse(raw)
							}
							if err == nil {
								benchmarkInfoSink = parsed.Info()
							}
						case "Bytes":
							benchmarkBytesSink = f.Bytes()
						}
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

func BenchmarkBlob(b *testing.B) {
	for _, kind := range []string{"random", "text-no-path", "utf16", "zlib", "base64"} {
		b.Run(kind, func(b *testing.B) {
			d := benchmarkBlob(kind, 1<<20)
			for _, stage := range []string{"Embedded", "FindPaths", "Unpack"} {
				b.Run(stage, func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(d)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						switch stage {
						case "Embedded":
							benchmarkPathsSink = embeddedPaths(d)
						case "FindPaths":
							benchmarkPathsSink = findPaths(d)
						case "Unpack":
							benchmarkStreamsSink = unpackStreams(d)
						}
					}
				})
			}
		})
	}
}
