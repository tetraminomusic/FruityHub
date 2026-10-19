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
}

// focus brings the existing window to the front (set by Run).
var focus = func() {}

// Focus is called when the user starts the app a second time.
func Focus() { focus() }
