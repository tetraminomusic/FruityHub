//go:build !windows

package app

func replaceExeIcon(exe, style string) error { return nil }

func patchExeIcon(path, style string) error { return nil }

func cleanOldIcons() {}
