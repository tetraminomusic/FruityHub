//go:build !windows

package app

import "net/url"

func systemProxy() (*url.URL, string, bool) { return nil, "", false }
