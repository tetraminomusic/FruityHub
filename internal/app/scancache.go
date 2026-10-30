package app

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"flsync/internal/flp"
)

// Кэши на диске, чтобы не сканировать заново при каждом открытии:
//   - что внутри каждого .flp (плагины, сэмплы, версия FL) — пересчитывается, только если файл изменился;
//   - установленные плагины — при запуске сразу берутся из прошлого сканирования, а свежее идёт в фоне.

// ---------- .flp ----------

type flpCacheEntry struct {
	MetadataOnly bool     `json:"metadataOnly,omitempty"`
	Size         int64    `json:"size"`
	Mod          int64    `json:"mod"` // unix ns
	Info         flp.Info `json:"info"`
	Error        string   `json:"error,omitempty"`
	Used         int64    `json:"used"` // когда последний раз нужен (для чистки)
}

var (
	flpCacheMu    sync.Mutex
	flpCache      map[string]*flpCacheEntry
	flpCacheDirty bool
	flpSaveTimer  *time.Timer
)

// v5 marks metadata-only entries; older binaries must not treat these as full
// dependency scans when a user runs an earlier application version.
func flpCachePath() string { return filepath.Join(ConfigDir(), "cache", "flp-index-v5.json") }

func loadFLPCache() {
	if flpCache != nil {
		return
	}
	flpCache = map[string]*flpCacheEntry{}
	readJSON(flpCachePath(), &flpCache)
	if flpCache == nil {
		flpCache = map[string]*flpCacheEntry{}
	}
}

// parseFLPCached — содержимое .flp из кэша, если файл не менялся (размер и время те же), иначе разбор.
func parseFLPCached(abs string) (flp.Info, error) {
	return parseFLPCachedMode(abs, false)
}

func parseFLPMetadataCached(abs string) (flp.Info, error) {
	return parseFLPCachedMode(abs, true)
}

func parseFLPCachedMode(abs string, metadataOnly bool) (flp.Info, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return flp.Info{}, err
	}
	flpCacheMu.Lock()
	loadFLPCache()
	if e := flpCache[abs]; e != nil && e.Size == st.Size() && e.Mod == st.ModTime().UnixNano() && (metadataOnly || !e.MetadataOnly || e.Error != "") {
		e.Used = time.Now().Unix()
		flpCacheMu.Unlock()
		if e.Error != "" {
			return e.Info, errString(e.Error)
		}
		return e.Info, nil
	}
	flpCacheMu.Unlock()
	f, perr := flp.ParseFile(abs)
	e := &flpCacheEntry{Size: st.Size(), Mod: st.ModTime().UnixNano(), Used: time.Now().Unix(), MetadataOnly: metadataOnly}
	if perr != nil {
		e.Error = perr.Error()
	} else if metadataOnly {
		e.Info = f.Metadata()
	} else {
		e.Info = f.Info()
	}
	flpCacheMu.Lock()
	// A concurrent full scan must not be replaced by a partial result.
	if old := flpCache[abs]; metadataOnly && old != nil && !old.MetadataOnly && old.Size == e.Size && old.Mod == e.Mod {
		flpCacheMu.Unlock()
		if old.Error != "" {
			return old.Info, errString(old.Error)
		}
		return old.Info, nil
	}
	flpCache[abs] = e
	scheduleFLPCacheSave()
	flpCacheMu.Unlock()
	return e.Info, perr
}

type errString string

func (e errString) Error() string { return string(e) }

// scheduleFLPCacheSave — сохраняем не чаще раза в 2 секунды (вызывать под flpCacheMu).
func scheduleFLPCacheSave() {
	flpCacheDirty = true
	if flpSaveTimer != nil {
		return
	}
	flpSaveTimer = time.AfterFunc(2*time.Second, func() {
		flpCacheMu.Lock()
		flpSaveTimer = nil
		if !flpCacheDirty {
			flpCacheMu.Unlock()
			return
		}
		flpCacheDirty = false
		old := time.Now().Add(-60 * 24 * time.Hour).Unix() // давно не нужные записи (удалённые проекты) выбрасываем
		cp := map[string]*flpCacheEntry{}
		for k, v := range flpCache {
			if v.Used > old {
				cp[k] = v
			}
		}
		flpCache = cp
		flpCacheMu.Unlock()
		os.MkdirAll(filepath.Dir(flpCachePath()), 0o755)
		writeJSON(flpCachePath(), cp)
	})
}

// ---------- установленные плагины ----------

type scanDisk struct {
	At      int64           `json:"at"`
	Plugins []scanDiskEntry `json:"plugins"`
	Folders []PluginFolder  `json:"folders"`
}

type scanDiskEntry struct {
	InstalledPlugin
	P string `json:"p,omitempty"` // путь (в InstalledPlugin он не сохраняется в JSON)
	S string `json:"s,omitempty"`
}

func scanCachePath() string { return filepath.Join(ConfigDir(), "cache", "plugins-scan.json") }

func saveScanCache(res []InstalledPlugin, folders []PluginFolder) {
	d := scanDisk{At: time.Now().Unix(), Folders: folders}
	for _, p := range res {
		d.Plugins = append(d.Plugins, scanDiskEntry{InstalledPlugin: p, P: p.Path, S: p.Source})
	}
	os.MkdirAll(filepath.Dir(scanCachePath()), 0o755)
	writeJSON(scanCachePath(), d)
}

// loadScanCache — прошлое сканирование (не старше недели); пути, которых уже нет, отбрасываются.
func loadScanCache() ([]InstalledPlugin, []PluginFolder, time.Time, bool) {
	var d scanDisk
	if readJSON(scanCachePath(), &d) != nil || d.At == 0 || time.Since(time.Unix(d.At, 0)) > 7*24*time.Hour {
		return nil, nil, time.Time{}, false
	}
	res := make([]InstalledPlugin, 0, len(d.Plugins))
	for _, e := range d.Plugins {
		p := e.InstalledPlugin
		p.Path, p.Source = e.P, e.S
		if p.Path != "" && !exists(p.Path) {
			continue
		}
		res = append(res, p)
	}
	return res, d.Folders, time.Unix(d.At, 0), true
}
