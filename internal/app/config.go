package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// LocalProject is a project folder registered in this app instance.
type LocalProject struct {
	Path            string   `json:"path"`
	RelinkOnReceive bool     `json:"relinkOnReceive"`
	SeenComments    int64    `json:"seenComments"` // newest comment time the user has seen
	Group           string   `json:"group,omitempty"`
	KeepFLPs        []string `json:"keepFlps,omitempty"` // extra .flp the user said are just versions
	Name            string   `json:"name,omitempty"`     // своё название проекта — только на этом компьютере
	MainFLP         string   `json:"mainFlp,omitempty"`  // имя главного .flp и репозиторий — по ним проект найдётся, если папку перенесут
	Remote          string   `json:"remote,omitempty"`
}

// Group is a folder in the project list (local only).
type Group struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Collapsed bool   `json:"collapsed"`
}

// Config is the per-machine app configuration (never committed).
type Config struct {
	UserName         string         `json:"userName"` // nick on the server (= in the app)
	UserID           string         `json:"userId,omitempty"`
	GitHubLogin      string         `json:"githubLogin,omitempty"`
	Server           string         `json:"server,omitempty"`      // server address (empty = built-in)
	DeviceToken      string         `json:"deviceToken,omitempty"` // this device's key on the server
	Sharing          string         `json:"sharing,omitempty"`     // who may download plugins from me: off|ask|members|all
	Token            string         `json:"token,omitempty"`       // GitHub token: projects are the user's own repos
	Proxy            string         `json:"proxy,omitempty"`       // прокси для GitHub, например http://127.0.0.1:10809 (пусто = авто)
	ExtraPluginDirs  []string       `json:"extraPluginDirs"`
	Projects         []LocalProject `json:"projects"`
	Groups           []Group        `json:"groups,omitempty"`
	LastParent       string         `json:"lastParent,omitempty"`
	BgDim            int            `json:"bgDim,omitempty"`    // затемнение фона, % (0 = по умолчанию)
	DevMode          bool           `json:"devMode,omitempty"`  // режим разработчика: журнал команд, админка сервера
	AdminKey         string         `json:"adminKey,omitempty"` // ключ администратора сервера
	SkipUpdate       string         `json:"skipUpdate,omitempty"`
	AutoUpdate       bool           `json:"autoUpdate,omitempty"`
	AutoFetchPlugins bool           `json:"autoFetchPlugins,omitempty"` // после скачивания проекта запросить недостающие плагины
	SeenVersion      string         `json:"seenVersion,omitempty"`      // версия, для которой уже показали «Что нового»
	GuideSeen        bool           `json:"guideSeen,omitempty"`        // показали знакомство с программой
	HideHelp         bool           `json:"hideHelp,omitempty"`         // убрать кнопку «?» из шапки
	LFSExtensions    []string       `json:"lfsExtensions,omitempty"`    // какие файлы уходят в Git LFS у новых проектов (пусто — стандартный список)
	NoPeerFetch      bool           `json:"noPeerFetch,omitempty"`      // не брать/не отдавать аудио проектов напрямую между участниками
	NoLAN            bool           `json:"noLan,omitempty"`            // не передавать напрямую по локальной сети (только через сервер)
	ToastSec         *int           `json:"toastSec,omitempty"`         // сколько секунд держать уведомления (0 — пока не закроют)
	NoErrorReports   bool           `json:"noErrorReports,omitempty"`   // не отправлять свои ошибки в журнал сервера
	SortMode         string         `json:"sortMode,omitempty"`         // порядок списка проектов: manual, name, name-desc, modified, modified-asc
	Lang             string         `json:"lang,omitempty"`
	OldNicks         []string       `json:"oldNicks,omitempty"`     // мои прежние ники (мои старые комментарии — мои)
	ProjectRoots     []string       `json:"projectRoots,omitempty"` // папки, где лежат проекты FL (Настройки → Проекты)
	LiteUI           bool           `json:"liteUi,omitempty"`       // лёгкий режим окна: без фона и размытия, без видеокарты (меньше памяти)             // язык интерфейса: "" — русский, "en" — английский
	LitePlugins      []string       `json:"litePlugins,omitempty"`  // плагины, у которых не тянуть библиотеку пресетов при передаче
	Meta             MetaSettings   `json:"meta"`                   // метаданные рендеров и README проектов

	hub *Hub
}

var cfgMu sync.Mutex

const AppName = "FruityHub"

func ConfigDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d, _ = os.UserHomeDir()
	}
	p := filepath.Join(d, "FruityHub")
	if _, err := os.Stat(p); err != nil {
		// настройки прошлых версий («прога узбека», FL Sync) переезжают сюда — регистрация и проекты сохраняются
		for _, old := range []string{"ProgrammaUzbeka", "FLSync"} {
			od := filepath.Join(d, old)
			if _, err := os.Stat(od); err != nil {
				continue
			}
			if os.Rename(od, p) == nil {
				break
			}
			// папка занята (например, ещё открыта старая версия) — переносим хотя бы настройки
			os.MkdirAll(p, 0o755)
			ents, _ := os.ReadDir(od)
			for _, e := range ents {
				if !e.IsDir() && (strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), "background")) {
					if b, err := os.ReadFile(filepath.Join(od, e.Name())); err == nil {
						os.WriteFile(filepath.Join(p, e.Name()), b, 0o600)
					}
				}
			}
			break
		}
	}
	os.MkdirAll(p, 0o755)
	return p
}

