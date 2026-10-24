package app

import (
	"bytes"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flsync/internal/hub"
)

func directHub(t *testing.T) (cfgA, cfgB *Config, root string) {
	root = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	s, err := hub.New(hub.Options{DataDir: filepath.Join(root, "hub-data"), GitHubAPI: gh.Srv.URL, Code: "uzbek", StreamWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	cfgA, cfgB = &Config{Server: srv.URL}, &Config{Server: srv.URL}
	cfgA.Hub().Register("sender", "uzbek", "tokA")
	cfgB.Hub().Register("receiver", "uzbek", "tokB")
	return
}

func steps(j *Job) string { j.mu.Lock(); defer j.mu.Unlock(); return strings.Join(j.Steps, "\n") }

// В одной локальной сети плагин идёт напрямую, мимо сервера; данные проверяются по sha256.
func TestPluginDirectLAN(t *testing.T) {
	testLAN = []string{"127.0.0.1"}
	t.Cleanup(func() { testLAN = nil })
	cfgA, cfgB, root := directHub(t)
	rs, _ := cfgB.Hub().RequestPlugins("sender", []ReqItem{{Plugin: "Pro-Q 4", Format: "VST3"}})
	cfgA.Hub().Approve(rs[0].ID, "plugin", "Pro-Q 4.vst3", "", "VST3")
	data := make([]byte, 3<<20)
	rand.Read(data)
	src := filepath.Join(root, "p.zip")
	os.WriteFile(src, data, 0o644)
	ja, jb := &Job{}, &Job{}
	var wg sync.WaitGroup
	var sendErr error
	wg.Add(1)
	go func() { defer wg.Done(); sendErr = cfgA.Hub().StreamSend(ja, rs[0].ID, src) }()
	r, _ := cfgB.Hub().Request(rs[0].ID)
	dst := filepath.Join(root, "got.zip")
	if err := cfgB.Hub().StreamRecv(jb, r, dst); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, data) {
		t.Fatal("данные не совпали")
	}
	if !strings.Contains(steps(jb), "по локальной сети") || !strings.Contains(steps(ja), "по локальной сети") {
		t.Fatalf("должно было пойти напрямую:\n%s\n---\n%s", steps(ja), steps(jb))
	}
}

// Получатель не достучался напрямую (другая сеть) — то же самое приходит через сервер, без ожидания.
func TestPluginDirectFallsBackToRelay(t *testing.T) {
	testLAN = []string{"127.0.0.1"}
	t.Cleanup(func() { testLAN = nil })
	cfgA, cfgB, root := directHub(t)
	rs, _ := cfgB.Hub().RequestPlugins("sender", []ReqItem{{Plugin: "Pro-Q 4", Format: "VST3"}})
	cfgA.Hub().Approve(rs[0].ID, "plugin", "Pro-Q 4.vst3", "", "VST3")
	src := filepath.Join(root, "p.zip")
	os.WriteFile(src, []byte("plugin-zip"), 0o644)
	ja, jb := &Job{}, &Job{}
	var wg sync.WaitGroup
	var sendErr error
	wg.Add(1)
	go func() { defer wg.Done(); sendErr = cfgA.Hub().StreamSend(ja, rs[0].ID, src) }()
	time.Sleep(300 * time.Millisecond)
	directOff.Store(true) // как будто адрес отправителя недоступен
	t.Cleanup(func() { directOff.Store(false) })
	r, _ := cfgB.Hub().Request(rs[0].ID)
	dst := filepath.Join(root, "got.zip")
	t0 := time.Now()
	if err := cfgB.Hub().StreamRecv(jb, r, dst); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	if b, _ := os.ReadFile(dst); string(b) != "plugin-zip" {
		t.Fatal("данные не совпали")
	}
	if time.Since(t0) > 4*time.Second {
		t.Fatalf("через сервер должно было пойти сразу, а заняло %v", time.Since(t0))
	}
}

func TestSpeedTest(t *testing.T) {
	cfgA, _, _ := directHub(t)
	txt, err := SpeedTest(cfgA, &Job{})
	if err != nil || !strings.Contains(txt, "четыре соединения") || !strings.Contains(txt, "Отправка:") {
		t.Fatalf("%v\n%s", err, txt)
	}
}

// Докачка: у получателя уже есть начало того же архива — через сервер и напрямую приходит только остаток.
func TestPluginResume(t *testing.T) {
	for _, lan := range []bool{false, true} {
		t.Run(map[bool]string{false: "через сервер", true: "по локальной сети"}[lan], func(t *testing.T) {
			if lan {
				testLAN = []string{"127.0.0.1"}
				t.Cleanup(func() { testLAN = nil })
			} else {
				directOff.Store(true)
				t.Cleanup(func() { directOff.Store(false) })
			}
			cfgA, cfgB, root := directHub(t)
			rs, _ := cfgB.Hub().RequestPlugins("sender", []ReqItem{{Plugin: "Pro-Q 4", Format: "VST3"}})
			cfgA.Hub().Approve(rs[0].ID, "plugin", "Pro-Q 4.vst3", "", "VST3")
			data := bytes.Repeat([]byte("0123456789abcdef"), 64<<10) // 1 МБ
			src := filepath.Join(root, "send.zip")
			os.WriteFile(src, data, 0o644)
			markPacked(src)
			dst := filepath.Join(root, "got.zip")
			// «прошлая попытка» оборвалась на 300 КБ; начало помечено — если сервер/отправитель пришлют с нуля, оно затрётся
			head := bytes.Repeat([]byte{'X'}, 300<<10)
			if lan { // по локальной сети файл сверяется по sha256 — начало должно быть настоящим
				head = data[:300<<10]
			}
			os.WriteFile(dst+".part", head, 0o644)
			savePartID(dst, packedID(src))
			ja, jb := &Job{}, &Job{}
			var wg sync.WaitGroup
			var sendErr error
			wg.Add(1)
			go func() { defer wg.Done(); sendErr = cfgA.Hub().StreamSend(ja, rs[0].ID, src) }()
			time.Sleep(300 * time.Millisecond)
			r, _ := cfgB.Hub().Request(rs[0].ID)
			err := cfgB.Hub().StreamRecv(jb, r, dst)
			wg.Wait()
			if err != nil || sendErr != nil {
				t.Fatal(err, sendErr)
			}
			b, _ := os.ReadFile(dst)
			if len(b) != len(data) || !bytes.Equal(b[:300<<10], head) || !bytes.Equal(b[300<<10:], data[300<<10:]) {
				t.Fatalf("ждали докачку остатка: %d байт", len(b))
			}
			if !strings.Contains(steps(ja), "Докачиваю") {
				t.Fatalf("отправитель не докачивал:\n%s", steps(ja))
			}
		})
	}
}
