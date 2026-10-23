package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// RunProbe — режим «пробная загрузка плагина» (FruityHub.exe запускает сам себя с FRUITYHUB_PROBE=путь).
// Печатает OK … или ERR причина. Плагин не выгружается: некоторые падают при выгрузке.
func RunProbe(path string) {
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH)
	if err != nil {
		fmt.Println("ERR " + describeLoadErr(path, err))
		os.Exit(0)
	}
	for _, n := range []string{"GetPluginFactory", "VSTPluginMain", "clap_entry", "main"} {
		if _, e := windows.GetProcAddress(h, n); e == nil {
			fmt.Println("OK (" + map[string]string{"GetPluginFactory": "VST3", "VSTPluginMain": "VST2", "main": "VST2", "clap_entry": "CLAP"}[n] + ")")
			os.Exit(0)
		}
	}
	fmt.Println("ERR файл загрузился, но в нём нет точки входа VST/CLAP — это не сам плагин или он повреждён")
	os.Exit(0)
}

func describeLoadErr(path string, err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		switch en {
		case 126: // ERROR_MOD_NOT_FOUND
			var miss []string
			for _, l := range binImports(path) {
				ll := strings.ToLower(l)
				if strings.HasPrefix(ll, "api-ms-") || strings.HasPrefix(ll, "ext-ms-") {
					continue
				}
				if findLib(l, winLibSearchDirs(filepath.Dir(path))) == "" {
					miss = append(miss, l)
				}
			}
			if len(miss) > 0 {
				return "не хватает библиотек: " + strings.Join(miss, ", ") + " (их ставит установщик плагина или Microsoft Visual C++)"
			}
			return "не хватает библиотеки, которую плагин грузит сам (её ставит установщик плагина или программа производителя)"
		case 193: // ERROR_BAD_EXE_FORMAT
			return "файл не для этой системы или разрядности (32/64 бит)"
		case 1114: // ERROR_DLL_INIT_FAILED
			return "плагин отказался запускаться (ошибка инициализации) — обычно защита/лицензия или нет его файлов данных"
		case 5:
			return "нет доступа к файлу плагина (права)"
		case 2, 3:
			return "файл плагина не найден"
		}
		return fmt.Sprintf("ошибка Windows %d: %s", uint32(en), err.Error())
	}
	return err.Error()
}
