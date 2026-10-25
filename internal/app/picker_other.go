//go:build !windows

package app

import "errors"

// nativePick — только на Windows (на Mac и так системное окно Finder).
func nativePick(title string, folder bool, filters [][2]string, start string) (string, error) {
	return "", errors.New("нет")
}
