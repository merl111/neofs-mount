//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/mathias/neofs-mount/internal/config"
	"github.com/mathias/neofs-mount/internal/fs"
	"github.com/mathias/neofs-mount/internal/neofs"

	"golang.org/x/sys/windows"
)

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

const (
	mbYesNo       = 0x00000004
	mbOk          = 0x00000000
	mbIconWarning = 0x00000030
	mbIconInfo    = 0x00000040
	idYes         = 6
)

func winMessageBox(title, text string, flags uint32) uintptr {
	t, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return 0
	}
	tit, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	r, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(tit)), uintptr(flags))
	return r
}

func runNeoFSDeleteFromContainer(targetPath string) int {
	cfgPath := config.DefaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		winMessageBox("NeoFS Mount", fmt.Sprintf("Could not load config:\n%v", err), mbOk|mbIconWarning)
		return 1
	}
	if cfg.Mountpoint == nil || *cfg.Mountpoint == "" {
		winMessageBox("NeoFS Mount", "Mountpoint is not set in config.", mbOk|mbIconWarning)
		return 1
	}
	if cfg.Endpoint == nil || cfg.WalletKey == nil {
		winMessageBox("NeoFS Mount", "Endpoint or wallet key missing in config.", mbOk|mbIconWarning)
		return 1
	}

	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		winMessageBox("NeoFS Mount", err.Error(), mbOk|mbIconWarning)
		return 1
	}
	absMount, err := filepath.Abs(*cfg.Mountpoint)
	if err != nil {
		winMessageBox("NeoFS Mount", err.Error(), mbOk|mbIconWarning)
		return 1
	}
	if !pathHasPrefixFold(absTarget, absMount) {
		winMessageBox("NeoFS Mount", fmt.Sprintf("Not under NeoFS mount:\n%s", absTarget), mbOk|mbIconWarning)
		return 1
	}

	msg := fmt.Sprintf("Permanently remove all NeoFS objects at this path from the container?\n\n%s\n\nThis cannot be undone.", absTarget)
	if winMessageBox("Delete from NeoFS container", msg, mbYesNo|mbIconWarning) != idYes {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	neo, err := neofs.New(ctx, neofs.Params{
		Endpoint:  *cfg.Endpoint,
		WalletKey: *cfg.WalletKey,
	})
	if err != nil {
		winMessageBox("NeoFS Mount", fmt.Sprintf("NeoFS connection failed:\n%v", err), mbOk|mbIconWarning)
		return 1
	}
	defer neo.Close()

	ignore := fs.IgnoreSetFromIDs(cfg.IgnoreContainerIDs)
	n, cnrStr, neoRel, err := fs.DeleteNeoFSAtMountPath(ctx, neo, absMount, absTarget, ignore)
	if err != nil {
		winMessageBox("NeoFS Mount", fmt.Sprintf("Delete failed:\n%v", err), mbOk|mbIconWarning)
		return 1
	}

	_ = os.RemoveAll(absTarget)

	if n == 0 {
		winMessageBox("NeoFS Mount", fmt.Sprintf("No NeoFS objects matched this path.\n\nContainer: %s\nPath: %s\n\nLocal item was removed if it still existed.", cnrStr, neoRel), mbOk|mbIconInfo)
		return 0
	}

	winMessageBox("NeoFS Mount", fmt.Sprintf("Deleted %d NeoFS object(s).\n\nContainer: %s\nPath: %s", n, cnrStr, neoRel), mbOk|mbIconInfo)
	return 0
}
