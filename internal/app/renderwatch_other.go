//go:build !windows

package app

func renderChanged(path string) bool           { return true }
func pruneRenderWatches(paths map[string]bool) {}
