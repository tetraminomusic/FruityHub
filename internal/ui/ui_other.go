//go:build !windows && !darwin

package ui

import "errors"

// Run: no native window on this platform — the caller falls back to a browser.
func Run(o Options) error {
	return errors.New("нет встроенного окна для этой ОС")
}
