//go:build windows

package app

import (
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemProxy читает «Параметры → Сеть → Прокси» (то, чем пользуются браузеры).
func systemProxy() (u *url.URL, raw string, enabled bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, "", false
	}
	defer k.Close()
	srv, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(srv) == "" {
		return nil, "", false
	}
	en, _, _ := k.GetIntegerValue("ProxyEnable")
	pick := srv
	if strings.Contains(srv, "=") {
		pick = ""
		want := []string{"https=", "http=", "socks="}
		for _, w := range want {
			for _, part := range strings.Split(srv, ";") {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, w) {
					pick = strings.TrimPrefix(part, w)
					if w == "socks=" {
						pick = "socks5://" + pick
					}
					break
				}
			}
			if pick != "" {
				break
			}
		}
	}
	u = parseProxy(pick)
	return u, pick, en == 1
}
