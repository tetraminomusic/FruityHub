package app

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Установка на Windows: FruityHub — один exe, который сам ставит себя как обычная программа.
// Запущен не из папки программы (с рабочего стола, из «Загрузок») — вместо главного экрана показывается
// окно установки: куда, ярлыки, убрать ли этот файл. Всё делается по кнопке «Установить», с шагами на экране.
// Программа — %LOCALAPPDATA%\Programs\FruityHub (без прав администратора: обновления тоже без вопросов),
// ярлыки — рабочий стол и «Пуск», удаление — «Приложения» Windows (FruityHub.exe --uninstall).
// Настройки, вход и кеш окна остаются, где были: %APPDATA%\FruityHub — переустановка их не трогает.

type InstallState struct {
	Supported        bool   `json:"supported"`        // Windows
	Installed        bool   `json:"installed"`        // запущен из папки программы
	Show             bool   `json:"show"`             // показать окно установки вместо главного экрана
	Uninstall        bool   `json:"uninstall"`        // запущен для удаления (из «Приложений»)
	Mode             string `json:"mode"`             // install — первый раз; move — старая версия с рабочего стола переезжает; update — поверх установленной
	Exe              string `json:"exe"`              // этот файл
	Dir              string `json:"dir"`              // папка программы
	Version          string `json:"version"`          // версия этого файла
	InstalledVersion string `json:"installedVersion"` // уже установленная (если есть)
	OldProtected     bool   `json:"oldProtected"`     // этот файл в защищённой папке (общий рабочий стол) — убрать его Windows разрешит только с подтверждением
}

// UninstallMode — запущено как «FruityHub.exe --uninstall».
var UninstallMode bool

func thisExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe
}

func (s *Server) installState() InstallState {
	st := InstallState{Supported: runtime.GOOS == "windows", Version: Version, Exe: thisExe(), Uninstall: UninstallMode}
	if !st.Supported {
		return st
	}
	st.Dir = installDir()
	st.Installed = st.Dir != "" && samePath(filepath.Dir(st.Exe), st.Dir)
	st.InstalledVersion = installedVersion()
	if !st.Installed {
		st.OldProtected = exeDirProtected(st.Exe)
		switch {
		case st.InstalledVersion != "":
			st.Mode = "update"
		case s.cfg.UserName != "" || len(s.cfg.Projects) > 0:
			st.Mode = "move"
		default:
			st.Mode = "install"
		}
		st.Show = !s.cfg.InstallDeclined && Version != "dev"
	}
	return st
}

func (s *Server) routesInstall(api func(string, func(r *http.Request) (any, error))) {
	api("/api/install", func(r *http.Request) (any, error) {
		if r.Method == "GET" {
			return s.installState(), nil
		}
		var req struct {
			Desktop   bool `json:"desktop"`
			StartMenu bool `json:"startMenu"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		j := s.startJobOpt("Установка FruityHub", "", false, func(j *Job) (any, error) {
			return installApp(j, req.Desktop, req.StartMenu, s.cfg.IconStyle)
		})
		return map[string]string{"id": j.ID, "job": j.ID, "title": j.Title}, nil
	})
	// «Запускать без установки» — больше не спрашивать
	api("/api/install/skip", func(r *http.Request) (any, error) {
		s.cfg.InstallDeclined = true
		return "ok", s.cfg.Save()
	})
	// запустить установленный FruityHub и закрыться; removeOld — установленный уберёт этот файл, когда он закроется
	api("/api/install/launch", func(r *http.Request) (any, error) {
		var req struct {
			RemoveOld bool `json:"removeOld"`
		}
		decode(r, &req)
		st := s.installState()
		if st.Installed {
			return nil, errors.New("FruityHub и так запущен из папки программы")
		}
		if err := launchInstalled(st.Dir, st.Exe, req.RemoveOld); err != nil {
			return nil, err
		}
		go func() { time.Sleep(400 * time.Millisecond); s.Stop() }()
		return "ok", nil
	})
	api("/api/uninstall", func(r *http.Request) (any, error) {
		var req struct {
			Settings bool `json:"settings"` // удалить и настройки, вход, кеш
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := uninstallApp(req.Settings); err != nil {
			return nil, err
		}
		go func() { time.Sleep(2500 * time.Millisecond); s.Stop() }()
		return "ok", nil
	})
}

var protOnce struct {
	sync.Once
	v bool
}

// exeDirProtected — можно ли без прав администратора писать в папку этого exe (проверяется один раз:
// проверка пишет пробный файл, а состояние спрашивается часто).
func exeDirProtected(exe string) bool {
	protOnce.Do(func() { protOnce.v = !writableDir(filepath.Dir(exe)) })
	return protOnce.v
}
