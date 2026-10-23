//go:build !windows && !darwin

package app

import (
	"os"
	"strings"
)

func runElevBatch(ops []elevOp, log string) error {
	var lines []string
	for _, o := range ops {
		lines = append(lines, "нет прав на запись: "+o.B)
	}
	os.WriteFile(log, []byte(strings.Join(lines, "\n")), 0o644)
	return nil
}
