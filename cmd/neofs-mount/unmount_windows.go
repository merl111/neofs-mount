//go:build windows

package main

import "fmt"

func tryUnmount(string) error {
	// CfApi has no Unix-style mount; nothing to unmount from the shell.
	return nil
}

func staleUnmountHelp(path string) string {
	return fmt.Sprintf("Stop the neofs-mount process or disconnect CfApi for: %s", path)
}
