package app

import (
	"archive/zip"
	"compress/flate"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Зависимости плагина не передаются, если они уже есть у получателя.
// Перед передачей отправитель публикует на сервере список файлов зависимостей (offer),
// получатель отвечает, какие у него уже есть (have), — их отправитель не кладёт в пакет.
// Если одновременно идут несколько передач с общими файлами (напр. общая папка Arturia у двух плагинов),
// каждый файл едет только с одной из них: получатель «закрепляет» файл за первой передачей,
// а остальным отвечает «уже есть». Если та передача сорвалась — недостающее дозапрашивается само.

type depOffer struct {
	Plugin string      `json:"plugin"` // имя пакета или файла плагина
	Bundle bool        `json:"bundle"` // плагин — папка-пакет (.vst3 и т. п.)
	Format string      `json:"format"`
	Sub    string      `json:"sub,omitempty"`
	Items  []offerItem `json:"items"`
}

type offerItem struct {
	Root  string   `json:"root"`
	Rel   string   `json:"rel"`
	Files []string `json:"files"` // пути внутри, через "/"; "" — элемент сам является файлом
}

// depHave: номер элемента → номера файлов, которые у получателя уже есть.
type depHave map[int][]int

const offerMaxFiles = 200000

// dataFiles — файлы элемента данных в постоянном порядке, включая лицензии, без ссылок.
func dataFiles(d *PluginData) (base string, files []string) {
	base = d.abs
	if base == "" {
		r := dataRoot(d.Root)
		if r == nil {
			return "", nil
		}
		base = filepath.Join(r.Path, filepath.FromSlash(d.Rel))
	}
	if d.files != nil {
		return base, d.files
	}
	st, err := os.Lstat(base)
	if err != nil {
		return base, nil
	}
	if !st.IsDir() {
		if !st.Mode().IsRegular() {
			return base, nil
		}
		d.files = []string{""}
		return base, d.files
	}
	filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(files)
	d.files = files
	return base, files
}

func buildOffer(format, pluginPath, sub string, data []PluginData) depOffer {
	st, _ := os.Stat(pluginPath)
	off := depOffer{Plugin: filepath.Base(strings.TrimRight(pluginPath, `/\`)), Bundle: st != nil && st.IsDir(), Format: format, Sub: sub}
	total := 0
	for i := range data {
		_, files := dataFiles(&data[i])
		if total+len(files) > offerMaxFiles {
			files = nil // слишком много файлов — этот элемент просто отправим
		}
		total += len(files)
		off.Items = append(off.Items, offerItem{Root: data[i].Root, Rel: data[i].Rel, Files: files})
	}
	return off
}

// applyHave помечает файлы, которые у получателя уже есть. Возвращает их число и объём.
func applyHave(data []PluginData, have depHave) (n int, bytes int64) {
	for idx, list := range have {
		if idx < 0 || idx >= len(data) {
			continue
		}
		d := &data[idx]
		base, files := dataFiles(d)
		if d.skip == nil {
			d.skip = map[int]bool{}
		}
		for _, j := range list {
			if j < 0 || j >= len(files) || d.skip[j] {
				continue
			}
			d.skip[j] = true
			n++
			if st, err := os.Stat(filepath.Join(base, filepath.FromSlash(files[j]))); err == nil {
				bytes += st.Size()
			}
		}
	}
	return
}

// negotiateDeps спрашивает у получателя, какие зависимости у него уже есть, и помечает их, чтобы не передавать.
func negotiateDeps(h *Hub, job *Job, r *PluginRequest, pluginPath, sub string, data []PluginData) {
	if len(data) == 0 {
		return
	}
	seq, err := h.PutOffer(r.ID, buildOffer(r.Format, pluginPath, sub, data))
	if err != nil {
		if !errors.Is(err, errNoNego) {
			DevLog("info", 0, true, "сверка зависимостей %s: %v", r.Plugin, err)
		}
		return
	}
	job.Logf("Сверяю с %s, какие зависимости у него уже есть…", r.FromNick)
	var have depHave
	got := false
	for i := 0; i < 3 && !got; i++ { // до минуты; старое приложение у получателя не ответит — тогда отправлю всё
		if job.Ctx().Err() != nil {
			return
		}
		s, ok, err := h.GetHave(r.ID, &have)
		if err != nil {
			return
		}
		got = ok && s == seq
	}
	if !got {
		job.Logf("%s не ответил — отправлю зависимости целиком", r.FromNick)
		return
	}
	if n, bytes := applyHave(data, have); n > 0 {
		job.Logf("✓ У %s уже есть %d файлов зависимостей (%s) — их не передаю", r.FromNick, n, fmtBytes(bytes))
	} else {
		job.Logf("У %s этих зависимостей нет — отправлю их", r.FromNick)
	}
}

// ---------- сторона получателя ----------

var claims = struct {
	sync.Mutex
	owner    map[string]string            // путь файла (в нижнем регистре) → id передачи, с которой он придёт
	deferred map[string]map[string]string // id передачи → путь → id передачи-владельца (файл обещан другой)
	answered map[string]int               // id передачи → номер списка, на который уже ответили
}{owner: map[string]string{}, deferred: map[string]map[string]string{}, answered: map[string]int{}}

// receiverBinDir — куда у получателя лягут библиотеки «рядом с плагином».
func receiverBinDir(cfg *Config, off *depOffer) string {
	for _, ip := range ScanInstalled(cfg, false) {
		if ip.Path != "" && strings.EqualFold(filepath.Base(strings.TrimRight(ip.Path, `/\`)), off.Plugin) {
			if b := pluginBinary(ip.Path); b != "" {
				return filepath.Dir(b)
			}
		}
	}
	if off.Bundle {
		return "" // пакета ещё нет — значит, и библиотек в нём нет
	}
	dir, _ := pluginTargetDir(cfg, off.Format)
	if dir == "" {
		return ""
	}
	if sub := cleanSubdir(off.Sub); sub != "" {
		dir = filepath.Join(dir, sub)
	}
	return dir
}

// answerOffer считает, какие файлы из предложения уже есть (или придут с другой передачей), и отвечает серверу.
func answerOffer(cfg *Config, r *PluginRequest) error {
	h := cfg.Hub()
	seq, raw, err := h.GetOffer(r.ID)
	if err != nil {
		return err
	}
	var off depOffer
	if err := json.Unmarshal(raw, &off); err != nil {
		return err
	}
	binDir := ""
	binDirDone := false
	have := depHave{}
	if r.Reinstall { // переустановка начисто: пусть придёт всё
		off.Items = nil
	}
	lite := cfg.IsLite(r.Plugin) // «без пресетов»: библиотеку пресетов не тянем
	skippedPresets := 0
	claims.Lock()
	// прошлый ответ этой передачи (её повторили) — освобождаем закреплённое за ней
	for p, o := range claims.owner {
		if o == r.ID {
			delete(claims.owner, p)
		}
	}
	delete(claims.deferred, r.ID)
	for i, it := range off.Items {
		var root string
		if it.Root == sideRoot {
			if !binDirDone {
				claims.Unlock()
				binDir, binDirDone = receiverBinDir(cfg, &off), true
				claims.Lock()
			}
			root = binDir
		} else if dr := dataRoot(it.Root); dr != nil {
			root = filepath.Join(dr.Path, filepath.FromSlash(it.Rel))
		}
		for j, f := range it.Files {
			if root == "" && it.Root != sideRoot {
				break
			}
			var p string
			switch {
			case it.Root == sideRoot:
				name := filepath.Base(it.Rel)
				// библиотека уже есть там, где Windows её найдёт (System32, PATH)
				if runtime.GOOS == "windows" && findLib(name, winLibSearchDirs(binDir)[1:]) != "" {
					have[i] = append(have[i], j)
					continue
				}
				if root == "" {
					continue
				}
				p = filepath.Join(root, name)
			case f == "":
				p = root
			default:
				p = filepath.Join(root, filepath.FromSlash(f))
			}
			key := strings.ToLower(filepath.Clean(p))
			if lite && it.Root != sideRoot && isPresetPath(it.Rel+"/"+f) {
				have[i] = append(have[i], j)
				skippedPresets++
				continue
			}
			if exists(p) {
				have[i] = append(have[i], j)
				continue
			}
			if o, ok := claims.owner[key]; ok && o != r.ID {
				have[i] = append(have[i], j) // придёт с другой передачей
				if claims.deferred[r.ID] == nil {
					claims.deferred[r.ID] = map[string]string{}
				}
				claims.deferred[r.ID][key] = o
				continue
			}
			claims.owner[key] = r.ID
		}
	}
	claims.Unlock()
	if err := h.PutHave(r.ID, seq, have); err != nil {
		return err
	}
	claims.Lock()
	claims.answered[r.ID] = seq
	claims.Unlock()
	n := 0
	for _, l := range have {
		n += len(l)
	}
	DevLog("info", 0, false, "зависимости %s: уже есть или придут с другой передачей — %d файлов (пресеты не берём: %d)", r.Plugin, n, skippedPresets)
	return nil
}

var answering sync.Map // id → true, пока считаем ответ

// answerOffers — получатель отвечает на все новые списки зависимостей.
func (s *Server) answerOffers(ready []PluginRequest) {
	for _, r := range ready {
		if r.OfferSeq == 0 {
			continue
		}
		claims.Lock()
		done := claims.answered[r.ID] == r.OfferSeq
		claims.Unlock()
		if done {
			continue
		}
		if _, busy := answering.LoadOrStore(r.ID, true); busy {
			continue
		}
		rr := r
		go func() {
			defer answering.Delete(rr.ID)
			if err := answerOffer(s.cfg, &rr); err != nil {
				DevLog("info", 0, true, "ответ про зависимости %s: %v", rr.Plugin, err)
			}
		}()
	}
}

// reconcileClaims: закончившиеся передачи освобождают свои файлы. Если передача, с которой должны были прийти
// общие файлы, сорвалась, а другая на неё рассчитывала, — та, другая, запрашивается ещё раз (придёт только недостающее).
func (s *Server) reconcileClaims(ready []PluginRequest) {
	active := map[string]bool{}
	for _, r := range ready {
		active[r.ID] = true
	}
	s.busyReq.Range(func(k, v any) bool {
		if v == "receiving" {
			active[k.(string)] = true
		}
		return true
	})
	var retry []string
	claims.Lock()
	for y, files := range claims.deferred {
		if active[y] {
			continue
		}
		waiting, missing := false, 0
		for p, o := range files {
			if active[o] {
				waiting = true
			} else if !exists(p) {
				missing++
			}
		}
		if waiting {
			continue
		}
		delete(claims.deferred, y)
		if missing > 0 {
			retry = append(retry, y)
		}
	}
	for p, o := range claims.owner {
		if !active[o] {
			delete(claims.owner, p)
		}
	}
	for id := range claims.answered {
		if !active[id] {
			delete(claims.answered, id)
		}
	}
	claims.Unlock()
	for _, id := range retry {
		go func(id string) {
			h := s.cfg.Hub()
			r, err := h.Request(id)
			if err != nil || r.Status != ReqInstalled {
				return
			}
			if h.RetryRequest(id) == nil {
				LogActivity("", "info", fmt.Sprintf("Общие файлы для %s должны были прийти с другой передачей, но она не прошла — запросил %s ещё раз (придёт только недостающее)", r.Plugin, r.Plugin))
			}
		}(id)
	}
}

// ---------- упаковка быстрее ----------

// Уже сжатые файлы не сжимаем повторно (только тратит время), остальное — быстрым уровнем сжатия.
var storedExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".zip": true, ".7z": true,
	".rar": true, ".gz": true, ".xz": true, ".bz2": true, ".mp3": true, ".ogg": true, ".flac": true, ".m4a": true, ".aac": true,
	".opus": true, ".mp4": true, ".mov": true, ".dmg": true, ".pkg": true, ".msi": true, ".cab": true, ".nicnt": true, ".ncw": true,
	".nkx": true, ".nkc": true, ".jar": true, ".woff": true, ".woff2": true, ".tar": false}

func zipMethod(name string, size int64) uint16 {
	if storedExts[strings.ToLower(filepath.Ext(name))] || size < 64 {
		return zip.Store
	}
	return zip.Deflate
}

// zstdWriters — архивы, которые пакуются в zstd (получатель новой версии его понимает).
var zstdWriters sync.Map

// zipMethodFor — как сжать файл в этом архиве: zstd (быстрее и плотнее), deflate или без сжатия.
func zipMethodFor(zw *zip.Writer, name string, size int64) uint16 {
	m := zipMethod(name, size)
	if _, ok := zstdWriters.Load(zw); ok && m == zip.Deflate {
		return zstd.ZipMethodWinZip
	}
	return m
}

// fastZip — быстрое сжатие; withZstd — zstd вместо deflate (в 3–5 раз быстрее при том же размере или меньше).
func fastZip(zw *zip.Writer, withZstd bool) {
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestSpeed)
	})
	if withZstd {
		zw.RegisterCompressor(zstd.ZipMethodWinZip, zstd.ZipCompressor(zstd.WithEncoderLevel(zstd.SpeedDefault)))
		zstdWriters.Store(zw, true)
	}
}

func doneZip(zw *zip.Writer) { zstdWriters.Delete(zw) }

func init() { // полученные архивы в zstd читаются так же, как обычные
	zip.RegisterDecompressor(zstd.ZipMethodWinZip, zstd.ZipDecompressor())
}

// presetExts — файлы пресетов и банков (их не тянем у плагинов «без пресетов»).
var presetExts = map[string]bool{".fxp": true, ".fxb": true, ".vstpreset": true, ".aupreset": true, ".nmsv": true, ".h2p": true,
	".fst": true, ".preset": true, ".pgtx": true, ".nksf": true, ".nki": true, ".nkm": true, ".bank": true, ".xpf": true,
	".ksd": true, ".spf": true, ".sbf": true, ".patch": true, ".syx": true, ".adv": true, ".adg": true, ".tal": true, ".pigments": true, ".labx": true}

// isPresetPath — путь внутри данных плагина похож на библиотеку пресетов: расширение пресета
// или папка Presets / Patches / Banks / Soundbanks / Factory Library.
func isPresetPath(rel string) bool {
	rel = strings.ToLower(filepath.ToSlash(rel))
	if presetExts[filepath.Ext(rel)] {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "presets", "preset", "patches", "banks", "soundbanks", "soundbank", "user presets", "factory presets", "factory library", "sound banks", "librarys", "library presets":
			return true
		}
	}
	return false
}
