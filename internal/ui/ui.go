// Package ui shows the app in a native window with an embedded web view
// (WKWebView on macOS, WebView2 on Windows).
package ui

// Options for the main window.
type Options struct {
	URL      string
	Title    string
	Width    int
	Height   int
	DataPath string          // WebView2 profile folder (Windows)
	Quit     <-chan struct{} // close the window when this fires
	Lite     bool            // лёгкий режим: без видеокарты и лишних служб WebView2 (меньше памяти на слабых компьютерах)
	IconID   int             // набор иконок из exe для окна (Windows)
	IconPNG  []byte          // своя картинка для Дока (macOS), nil — иконка бандла
	Bundle   string          // путь к FruityHub.app (macOS) — для своей иконки в Finder
}

// SetIcon меняет иконку уже открытого окна: id — набор иконок из exe (Windows), png — для Дока и Finder (macOS),
// custom=false — вернуть иконку бандла.
func SetIcon(id int, png []byte, custom bool) { setIcon(id, png, custom) }

var setIcon = func(id int, png []byte, custom bool) {}

// Handle — системный номер окна (HWND на Windows), 0 — окна нет.
var Handle = func() uintptr { return 0 }

// focus brings the existing window to the front (set by Run).
var focus = func() {}

// Focus is called when the user starts the app a second time.
func Focus() { focus() }
