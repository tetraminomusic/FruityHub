//go:build !windows

package app

import "errors"

func installDir() string           { return "" }
func installedVersion() string     { return "" }
func RefreshInstallEntry()         {}
func RemoveOldExe(string)          {}
func setShortcutIcon(string) error { return nil }
func launchInstalled(string, string, bool) error {
	return errors.New("установка — только на Windows")
}
func uninstallApp(bool) error { return errors.New("установка — только на Windows") }
func installApp(*Job, bool, bool, string) (any, error) {
	return nil, errors.New("установка — только на Windows")
}
