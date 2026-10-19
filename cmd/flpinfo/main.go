package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"flsync/internal/flp"
)

func main() {
	f, err := flp.ParseFile(os.Args[1])
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(f.Info(), "", " ")
	fmt.Println(string(b))
	raw, _ := os.ReadFile(os.Args[1])
	fmt.Println("roundtrip identical:", bytes.Equal(raw, f.Bytes()))
	n := f.RewriteSamplePaths(func(o string) string { return "/tmp/Проект/Samples/" + flp.BaseName(o) })
	g, err := flp.Parse(f.Bytes())
	fmt.Println("rewritten", n, err, g.Info().Samples)
}
