//go:build windows

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenderNotifications(t *testing.T) {
	dir := t.TempDir()
	defer pruneRenderWatches(nil)
	if !renderChanged(dir) {
		t.Fatal("initial scan skipped")
	}
	if renderChanged(dir) {
		t.Fatal("unchanged directory requested a rescan")
	}
	if err := os.WriteFile(filepath.Join(dir, "render.wav"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if renderChanged(dir) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("new render did not trigger Windows notification")
}
