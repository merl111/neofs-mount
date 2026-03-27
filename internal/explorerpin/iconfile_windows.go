//go:build windows

package explorerpin

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"

	ico "github.com/Kodeworks/golang-image-ico"
)

// writeExplorerTrayICO decodes the same PNG used for the Fyne tray icon, encodes a
// Windows .ico, and returns an absolute path. Explorer reads .ico files reliably;
// a plain go-built .exe usually has no RT_GROUP_ICON, so "exe,0" shows a generic folder.
func writeExplorerTrayICO(png []byte) (string, error) {
	if len(png) == 0 {
		return "", fmt.Errorf("explorerpin: empty icon bytes")
	}
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return "", err
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("explorerpin: APPDATA not set")
	}
	dir := filepath.Join(appData, "neofs-mount")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "explorer-tray.ico")
	var buf bytes.Buffer
	if err := ico.Encode(&buf, img); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, buf.Bytes(), 0644); err != nil {
		return "", err
	}
	return filepath.Abs(out)
}
