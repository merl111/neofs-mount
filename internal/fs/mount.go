//go:build linux || darwin

package fs

import (
	"context"
	"log/slog"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/mathias/neofs-mount/internal/cache"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/mathias/neofs-mount/internal/uploads"
)

type MountParams struct {
	Logger *slog.Logger

	Endpoint  string
	WalletKey string

	Mountpoint string
	ReadOnly   bool

	CacheDir  string
	CacheSize int64

	IgnoreContainerIDs []string
	UploadTracker      *uploads.Tracker // optional; enables live upload tracking
}

type MountedFS struct {
	log    *slog.Logger
	server *fuse.Server
	neo    *neofs.Client
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

	root := &rootNode{
		log:               log,
		neo:               neo,
		cache:             cch,
		dirCache:          newDirCache(5 * time.Minute),
		ro:                p.ReadOnly,
		ignoreContainers:  makeIgnoreSet(p.IgnoreContainerIDs),
		uploadTracker:     p.UploadTracker,
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

	server, err := fs.Mount(p.Mountpoint, root, opts)
	if err != nil {
		_ = neo.Close()
		return nil, err
	}

	return &MountedFS{log: log, server: server, neo: neo}, nil
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
	return nil
}

