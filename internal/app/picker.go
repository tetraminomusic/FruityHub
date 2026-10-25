package app

import (
	"errors"
	"strings"
	"sync"
)

// ---------- окна выбора папки и файла ----------
// Одновременно открыто только одно: повторное нажатие «Выбрать…», пока окно открыто, ничего не делает.

var pickMu sync.Mutex

// PickerOwner — окно FruityHub (на Windows окно выбора будет поверх него и модальным). Задаёт main.
var PickerOwner func() uintptr

var errPickerOpen = errors.New("окно выбора уже открыто — оно может быть за окном FruityHub")

func guardPick(fn func() (string, error)) (string, error) {
	if !pickMu.TryLock() {
		return "", errPickerOpen
	}
	defer pickMu.Unlock()
	p, err := fn()
	return strings.TrimSpace(p), err
}

// PickFolder — выбрать папку (на Windows — обычным Проводником).
func PickFolder() (string, error) {
	return guardPick(func() (string, error) {
		if p, err := nativePick("Выбери папку", true, nil, ""); err == nil {
			return p, nil
		}
		return osPickFolder()
	})
}

// PickAudioFile — выбрать трек (mp3, flac, wav).
func PickAudioFile() (string, error) {
	return guardPick(func() (string, error) {
		if p, err := nativePick("Трек для прослушивания", false, [][2]string{{"Аудио (mp3, flac, wav)", "*.mp3;*.flac;*.wav"}}, ""); err == nil {
			return p, nil
		}
		return osPickAudioFile()
	})
}

// PickAnyFile — выбрать любой файл.
func PickAnyFile() (string, error) {
	return guardPick(func() (string, error) {
		if p, err := nativePick("Выбери файл", false, [][2]string{{"Все файлы", "*.*"}}, ""); err == nil {
			return p, nil
		}
		return osPickAnyFile()
	})
}

// PickImageFile — выбрать картинку (обложка, фон).
func PickImageFile() (string, error) {
	return guardPick(func() (string, error) {
		if p, err := nativePick("Выбери картинку", false, [][2]string{{"Картинки (jpg, png, webp)", "*.jpg;*.jpeg;*.png;*.webp"}}, ""); err == nil {
			return p, nil
		}
		return osPickImageFile()
	})
}
