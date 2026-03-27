package main

// Windows PE icon (same artwork as neofs-mount-tray). The .ico lives in the tray package;
// regenerate both after logo changes:
//
//	go generate ./cmd/neofs-mount-tray
//	go generate ./cmd/neofs-mount
//
//go:generate go run github.com/akavel/rsrc@v0.10.2 -ico ../neofs-mount-tray/app.ico -arch amd64 -o rsrc.syso
