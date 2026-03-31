//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mathias/neofs-mount/internal/cfapi"
	"github.com/mathias/neofs-mount/internal/config"
)

// runUnregisterSyncRoot calls CfUnregisterSyncRoot so Windows releases C:\NeoFS (or your mount path).
// Use when Explorer says "cloud operation is invalid" and you cannot delete or recreate the folder.
//
//	pathArg empty → use mountpoint from config.toml; otherwise must be the exact sync root (e.g. C:\NeoFS).
func runUnregisterSyncRoot(pathArg string) int {
	var root string
	if pathArg != "" {
		abs, err := filepath.Abs(pathArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid path: %v\n", err)
			printUnregisterHelp()
			return 1
		}
		root = filepath.Clean(abs)
	} else {
		cfgPath := config.DefaultConfigPath()
		cfg, err := config.Load(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not load config %s: %v\n", cfgPath, err)
			printUnregisterHelp()
			return 1
		}
		if cfg.Mountpoint == nil || strings.TrimSpace(*cfg.Mountpoint) == "" {
			fmt.Fprintln(os.Stderr, "config.toml has no mountpoint set.")
			printUnregisterHelp()
			return 1
		}
		root, _ = filepath.Abs(filepath.Clean(*cfg.Mountpoint))
	}

	fmt.Fprintf(os.Stdout, "Unregistering Cloud Sync root: %s\n", root)
	if err := cfapi.UnregisterSyncRoot(root); err != nil {
		fmt.Fprintf(os.Stderr, "\nUnregisterSyncRoot failed: %v\n\n", err)
		printUnregisterRecoverySteps()
		return 1
	}
	fmt.Fprintf(os.Stdout, "\nSuccess. The folder is no longer a Cloud Sync root.\n")
	fmt.Fprintf(os.Stdout, "Next: delete or recreate %s if you want a clean directory, then start NeoFS and mount again.\n", root)
	return 0
}

func printUnregisterHelp() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, `  neofs-mount-tray.exe -neofs-unregister-sync-root`)
	fmt.Fprintln(os.Stderr, `  neofs-mount-tray.exe -neofs-unregister-sync-root "C:\NeoFS"`)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Run from Command Prompt or PowerShell (tray app must be fully quit first).")
}

func printUnregisterRecoverySteps() {
	fmt.Fprintln(os.Stderr, "Do this, then run the same command again:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  1) Task Manager → end ALL neofs-mount-tray.exe (and neofs-mount.exe if any).")
	fmt.Fprintln(os.Stderr, "  2) Close every Explorer window.")
	fmt.Fprintln(os.Stderr, "  3) Restart Explorer (paste in Win+R or cmd):")
	fmt.Fprintln(os.Stderr, "       taskkill /IM explorer.exe /F & start explorer")
	fmt.Fprintln(os.Stderr, "  4) Retry unregister. If it still fails, open Command Prompt as Administrator and retry.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Path must match the folder that was registered (same spelling as in Settings → Mountpoint).")
}
