//go:build windows

package mountutils

import (
	"os/exec"
	"path/filepath"
)

// openLogDirectory opens the log directory in Windows Explorer.
func openLogDirectory() error {
	dir := filepath.Dir(LogFilePath())
	return exec.Command("explorer.exe", dir).Start()
}

// isNotConn always returns false on Windows — stale FUSE mounts don't occur.
func isNotConn(_ error) bool { return false }

// tryUnmount is a no-op on Windows; CfApi manages unmounting.
func tryUnmount(_ string) error { return nil }

// staleUnmountHelp returns a Windows-appropriate hint (unused in practice).
func staleUnmountHelp(path string) string {
	return "Stale FUSE mounts do not occur on Windows. Check folder: " + path
}
