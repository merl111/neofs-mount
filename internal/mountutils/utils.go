package mountutils

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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

	logFile := LogFilePath()
	var w io.Writer = os.Stderr
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err == nil {
		w = io.MultiWriter(f, &bestEffortWriter{os.Stderr})
	}

	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

// bestEffortWriter wraps an io.Writer and swallows errors so that
// io.MultiWriter does not short-circuit when the underlying writer
// is unavailable (e.g. os.Stderr in a Windows GUI-subsystem app).
type bestEffortWriter struct{ w io.Writer }

func (b *bestEffortWriter) Write(p []byte) (int, error) {
	n, _ := b.w.Write(p)
	return n, nil
}

// LogFilePath returns the OS-specific path for the neofs-mount log file.
func LogFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	var dir string
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(home, "Library", "Logs", "neofs-mount")
	} else {
		dir = filepath.Join(home, ".local", "state", "neofs-mount")
	}
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "neofs-mount.log")
}

// OpenLogDirectory opens the directory containing the log file in the OS file manager.
// Platform-specific implementations are in utils_windows.go and utils_unix.go.
func OpenLogDirectory() error {
	return openLogDirectory()
}

func EnsureDir(path string, perm os.FileMode) error {
	st, err := os.Stat(path)
	if err == nil {
		if st.IsDir() {
			return nil
		}
		return fmt.Errorf("path exists but is not a directory: %s", path)
	}
	// If a previous FUSE mount crashed, Linux/macOS can return ENOTCONN here.
	// Try to unmount and re-check (no-op on Windows).
	if isNotConn(err) {
		unmErr := tryUnmount(path)
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


