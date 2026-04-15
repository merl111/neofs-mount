//go:build linux || darwin || windows

package fs

import (
	"log/slog"
	"time"

	"github.com/mathias/neofs-mount/internal/uploads"
)

// MountParams is shared by Linux (FUSE), macOS (File Provider host coordination), and Windows (CfAPI).
type MountParams struct {
	Logger *slog.Logger

	Endpoint  string
	WalletKey string

	Mountpoint             string
	ReadOnly               bool
	TraceReads             bool
	StreamLookaheadWindows int

	CacheDir  string
	CacheSize int64

	IgnoreContainerIDs []string
	UploadTracker      *uploads.Tracker // optional; enables live upload tracking
	UploadHistory      *uploads.History

	// AuditLogPath is the append-only JSONL audit file; empty disables.
	AuditLogPath string

	// FetchDirCacheTTL is how long Windows CfAPI directory listings are reused before
	// hitting NeoFS again (Windows only). Zero means 5 seconds.
	FetchDirCacheTTL time.Duration

	// HydrationCacheMaxObjectBytes is the max object size fully written into the disk
	// cache on first Windows FetchData; larger objects use ranged reads only (Windows only).
	// Zero means 64 MiB.
	HydrationCacheMaxObjectBytes int64
}
