package mountutils

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func NewLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

func EnsureDir(path string, perm os.FileMode) error {
	st, err := os.Stat(path)
	if err == nil {
		if st.IsDir() {
			return nil
		}
		return fmt.Errorf("path exists but is not a directory: %s", path)
	}
	// If a previous FUSE mount crashed, Linux can return ENOTCONN here.
	// Try to unmount and re-check.
	if IsNotConn(err) {
		unmErr := TryUnmount(path)
		if st2, err2 := os.Stat(path); err2 == nil && st2.IsDir() {
			return nil
		}
		// If it still fails, surface a helpful error.
		help := staleUnmountHelp(path)
		if unmErr != nil {
			return fmt.Errorf("mountpoint is in a stale FUSE state: %s (auto-unmount failed: %v)\n%s", path, unmErr, help)
		}
		return fmt.Errorf("mountpoint is in a stale FUSE state: %s\n%s", path, help)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat: %w", err)
	}
	if err := os.MkdirAll(path, perm); err != nil {
		// In case of a race or strange filesystem behavior, re-check.
		if st2, err2 := os.Stat(path); err2 == nil && st2.IsDir() {
			return nil
		}
		return fmt.Errorf("mkdir: %w", err)
	}
	return nil
}

func IsNotConn(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return errors.Is(pe.Err, syscall.ENOTCONN)
	}
	return errors.Is(err, syscall.ENOTCONN)
}

func TryUnmount(path string) error {
	// Best-effort: try a normal unmount first.
	if err := syscall.Unmount(path, 0); err == nil {
		return nil
	}
	// If that fails, try a forced unmount (helps with dead FUSE daemons).
	if err := syscall.Unmount(path, syscall.MNT_FORCE); err == nil {
		return nil
	}

	// Fallback: call platform helper (more reliable for FUSE).
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
		return fmt.Sprintf("Try:\n  umount -f %s", path)
	default:
		return fmt.Sprintf("Try unmounting the path: %s", path)
	}
}
