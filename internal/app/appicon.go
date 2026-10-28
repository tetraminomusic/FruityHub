package app

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// Иконка приложения (Настройки → Вид): без фона (по умолчанию) или на тёмной плитке, как в 0.9.9.
//
// Windows: в exe вшиты три набора иконок — 2 (по умолчанию), 10 (плитка), 18 (без фона). Окно и панель задач
// меняют иконку сразу (WM_SETICON), а у установленного FruityHub — ещё и ярлыки на рабочем столе и в «Пуске».
// Сам exe не переписывается (антивирусы принимают такое за вирус).
// macOS: значок в Доке — сразу, у FruityHub.app в Finder — своя иконка файла; после обновления ставится снова при запуске.

const (
	IconClear = ""
	IconTile  = "tile"
)

const (
	iconGroupShown = 2
	iconGroupTile  = 10
	iconGroupClear = 18
)

// IconGroup — какой набор иконок из exe брать для окна (Windows).
func IconGroup(style string) int {
	if style == IconTile {
		return iconGroupTile
	}
	return iconGroupClear
}

// AppIconPNG — картинка для Дока macOS: плитка или без фона.
func AppIconPNG(assets fs.FS, style string) []byte {
	if assets == nil {
		return nil
	}
	n := "appicon-clear.png"
	if style == IconTile {
		n = "appicon-tile.png"
	}
	b, _ := fs.ReadFile(assets, n)
	return b
}

// IconStyle — выбранная иконка приложения.
func (s *Server) IconStyle() string { return s.cfg.IconStyle }

// OnIconChange — поменять иконку окна / Дока сейчас (ставит main, когда есть окно).
var OnIconChange func(style string)

// AppBundle — путь к FruityHub.app (macOS) или "" (запуск не из бандла).
func AppBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, _ = filepath.EvalSymlinks(exe)
	b := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if filepath.Ext(b) == ".app" {
		return b
	}
	return ""
}

func (s *Server) setIconStyle(style string, j *Job) error {
	if style != IconTile {
		style = IconClear
	}
	s.cfg.IconStyle = style
	if err := s.cfg.Save(); err != nil {
		return err
	}
	stampReset()
	if OnIconChange != nil {
		OnIconChange(style)
	}
	if runtime.GOOS == "windows" {
		if err := setShortcutIcon(style); err != nil {
			j.Logf("Ярлыки не обновил: %v", err)
		}
	}
	return nil // в браузере (Linux) — только иконка в шапке
}

func (s *Server) routesIcon(api func(string, func(r *http.Request) (any, error))) {
	api("/api/appicon", func(r *http.Request) (any, error) {
		var req struct {
			Style string `json:"style"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		j := s.startJobOpt("Меняю иконку приложения", "", false, func(j *Job) (any, error) {
			if err := s.setIconStyle(req.Style, j); err != nil {
				return nil, err
			}
			j.Logf("✓ Иконка поменялась")
			return "ok", nil
		})
		return map[string]string{"id": j.ID, "job": j.ID, "title": j.Title}, nil
	})
}
