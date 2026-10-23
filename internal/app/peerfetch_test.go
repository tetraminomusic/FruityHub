package app

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Аудио проекта приходит с компьютера участника (по локальной сети или через сервер), а не с GitHub.
func TestPeerFetchLFS(t *testing.T) {
	for _, lan := range []bool{true, false} {
		name := map[bool]string{true: "по локальной сети", false: "через сервер"}[lan]
		t.Run(name, func(t *testing.T) {
			peerFetchMin = 0
			t.Cleanup(func() { peerFetchMin = 8 << 20 })
			if lan {
				testLAN = []string{"127.0.0.1"}
				t.Cleanup(func() { testLAN = nil })
			}
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
			gh := useFakeGitHub(t, root)
			hubURL := startHub(t, root, gh)
			cfgA, cfgB := &Config{Server: hubURL}, &Config{Server: hubURL}
			must(t, func() error { _, err := cfgA.Hub().Register("tetramino", "uzbek", "tokA"); return err }())
			must(t, func() error { _, err := cfgB.Hub().Register("friend", "uzbek", "tokB"); return err }())
			aDir := filepath.Join(root, "A", "Beat")
			os.MkdirAll(filepath.Join(aDir, "Audio"), 0o755)
			os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP(filepath.Join(aDir, "Audio", "vox.wav")), 0o644)
			big := make([]byte, 2<<20)
			rand.Read(big)
			os.WriteFile(filepath.Join(aDir, "Audio", "vox.wav"), big, 0o644)
			jA := &Job{}
			A := &Project{Dir: aDir, Cfg: cfgA, Job: jA}
			if _, err := A.CreateOnGitHub("Beat", []string{"*"}); err != nil {
				t.Fatal(err, dump(jA))
			}
			cfgA.Projects = []LocalProject{{Path: aDir}}
			sA := &Server{cfg: cfgA, jobs: map[string]*Job{}}
			stop := make(chan struct{})
			defer close(stop)
			cfgA.Hub().Inbox() // приложение A на связи и умеет отдавать аудио
			go func() {
				for {
					select {
					case <-stop:
						return
					case <-time.After(150 * time.Millisecond):
					}
					if in, err := cfgA.Hub().Inbox(); err == nil {
						sA.serveFetches(in.Fetches)
					}
				}
			}()
			in, err := cfgB.Hub().Inbox()
			must(t, err)
			acc, err := cfgB.Hub().AcceptInvite(in.Invites[0].ID)
			must(t, err)
			jB := &Job{}
			bDir, err := CloneServerProject(cfgB, jB, acc, filepath.Join(root, "B"))
			if err != nil {
				t.Fatal(err, dump(jB))
			}
			if b, _ := os.ReadFile(filepath.Join(bDir, "Audio", "vox.wav")); !bytes.Equal(b, big) {
				t.Fatalf("аудио не совпало (%d байт)\n%s", len(b), dump(jB))
			}
			want := "Аудио от tetramino " + map[bool]string{true: "по локальной сети", false: "через сервер FruityHub"}[lan]
			if !strings.Contains(steps(jB), want) {
				t.Fatalf("аудио должно было прийти от участника (%s):\n%s", want, steps(jB))
			}
			// «Получить»: новое аудио тоже приходит от участника
			take2 := make([]byte, 1<<20)
			rand.Read(take2)
			os.WriteFile(filepath.Join(aDir, "Audio", "take2.wav"), take2, 0o644)
			os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP(filepath.Join(aDir, "Audio", "take2.wav")), 0o644)
			if _, err := A.Send("дубль 2", false); err != nil {
				t.Fatal(err, dump(jA))
			}
			jB2 := &Job{}
			B := &Project{Dir: bDir, Cfg: cfgB, Job: jB2}
			if _, err := B.Receive(); err != nil {
				t.Fatal(err, dump(jB2))
			}
			if b, _ := os.ReadFile(filepath.Join(bDir, "Audio", "take2.wav")); !bytes.Equal(b, take2) {
				t.Fatalf("новое аудио не пришло\n%s", dump(jB2))
			}
			if !strings.Contains(steps(jB2), want) {
				t.Fatalf("при получении аудио должно было прийти от участника:\n%s", steps(jB2))
			}
		})
	}
}
