package app

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Небольшие файлы к ссылке плагина: кладутся в хранилище проекта на GitHub, в общем каталоге проекта
// (.flsync/catalog.json) остаётся запись — друг видит файл после «Получить» и скачивает его в «Загрузки».

const linkFileMax = 50 << 20

func linkAssetName(plugin, file string) string {
	h := sha1.Sum([]byte(normName(plugin) + "/" + file))
	return "plugfile-" + hex.EncodeToString(h[:6]) + strings.ToLower(filepath.Ext(file))
}

func (p *Project) linkStorage() (*Storage, error) {
	r := p.git().Remote()
	if r == "" {
		return nil, errors.New("сначала подключи проект (создай репозиторий) — файлы хранятся вместе с ним на GitHub")
	}
	return OpenStorage(p.Cfg, r)
}

func (p *Project) AttachLinkFile(job *Job, plugin, src string) (*LinkFile, error) {
	st, err := os.Stat(src)
	if err != nil || st.IsDir() {
		return nil, errors.New("файл не найден")
	}
	if st.Size() > linkFileMax {
		return nil, fmt.Errorf("файл %s — слишком большой: можно до 50 МБ", fmtBytes(st.Size()))
	}
	s, err := p.linkStorage()
	if err != nil {
		return nil, err
	}
	name := filepath.Base(src)
	asset := linkAssetName(plugin, name)
	if as, err := s.Assets(); err == nil { // тот же файл ещё раз — заменяем
		for _, a := range as {
			if a.Name == asset {
				s.Delete(a.ID)
			}
		}
	}
	job.Logf("Загружаю %s…", name)
	if _, err := s.Upload(job, asset, name, src); err != nil {
		return nil, err
	}
	lf := LinkFile{Name: name, Size: st.Size(), Asset: asset, By: p.Cfg.MachineID()}
	c := p.Catalog()
	k := normName(plugin)
	e := c[k]
	var files []LinkFile
	for _, f := range e.Files {
		if f.Name != name {
			files = append(files, f)
		}
	}
	e.Files = append(files, lf)
	c[k] = e
	if err := writeJSON(filepath.Join(p.Dir, ".flsync", "catalog.json"), c); err != nil {
		return nil, err
	}
	job.Logf("✓ Файл прикреплён. Друг увидит его после того, как ты нажмёшь «Отправить», а он — «Получить»")
	return &lf, nil
}

func (p *Project) linkFile(plugin, name string) (*LinkFile, error) {
	for _, f := range p.Catalog()[normName(plugin)].Files {
		if f.Name == name {
			ff := f
			return &ff, nil
		}
	}
	return nil, errors.New("такого файла нет")
}

// FetchLinkFile скачивает прикреплённый файл в «Загрузки».
func (p *Project) FetchLinkFile(job *Job, plugin, name string) (string, error) {
	lf, err := p.linkFile(plugin, name)
	if err != nil {
		return "", err
	}
	s, err := p.linkStorage()
	if err != nil {
		return "", err
	}
	as, err := s.Assets()
	if err != nil {
		return "", err
	}
	for _, a := range as {
		if a.Name == lf.Asset {
			home, _ := os.UserHomeDir()
			dst, _, _ := uniqueTarget(filepath.Join(home, "Downloads"), lf.Name, "")
			if err := s.Download(job, a, dst); err != nil {
				return "", err
			}
			job.Logf("✓ Сохранено в «Загрузки»: %s", filepath.Base(dst))
			return dst, nil
		}
	}
	return "", errors.New("файл не найден в хранилище проекта — возможно, его удалили")
}

func (p *Project) RemoveLinkFile(plugin, name string) error {
	lf, err := p.linkFile(plugin, name)
	if err != nil {
		return err
	}
	if s, err := p.linkStorage(); err == nil {
		if as, err := s.Assets(); err == nil {
			for _, a := range as {
				if a.Name == lf.Asset {
					s.Delete(a.ID)
				}
			}
		}
	}
	c := p.Catalog()
	k := normName(plugin)
	e := c[k]
	var files []LinkFile
	for _, f := range e.Files {
		if f.Name != name {
			files = append(files, f)
		}
	}
	e.Files = files
	if e.URL == "" && e.Note == "" && len(files) == 0 {
		delete(c, k)
	} else {
		c[k] = e
	}
	return writeJSON(filepath.Join(p.Dir, ".flsync", "catalog.json"), c)
}
