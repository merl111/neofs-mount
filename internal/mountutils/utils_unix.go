//go:build !windows

package mountutils

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// openLogDirectory opens the log directory using xdg-open (Linux) or open (macOS).
func openLogDirectory() error {
	dir := filepath.Dir(LogFilePath())
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", dir)
	} else {
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

func isNotConn(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return errors.Is(pe.Err, syscall.ENOTCONN)
	}
	return errors.Is(err, syscall.ENOTCONN)
}

func tryUnmount(path string) error {
	if err := syscall.Unmount(path, 0); err == nil {
		return nil
	}
	switch runtime.GOOS {
	case "linux":
		if p, err := exec.LookPath("fusermount3"); err == nil {
			if out, err := exec.Command(p, "-u", "-z", path).CombinedOutput(); err == nil {
				_ = out
				return nil
			} else {
				return fmt.Errorf("fusermount3 -u -z failed: %w", err)
			}
		}
		if p, err := exec.LookPath("fusermount"); err == nil {
			if out, err := exec.Command(p, "-u", "-z", path).CombinedOutput(); err == nil {
				_ = out
				return nil
			} else {
				return fmt.Errorf("fusermount -u -z failed: %w", err)
			}
		}
	case "darwin":
		if p, err := exec.LookPath("umount"); err == nil {
			if out, err := exec.Command(p, "-f", path).CombinedOutput(); err == nil {
				_ = out
				return nil
			} else {
				return fmt.Errorf("umount -f failed: %w", err)
			}
		}
	}
	return fmt.Errorf("unmount failed (no helper available): %s", path)
}

func staleUnmountHelp(path string) string {
	switch runtime.GOOS {
	case "linux":
		return fmt.Sprintf("Try:\n  fusermount3 -u -z %s\n  # or\n  fusermount -u -z %s", path, path)
	case "darwin":
		return fmt.Sprintf("File Provider: disconnect via the neoFS Mount app. Legacy FUSE: umount -f %s", path)
	default:
		return fmt.Sprintf("Try unmounting the path: %s", path)
	}
}
