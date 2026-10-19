//go:build !windows

package app

import "errors"

func elevatedReplace(src, dst string) error {
	return errors.New("нет прав на запись в " + dst)
}
