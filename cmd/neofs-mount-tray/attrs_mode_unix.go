//go:build !windows

package main

import (
	"os"
)

func tryNeoFSAttrsMode() (done bool, exitCode int) {
	if len(os.Args) >= 3 && os.Args[1] == "-neofs-delete" {
		return true, runNeoFSDeleteFromContainer(os.Args[2])
	}
	if len(os.Args) >= 3 && os.Args[1] == "-neofs-attrs" {
		return true, runNeoFSAttrsViewer(os.Args[2])
	}
	return false, 0
}
