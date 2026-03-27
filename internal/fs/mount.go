//go:build linux

package fs

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/mathias/neofs-mount/internal/audit"
	"github.com/mathias/neofs-mount/internal/cache"
	"github.com/mathias/neofs-mount/internal/neofs"
)

type MountedFS struct {
	log    *slog.Logger
	server *fuse.Server
	neo    *neofs.Client
	audit  *audit.Log
}

func Mount(p MountParams) (*MountedFS, error) {
	log := p.Logger
	if log == nil {
		log = slog.Default()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	neo, err := neofs.New(ctx, neofs.Params{
		Logger:    log,
		Endpoint:  p.Endpoint,
		WalletKey: p.WalletKey,
	})
	if err != nil {
		return nil, err
	}

	cch, err := cache.New(p.CacheDir, p.CacheSize)
	if err != nil {
		_ = neo.Close()
		return nil, err
	}

	auditLog, aerr := audit.Open(p.AuditLogPath)
	if aerr != nil {
		log.Warn("audit log disabled", "path", p.AuditLogPath, "err", aerr)
		auditLog = nil
	}
	if auditLog != nil {
		auditLog.Record("session_mount", map[string]any{
			"mountpoint": p.Mountpoint,
			"backend":    "fuse",
			"note":       "Moving files out of the mount does not remove NeoFS objects; they remain until replaced by upload or explicitly deleted (unlink).",
		})
	}

	root := &rootNode{
		log:               log,
		neo:               neo,
		cache:             cch,
		dirCache:          newDirCache(5 * time.Minute),
		ro:                p.ReadOnly,
		ignoreContainers:  makeIgnoreSet(p.IgnoreContainerIDs),
		uploadTracker:     p.UploadTracker,
		uploadHistory:     p.UploadHistory,
		audit:             auditLog,
	}

	// Use long kernel-level TTLs: NeoFS objects are immutable, so stale dentry/attr
	// entries just mean slightly old data — identical to how goofys handles S3.
	// Short TTLs (5s) cause the kernel to re-ask FUSE on every shell keystroke (tab
	// completion), which floods the log and generates unnecessary scan lookups.
	entryTTL := 5 * time.Minute
	attrTTL := 5 * time.Minute
	opts := &fs.Options{
		EntryTimeout: &entryTTL,
		AttrTimeout:  &attrTTL,
		MountOptions: fuse.MountOptions{
			FsName:        "neofs",
			Name:          "neofs",
			DisableXAttrs: false,
		},
	}
	// On Linux, try syscall.Mount on /dev/fuse first; if that works we avoid fusermount3.
	// When fusermount exits with status 1 (often reported as “code 256”), direct mount frequently still succeeds.
	if runtime.GOOS == "linux" {
		opts.MountOptions.DirectMount = true
	}

	server, err := fs.Mount(p.Mountpoint, root, opts)
	if err != nil {
		_ = neo.Close()
		if auditLog != nil {
			_ = auditLog.Close()
		}
		err = wrapFuseMountError(p.Mountpoint, err)
		return nil, err
	}

	return &MountedFS{log: log, server: server, neo: neo, audit: auditLog}, nil
}

func wrapFuseMountError(mountpoint string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, "fusermount") {
		return err
	}
	return fmt.Errorf("%w\n\n"+
		"Fusermount failed (exit status 1 is often shown as “256”). Common fixes:\n"+
		"  • Lazy-unmount a stuck FUSE mount, then retry:\n"+
		"      fusermount3 -u -z %q\n"+
		"    (or: fusermount -u -z … on older systems)\n"+
		"  • Ensure the mountpoint directory exists, is empty, and is not already a mount.\n"+
		"  • Check /etc/fuse.conf (user_allow_other, mount_max) and that fuse3 is installed.\n"+
		"  • This build tries kernel FUSE mount first on Linux; if you still see this, paste the full error.",
		err, mountpoint)
}

func (m *MountedFS) Unmount() error {
	if m == nil || m.server == nil {
		return nil
	}
	return m.server.Unmount()
}

func (m *MountedFS) Shutdown(_ context.Context) error {
	if m == nil {
		return nil
	}
	// Unmount triggers go-fuse shutdown. Close NeoFS client afterward.
	if m.server != nil {
		_ = m.server.Unmount()
	}
	if m.neo != nil {
		_ = m.neo.Close()
	}
	if m.audit != nil {
		m.audit.Record("session_unmount", map[string]any{"backend": "fuse"})
		_ = m.audit.Close()
		m.audit = nil
	}
	return nil
}

