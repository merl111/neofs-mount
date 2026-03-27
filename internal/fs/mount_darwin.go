//go:build darwin

package fs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/mathias/neofs-mount/internal/audit"
)

// FileProviderAppBundleID is the macOS host app that owns the File Provider extension.
// Override with env NEOFS_FP_BUNDLE_ID for local development.
const FileProviderAppBundleID = "org.neofs.mount"

// MountedFS on macOS does not hold a FUSE server; Mount launches the native app which
// hosts the File Provider extension and Go static library.
type MountedFS struct {
	log   *slog.Logger
	audit *audit.Log
}

// Mount opens the neoFS Mount macOS application (File Provider). NeoFS I/O runs inside
// the app extension + linked Go archive, not in this process.
func Mount(p MountParams) (*MountedFS, error) {
	log := p.Logger
	if log == nil {
		log = slog.Default()
	}

	auditLog, aerr := audit.Open(p.AuditLogPath)
	if aerr != nil {
		log.Warn("audit log disabled", "path", p.AuditLogPath, "err", aerr)
		auditLog = nil
	}
	if auditLog != nil {
		auditLog.Record("session_mount", map[string]any{
			"mountpoint": p.Mountpoint,
			"backend":    "fileprovider",
			"note":       "Launched native macOS app; containers appear under the NeoFS location in Finder (File Provider), not as a traditional POSIX mount. Configure endpoint and wallet in the tray app Settings — the native app reads the same config path.",
		})
	}

	bundleID := os.Getenv("NEOFS_FP_BUNDLE_ID")
	if bundleID == "" {
		bundleID = FileProviderAppBundleID
	}

	// -g brings the app forward; -n opens a new instance if already running (config reload).
	cmd := exec.Command("open", "-gn", "-b", bundleID)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if auditLog != nil {
			_ = auditLog.Close()
		}
		return nil, fmt.Errorf("macOS File Provider: could not open app %q (install the neoFS Mount .app from macos/NeoFSMount build or set NEOFS_FP_BUNDLE_ID): %w", bundleID, err)
	}

	return &MountedFS{log: log, audit: auditLog}, nil
}

// Unmount is a no-op for the File Provider path: disconnect from Finder via the host app
// or System Settings. Legacy FUSE umount is not used.
func (m *MountedFS) Unmount() error {
	return nil
}

// Shutdown closes local resources (audit). It does not stop the native app.
func (m *MountedFS) Shutdown(_ context.Context) error {
	if m == nil {
		return nil
	}
	if m.audit != nil {
		m.audit.Record("session_unmount", map[string]any{"backend": "fileprovider"})
		_ = m.audit.Close()
		m.audit = nil
	}
	return nil
}
