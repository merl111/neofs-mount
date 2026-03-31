//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/mathias/neofs-mount/internal/config"
	"github.com/mathias/neofs-mount/internal/fs"
	"github.com/mathias/neofs-mount/internal/neofs"
)

func runNeoFSDeleteFromContainer(targetPath string) int {
	cfgPath := config.DefaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return runDeleteMessageScreen("NeoFS", fmt.Sprintf("Could not load config:\n%v", err), 1)
	}
	if cfg.Mountpoint == nil || *cfg.Mountpoint == "" {
		return runDeleteMessageScreen("NeoFS", "Mountpoint is not set in config.", 1)
	}
	if cfg.Endpoint == nil || cfg.WalletKey == nil {
		return runDeleteMessageScreen("NeoFS", "Endpoint or wallet key missing in config.", 1)
	}

	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return runDeleteMessageScreen("NeoFS", err.Error(), 1)
	}
	absMount, err := filepath.Abs(*cfg.Mountpoint)
	if err != nil {
		return runDeleteMessageScreen("NeoFS", err.Error(), 1)
	}
	if !pathHasPrefixFold(absTarget, absMount) {
		return runDeleteMessageScreen("NeoFS", fmt.Sprintf("Not under NeoFS mount:\n%s", absTarget), 1)
	}

	msg := fmt.Sprintf("Permanently remove all NeoFS objects at this path from the container?\n\n%s\n\nThis cannot be undone.", absTarget)

	exit := 1
	a := app.NewWithID("org.neofs.mount.delete")
	a.SetIcon(resourceLogoPng)
	w := a.NewWindow("Delete from NeoFS container")
	w.SetIcon(resourceLogoPng)
	w.Resize(fyne.NewSize(520, 220))
	w.SetFixedSize(true)

	dialog.ShowConfirm("Delete from NeoFS container", msg, func(ok bool) {
		if !ok {
			exit = 0
			w.Close()
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		neo, err := neofs.New(ctx, neofs.Params{
			Endpoint:  *cfg.Endpoint,
			WalletKey: *cfg.WalletKey,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "neofs-mount-tray: NeoFS connection failed: %v\n", err)
			exit = 1
			w.Close()
			return
		}
		defer neo.Close()

		ignore := fs.IgnoreSetFromIDs(cfg.IgnoreContainerIDs)
		n, cnrStr, neoRel, err := fs.DeleteNeoFSAtMountPath(ctx, neo, absMount, absTarget, ignore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "neofs-mount-tray: delete failed: %v\n", err)
			exit = 1
			w.Close()
			return
		}

		_ = os.RemoveAll(absTarget)

		var info string
		if n == 0 {
			info = fmt.Sprintf("No NeoFS objects matched this path.\n\nContainer: %s\nPath: %s\n\nLocal item was removed if it still existed.", cnrStr, neoRel)
		} else {
			info = fmt.Sprintf("Deleted %d NeoFS object(s).\n\nContainer: %s\nPath: %s", n, cnrStr, neoRel)
		}

		exit = 0
		lbl := widget.NewLabel(info)
		lbl.Wrapping = fyne.TextWrapWord
		okBtn := widget.NewButton("OK", func() { w.Close() })
		w.SetContent(container.NewBorder(nil, container.NewPadded(okBtn), nil, nil, container.NewPadded(lbl)))
	}, w)

	w.SetOnClosed(func() { a.Quit() })
	w.Show()
	a.Run()
	return exit
}

func runDeleteMessageScreen(title, message string, code int) int {
	a := app.NewWithID("org.neofs.mount.deleteerr")
	a.SetIcon(resourceLogoPng)
	w := a.NewWindow(title)
	w.SetIcon(resourceLogoPng)
	w.Resize(fyne.NewSize(440, 180))
	w.SetFixedSize(true)
	lbl := widget.NewLabel(message)
	lbl.Wrapping = fyne.TextWrapWord
	w.SetContent(container.NewBorder(nil, container.NewPadded(widget.NewButton("OK", func() { w.Close() })), nil, nil, container.NewPadded(lbl)))
	w.SetOnClosed(func() { a.Quit() })
	w.Show()
	a.Run()
	return code
}
