// Package web — интерфейс FruityHub, встраивается в приложение при сборке:
// index.html — каркас страницы, static/ — код (js), оформление (css) и тексты (i18n), картинки — рядом.
package web

import "embed"

//go:embed index.html
var Index []byte

//go:embed index.html bg.jpg icon.png icon-tile.png appicon-clear.png appicon-tile.png static
var FS embed.FS
