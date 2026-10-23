// Package web — интерфейс FruityHub (одна страница), встраивается в приложение при сборке.
package web

import "embed"

//go:embed index.html
var Index []byte

//go:embed index.html bg.jpg icon.png
var FS embed.FS
