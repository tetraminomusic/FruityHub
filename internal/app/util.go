package app

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func trimOut(b []byte) string { return strings.TrimRight(strings.TrimSpace(string(b)), `/\`) }

func fileSHA1(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".flsync-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(src); err == nil {
		os.Chtimes(tmp, st.ModTime(), st.ModTime())
	}
	return os.Rename(tmp, dst)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func dirExists(p string) bool {
	st, err := os.Stat(strings.TrimSpace(p))
	return err == nil && st.IsDir()
}

// trashInto moves p into a trash folder, keeping the name unique.
func trashInto(trash, p string) error {
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return err
	}
	name := filepath.Base(p)
	dst := filepath.Join(trash, name)
	for i := 2; exists(dst); i++ {
		ext := filepath.Ext(name)
		dst = filepath.Join(trash, fmt.Sprintf("%s %d%s", strings.TrimSuffix(name, ext), i, ext))
	}
	if err := os.Rename(p, dst); err != nil {
		// different disk: copy, then remove
		if cerr := copyFile(p, dst); cerr != nil {
			return err
		}
		return os.Remove(p)
	}
	return nil
}
