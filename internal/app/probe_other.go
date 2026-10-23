//go:build !windows

package app

import (
	"fmt"
	"os"
)

func RunProbe(path string) {
	fmt.Println("ERR пробная загрузка есть только на Windows")
	os.Exit(0)
}
