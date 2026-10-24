module flsync

go 1.22

replace golang.org/x/sys => github.com/golang/sys v0.30.0

require (
	github.com/ebitengine/purego v0.8.4
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
)

require (
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/net v0.21.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)

replace golang.org/x/crypto => github.com/golang/crypto v0.31.0

replace golang.org/x/net => github.com/golang/net v0.33.0

replace golang.org/x/text => github.com/golang/text v0.21.0
