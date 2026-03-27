package main

import (
	"fmt"
	"io"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mathias/neofs-mount/internal/config"
)

const auditPreviewMaxBytes = 512 * 1024

func readAuditPreview(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "No audit file yet.\n\nEvents appear after mount operations (upload, delete, etc.)."
		}
		return fmt.Sprintf("Error: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("Error opening file: %v", err)
	}
	defer f.Close()

	start := int64(0)
	if fi.Size() > auditPreviewMaxBytes {
		start = fi.Size() - auditPreviewMaxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return fmt.Sprintf("Error seeking: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("Error reading: %v", err)
	}
	out := string(data)
	if start > 0 {
		out = fmt.Sprintf("… (showing last %d KB of file)\n\n%s", auditPreviewMaxBytes/1024, out)
	}
	return out
}

func showAuditLogWindow(a fyne.App, _ fyne.Window) {
	aw := a.NewWindow("NeoFS operations audit")
	aw.Resize(fyne.NewSize(760, 480))

	pathLabel := widget.NewLabel("")
	body := widget.NewMultiLineEntry()
	body.Wrapping = fyne.TextWrapOff
	body.TextStyle = fyne.TextStyle{Monospace: true}

	refresh := func() {
		cfgPath := config.DefaultConfigPath()
		cfg, err := config.Load(cfgPath)
		if err != nil {
			cfg = &config.FileConfig{}
		}
		p := config.ResolveAuditLogPath(cfg)
		if p == "" {
			pathLabel.SetText("Audit logging is disabled in Settings.")
			body.Enable()
			body.SetText("Enable “Record NeoFS operations to audit log” in Settings, save, then remount.")
			body.Disable()
			return
		}
		pathLabel.SetText("File: " + p)
		body.Enable()
		body.SetText(readAuditPreview(p))
		body.Disable()
	}

	refreshBtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), refresh)
	top := container.NewBorder(nil, nil, nil, refreshBtn, pathLabel)

	scroll := container.NewScroll(body)
	scroll.SetMinSize(fyne.NewSize(700, 400))

	aw.SetContent(container.NewBorder(top, nil, nil, nil, scroll))
	refresh()
	aw.Show()
}
