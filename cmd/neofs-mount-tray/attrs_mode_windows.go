//go:build windows

package main

import (
	"os"
	"strings"
)

func tryNeoFSAttrsMode() (done bool, exitCode int) {
	if len(os.Args) >= 2 && os.Args[1] == "-neofs-unregister-sync-root" {
		path := ""
		if len(os.Args) >= 3 {
			path = strings.TrimSpace(os.Args[2])
			path = strings.Trim(path, `"`)
		}
		return true, runUnregisterSyncRoot(path)
	}
	if len(os.Args) >= 3 && os.Args[1] == "-neofs-delete" {
		return true, runNeoFSDeleteFromContainer(os.Args[2])
	}
	if len(os.Args) >= 3 && os.Args[1] == "-neofs-attrs" {
		return true, runNeoFSAttrsViewer(os.Args[2])
	}
	return false, 0
}