func LoadConfig() *Config {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := &Config{}
	b, err := os.ReadFile(filepath.Join(ConfigDir(), "config.json"))
	if err == nil {
		json.Unmarshal(b, c)
	}
	SetUserProxy(c.Proxy)
	if len(c.LFSExtensions) > 0 {
		SetGlobalLFS(c.LFSExtensions)
	}
	return c
}

// MetaSettings — Настройки → Метаданные.
type MetaSettings struct {
	Off      bool     `json:"off,omitempty"`      // не записывать метаданные в рендеры
	Exts     []string `json:"exts,omitempty"`     // в какие файлы (пусто — wav, mp3, flac)
	NoCover  bool     `json:"noCover,omitempty"`  // не вшивать обложку
	NoReadme bool     `json:"noReadme,omitempty"` // не делать README.md проекта для GitHub
	All      bool     `json:"all,omitempty"`      // и в старые рендеры, а не только в новые
}

func (m MetaSettings) Extensions() []string {
	if len(m.Exts) == 0 {
		return []string{"wav", "mp3", "flac"}
	}
	return m.Exts
}

// IsLite — у этого плагина не тянуть библиотеку пресетов.
func (c *Config) IsLite(plugin string) bool {
	k := normName(plugin)
	for _, n := range c.LitePlugins {
		if normName(n) == k {
			return true
		}
	}
	return false
}

func (c *Config) SetLite(plugin string, on bool) {
	k := normName(plugin)
	var out []string
	for _, n := range c.LitePlugins {
		if normName(n) != k {
			out = append(out, n)
		}
	}
	if on {
		out = append(out, plugin)
	}
	c.LitePlugins = out
}

func (c *Config) Save() error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := filepath.Join(ConfigDir(), "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(ConfigDir(), "config.json"))
}

func (c *Config) Project(path string) *LocalProject {
	for i := range c.Projects {
		if samePath(c.Projects[i].Path, path) {
			return &c.Projects[i]
		}
	}
	return nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// MachineID is how this participant is named in shared files.
func (c *Config) MachineID() string {
	n := strings.TrimSpace(c.UserName)
	if n == "" {
		n, _ = os.Hostname()
	}
	return safeName(n)
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func OSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	return runtime.GOOS
}

// SharedSettings live in the repo (.flsync/settings.json) so both
// participants follow the same rules.
type SharedSettings struct {
	IgnoreUnused   bool     `json:"ignoreUnused"`
	CollectSamples bool     `json:"collectSamples"`
	OrganizeAudio  bool     `json:"organizeAudio"` // keep the root clean: audio goes to Audio/
	SendBackup     bool     `json:"sendBackup"`    // отправлять папку Backup (автосохранения FL) — по умолчанию нет
	AlwaysInclude  []string `json:"alwaysInclude"` // rel paths; trailing "/" = whole folder
	LFSExtensions  []string `json:"lfsExtensions"`
}

// globalLFS — список расширений для LFS из настроек этого компьютера (Настройки → GitHub); его получают новые проекты.
var globalLFS atomic.Pointer[[]string]

func SetGlobalLFS(l []string) { globalLFS.Store(&l) }

func DefaultLFSExtensions() []string {
	return []string{"wav", "mp3", "flac", "ogg", "aif", "aiff", "m4a", "wv", "rx2", "rex", "sf2",
		"opus", "zip", "mp4", "mov", "dwp", "nmsv", "fxp", "fxb", "vstpreset"}
}

func DefaultShared() SharedSettings {
	ext := DefaultLFSExtensions()
	if g := globalLFS.Load(); g != nil && len(*g) > 0 {
		ext = append([]string(nil), *g...)
	}
	return SharedSettings{
		IgnoreUnused:   true,
		CollectSamples: true,
		OrganizeAudio:  true,
		LFSExtensions:  ext,
	}
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.MarshalIndent(v, "", "  ")
	b = append(b, '\n')
	if old, err := os.ReadFile(path); err == nil && string(old) == string(b) {
		return nil // unchanged: avoid noise in git
	}
	return os.WriteFile(path, b, 0o644)
}

func LoadShared(dir string) SharedSettings {
	s := DefaultShared()
	readJSON(filepath.Join(dir, ".flsync", "settings.json"), &s)
	if len(s.LFSExtensions) == 0 {
		s.LFSExtensions = DefaultShared().LFSExtensions
	}
	return s
}

// DisplayName — название проекта в списке: своё (если задано) или имя папки.
func (lp *LocalProject) DisplayName() string {
	if n := strings.TrimSpace(lp.Name); n != "" {
		return n
	}
	return filepath.Base(lp.Path)
}
