package app

import (
	"bufio"
	"bytes"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"flsync/internal/flp"
)

// FL Studio keeps the list of found plugins in "Plugin database": for each
// plugin an .nfo (text: name, path, type…) and an .fst (a tiny preset that
// loads it) under Installed/<Generators|Effects>/<format>/, plus optional
// favourites in the category folders. When a plugin arrives from a friend we
// copy his entries for it, pointing them to where the plugin was installed
// here — so FL lists the plugin without a rescan.

const fldbPrefix = "__uzbek_fldb__/"

func flDBRoot() string { return filepath.Join(flUserData(), "Presets", "Plugin database") }

func samePluginPath(a, b string) bool {
	a, b = strings.TrimRight(a, `/\`), strings.TrimRight(b, `/\`)
	if runtime.GOOS == "linux" {
		return a == b
	}
	return strings.EqualFold(a, b)
}

// nfoPaths returns the plugin paths an .nfo describes (ps_file_filename_N).
func nfoPaths(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "\ufeff"), "=")
		if ok && strings.HasPrefix(strings.ToLower(k), "ps_file_filename_") {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// collectFLDBEntries finds this computer's FL database entries for a plugin.
// Keys are paths relative to the database root (with "/").
func collectFLDBEntries(pluginPath string) map[string][]byte {
	root := flDBRoot()
	out := map[string][]byte{}
	filepath.WalkDir(filepath.Join(root, "Installed"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".nfo") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, pp := range nfoPaths(b) {
			if samePluginPath(pp, pluginPath) {
				rel, _ := filepath.Rel(root, p)
				out[filepath.ToSlash(rel)] = b
				base := strings.TrimSuffix(p, filepath.Ext(p))
				for _, ext := range []string{".fst", ".FST"} {
					if fb, err := os.ReadFile(base + ext); err == nil {
						rel, _ := filepath.Rel(root, base+ext)
						out[filepath.ToSlash(rel)] = fb
						break
					}
				}
				break
			}
		}
		return nil
	})
	// favourites in the category folders that load this plugin
	needle := []byte(strings.TrimRight(pluginPath, `/\`))
	for _, top := range []string{"Generators", "Effects"} {
		filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".fst") {
				return nil
			}
			if info, _ := d.Info(); info == nil || info.Size() > 256<<10 {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, needle) {
				rel, _ := filepath.Rel(root, p)
				out[filepath.ToSlash(rel)] = b
			}
			return nil
		})
	}
	return out
}

// delphiTime is how FL stores file dates in .nfo (TDateTime bits as int64).
func delphiTime(t time.Time) int64 {
	_, off := t.Zone()
	days := float64(t.Unix()+int64(off))/86400 + 25569
	return int64(math.Float64bits(days))
}

func rewriteNFO(b []byte, oldPath, newPath string, mtime time.Time) []byte {
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		cr := strings.HasSuffix(l, "\r")
		k, v, ok := strings.Cut(strings.TrimSuffix(l, "\r"), "=")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimPrefix(k, "\ufeff"))
		switch {
		case strings.HasPrefix(key, "ps_file_filename_") && samePluginPath(strings.TrimSpace(v), oldPath):
			l = k + "=" + newPath
		case strings.HasPrefix(key, "ps_file_date_") && !mtime.IsZero():
			l = k + "=" + strconv.FormatInt(delphiTime(mtime), 10)
		default:
			continue
		}
		if cr {
			l += "\r"
		}
		lines[i] = l
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteFST(b []byte, oldPath, newPath string) []byte {
	f, err := flp.Parse(b)
	if err != nil {
		return b
	}
	if f.RewritePluginPaths(func(old string) string {
		if samePluginPath(old, oldPath) {
			return newPath
		}
		return old
	}) == 0 {
		return b
	}
	return f.Bytes()
}

// registerInFLDB writes a friend's entries for a plugin into this computer's
// FL database, pointing at newPath. Returns how many files were written.
func registerInFLDB(entries map[string][]byte, oldPath, newPath string) int {
	root := flDBRoot()
	if len(entries) == 0 || !dirExists(root) {
		return 0
	}
	var mtime time.Time
	if st, err := os.Stat(newPath); err == nil {
		mtime = st.ModTime()
	}
	n := 0
	for rel, b := range entries {
		rel = filepath.FromSlash(rel)
		if strings.Contains(rel, "..") {
			continue
		}
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".nfo":
			b = rewriteNFO(b, oldPath, newPath, mtime)
		case ".fst":
			b = rewriteFST(b, oldPath, newPath)
		default:
			continue
		}
		dst := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if os.WriteFile(dst, b, 0o644) == nil {
			n++
		}
	}
	return n
}

// fldbEntriesFromZip splits the database entries off a received plugin zip.
func splitFLDB(name string) (rel string, ok bool) {
	if strings.HasPrefix(name, fldbPrefix) {
		return strings.TrimPrefix(name, fldbPrefix), true
	}
	return "", false
}
