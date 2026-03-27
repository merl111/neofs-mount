package main

// Regenerate Windows executable icon from the same PNG as fyne bundle (bundled.go).
// Outputs live under win/pe-rsrc/ (not cmd/) so Linux and fyne-cross builds do not link COFF .syso objects.
// From repo root:
//
//	go generate ./cmd/neofs-mount-tray
//	go generate ./cmd/neofs-mount
//
//go:generate go run ../../tools/genwinicon -bundled bundled.go -o app.ico
//go:generate go run github.com/akavel/rsrc@v0.10.2 -ico app.ico -arch amd64 -o ../../win/pe-rsrc/neofs-mount-tray.syso
