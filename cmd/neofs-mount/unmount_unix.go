//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
)

func tryUnmount(path string) error {
	// Best-effort: try a normal unmount first.
	if err := syscall.Unmount(path, 0); err == nil {
		return nil
	}

	// Fallback: call platform helper (more reliable for FUSE).
	switch runtime.GOOS {
	case "linux":
		if p, err := exec.LookPath("fusermount3"); err == nil {
			if out, err := exec.Command(p, "-u", "-z", path).CombinedOutput(); err == nil {
				_ = out
				return nil
			}
			return fmt.Errorf("fusermount3 -u -z failed: %w", err)
		}
		if p, err := exec.LookPath("fusermount"); err == nil {
			if out, err := exec.Command(p, "-u", "-z", path).CombinedOutput(); err == nil {
				_ = out
				return nil
			}
			return fmt.Errorf("fusermount -u -z failed: %w", err)
		}
	case "darwin":
		if p, err := exec.LookPath("umount"); err == nil {
			if out, err := exec.Command(p, "-f", path).CombinedOutput(); err == nil {
				_ = out
				return nil
			}
			return fmt.Errorf("umount -f failed: %w", err)
		}
	}

	return fmt.Errorf("unmount failed (no helper available): %s", path)
}

func staleUnmountHelp(path string) string {
	switch runtime.GOOS {
	case "linux":
		return fmt.Sprintf("Try:\n  fusermount3 -u -z %s\n  # or\n  fusermount -u -z %s", path, path)
	case "darwin":
		return fmt.Sprintf("Try:\n  umount -f %s", path)
	default:
		return fmt.Sprintf("Try unmounting the path: %s", path)
	}
}
