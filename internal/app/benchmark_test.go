package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

var benchmarkNamesSink []string
var benchmarkCountSink int64
var benchmarkAnalysisSink *Analysis
var benchmarkNameSink string

func benchmarkProject(b *testing.B, count int) *Project {
	b.Helper()
	root := b.TempDir()
	for i := 0; i < count; i++ {
		dir := filepath.Join(root, fmt.Sprintf("Audio-%02d", i%20))
		if err := os.MkdirAll(dir, 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("sample-%d.wav", i)), []byte{0}, 0600); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Generated.flp"), synthFLP("kick.wav"), 0600); err != nil {
		b.Fatal(err)
	}
	return &Project{Dir: root}
}

func BenchmarkProjectFLPs(b *testing.B) {
	for _, count := range []int{100, 5000} {
		b.Run(fmt.Sprintf("Files%d", count), func(b *testing.B) {
			p := benchmarkProject(b, count)
			if got := p.FLPs(); len(got) != 1 {
				b.Fatal(got)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkNamesSink = p.FLPs()
			}
		})
	}
}

func BenchmarkProjectAnalyze(b *testing.B) {
	for _, count := range []int{100, 5000} {
		b.Run(fmt.Sprintf("Files%d", count), func(b *testing.B) {
			p := benchmarkProject(b, count)
			if a := p.Analyze(); len(a.FLPs) != 1 || len(a.Audio) != count {
				b.Fatal("invalid analysis fixture")
			}
			// Measure a warm FLP cache, without asynchronous cache-file writes.
			flpCacheMu.Lock()
			if flpSaveTimer != nil {
				flpSaveTimer.Stop()
				flpSaveTimer = nil
			}
			flpCacheMu.Unlock()
			b.Cleanup(func() {
				flpCacheMu.Lock()
				delete(flpCache, filepath.Join(p.Dir, "Generated.flp"))
				flpCacheMu.Unlock()
			})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkAnalysisSink = p.Analyze()
			}
		})
	}
}

func BenchmarkProjectMainFLP(b *testing.B) {
	for _, count := range []int{100, 5000} {
		b.Run(fmt.Sprintf("Files%d", count), func(b *testing.B) {
			p := benchmarkProject(b, count)
			if p.MainFLP() != "Generated.flp" {
				b.Fatal("invalid main FLP fixture")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkNameSink = p.MainFLP()
			}
		})
	}
}

func BenchmarkFLPCacheHit(b *testing.B) {
	path := filepath.Join(b.TempDir(), "Generated.flp")
	if err := os.WriteFile(path, synthFLP("kick.wav"), 0600); err != nil {
		b.Fatal(err)
	}
	if _, err := parseFLPCached(path); err != nil {
		b.Fatal(err)
	}
	// Leave no asynchronous save referencing a deleted benchmark directory.
	b.Cleanup(func() {
		flpCacheMu.Lock()
		defer flpCacheMu.Unlock()
		if flpSaveTimer != nil {
			flpSaveTimer.Stop()
			flpSaveTimer = nil
		}
		delete(flpCache, path)
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseFLPCached(path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArchive(b *testing.B) {
	for _, kind := range []string{"random", "mixed", "repeated"} {
		d := make([]byte, 4<<20)
		rand.New(rand.NewSource(42)).Read(d)
		if kind == "repeated" {
			copy(d, bytes.Repeat([]byte("synthetic preset data\x00"), len(d)/22+1))
		} else if kind == "mixed" {
			for i := 0; i < len(d); i += 4096 {
				copy(d[i:i+2048], bytes.Repeat([]byte{0}, 2048))
			}
		}
		for _, method := range []string{"store", "deflate", "zstd"} {
			b.Run(kind+"/"+method, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(d)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					out := &benchmarkCountingWriter{}
					zw := zip.NewWriter(out)
					fastZip(zw, method == "zstd")
					name := "Generated.dll"
					if method == "store" {
						name = "Generated.flac"
					}
					w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zipMethodFor(zw, name, int64(len(d)))})
					if err == nil {
						_, err = io.Copy(w, bytes.NewReader(d))
					}
					closeErr := zw.Close()
					doneZip(zw)
					if err != nil || closeErr != nil {
						b.Fatalf("archive: %v / %v", err, closeErr)
					}
					benchmarkCountSink = out.n
				}
				b.ReportMetric(float64(benchmarkCountSink)/float64(len(d)), "output/input")
			})
		}
	}
}

type benchmarkCountingWriter struct{ n int64 }

func (w *benchmarkCountingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
