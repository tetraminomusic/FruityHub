package app

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---------- докачка с места обрыва ----------
//
// Отправитель держит собранный архив плагина, пока передача не закончится, и помечает его случайным id.
// Получатель хранит недокачанное начало файла (.part) и id архива. При повторе получатель говорит,
// сколько у него уже есть, отправитель шлёт только остаток. Если архив другой (пересобран) — с нуля.

// appCaps — что умеет это приложение (сервер передаёт это отправителю плагина).
const appCaps = "fetch,zstd,resume"

func hasCap(caps, c string) bool {
	for _, x := range strings.Split(caps, ",") {
		if strings.TrimSpace(x) == c {
			return true
		}
	}
	return false
}

// sendZipPath — где лежит собранный архив; у переустановки начисто — свой (в нём все зависимости).
func sendZipPath(id string, reinstall bool) string {
	n := "fruityhub-send-" + id
	if reinstall {
		n += "-re"
	}
	return filepath.Join(os.TempDir(), n+".zip")
}

// packedID — id собранного архива ("" — архива нет или он собран не до конца).
func packedID(path string) string {
	b, err := os.ReadFile(path + ".id")
	if err != nil || !exists(path) {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func markPacked(path string) error {
	b := make([]byte, 12)
	rand.Read(b)
	return os.WriteFile(path+".id", []byte(hex.EncodeToString(b)), 0o644)
}

func dropSendZip(id string) {
	for _, re := range []bool{false, true} {
		p := sendZipPath(id, re)
		os.Remove(p)
		os.Remove(p + ".id")
	}
}

// partInfo — что уже получено: id архива и сколько байт ("" — ничего).
func partInfo(dst string) (string, int64) {
	b, err := os.ReadFile(dst + ".part.id")
	if err != nil {
		return "", 0
	}
	st, err := os.Stat(dst + ".part")
	if err != nil || st.Size() == 0 {
		return "", 0
	}
	return strings.TrimSpace(string(b)), st.Size()
}

func savePartID(dst, id string) {
	if id == "" {
		os.Remove(dst + ".part.id")
		return
	}
	os.WriteFile(dst+".part.id", []byte(id), 0o644)
}

func dropPart(dst string) {
	os.Remove(dst + ".part")
	os.Remove(dst + ".part.id")
}

func resumeHeaders(set func(k, v string), dst string) {
	if id, n := partInfo(dst); id != "" {
		set("X-Have-File", id)
		set("X-Have-Bytes", strconv.FormatInt(n, 10))
	}
}

// cleanStaleTransfers убирает брошенные архивы и недокачанные файлы старше двух суток.
func cleanStaleTransfers() {
	ents, _ := os.ReadDir(os.TempDir())
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "fruityhub-send-") && !(strings.HasPrefix(n, "uzbek-plugin-") && strings.Contains(n, ".part")) {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 48*time.Hour {
			os.Remove(filepath.Join(os.TempDir(), n))
		}
	}
}
