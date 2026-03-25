//go:build windows

package fs

// Windows mount adapter — delegates to internal/cfapi instead of go-fuse.
// The public API (MountParams, MountedFS, Mount, Unmount, Shutdown) is
// identical to the Linux/macOS version in mount.go so the tray app compiles
// unchanged on all platforms.

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/mathias/neofs-mount/internal/cache"
	"github.com/mathias/neofs-mount/internal/cfapi"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/mathias/neofs-mount/internal/uploads"
)

// MountParams mirrors the Linux/macOS definition exactly.
type MountParams struct {
	Logger *slog.Logger

	Endpoint  string
	WalletKey string

	Mountpoint string
	ReadOnly   bool

	CacheDir  string
	CacheSize int64

	IgnoreContainerIDs []string
	UploadTracker      *uploads.Tracker
}

// MountedFS mirrors the Linux/macOS definition.
type MountedFS struct {
	log     *slog.Logger
	session *cfapi.Session
	prov    *neofsProvider
	neo     *neofs.Client
}

// Mount registers the sync root and connects the Windows CfApi callbacks.
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

	prov := &neofsProvider{
		log:              log,
		neo:              neo,
		cache:            cch,
		ro:               p.ReadOnly,
		ignoreContainers: makeIgnoreSet(p.IgnoreContainerIDs),
		uploadTracker:    p.UploadTracker,
		root:             p.Mountpoint,
	}

	// Register as a sync provider (idempotent; safe to call on every launch).
	if err := cfapi.RegisterSyncRoot(p.Mountpoint, "neoFS Mount", "1.0"); err != nil {
		log.Warn("cfapi RegisterSyncRoot (may already be registered)", "err", err)
	}

	session, err := cfapi.Connect(p.Mountpoint, prov, log)
	if err != nil {
		_ = neo.Close()
		return nil, err
	}

	return &MountedFS{
		log:     log,
		session: session,
		prov:    prov,
		neo:     neo,
	}, nil
}

// Unmount disconnects the CfApi session.
func (m *MountedFS) Unmount() error {
	if m == nil || m.session == nil {
		return nil
	}
	return m.session.Disconnect()
}

// Shutdown cleans up all resources.
func (m *MountedFS) Shutdown(_ context.Context) error {
	if m == nil {
		return nil
	}
	if m.session != nil {
		_ = m.session.Disconnect()
	}
	if m.neo != nil {
		_ = m.neo.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// makeIgnoreSet — shared helper (duplicated for the windows build unit since
// tree.go is excluded by build tags on Windows)
// ---------------------------------------------------------------------------
func makeIgnoreSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	s := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

// ---------------------------------------------------------------------------
// neofsProvider implements cfapi.Provider
// ---------------------------------------------------------------------------

type neofsProvider struct {
	log              *slog.Logger
	neo              *neofs.Client
	cache            *cache.Cache
	ro               bool
	ignoreContainers map[string]struct{}
	uploadTracker    *uploads.Tracker
	root             string // sync root path on disk
}

// FetchPlaceholders is called when Windows opens a directory under the sync root.
// We translate the path back to a NeoFS container / prefix and enumerate objects.
func (p *neofsProvider) FetchPlaceholders(req cfapi.FetchPlaceholdersRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// NormalizedPath is relative to the sync root, e.g. "\testc" or "\" for root.
	rel := filepath.Clean(req.NormalizedPath)
	if rel == "." {
		rel = ""
	}

	p.log.Debug("cfapi: FetchPlaceholders", "path", rel)

	if rel == "" {
		// Root directory → enumerate containers.
		return p.fetchContainers(ctx, req)
	}
	// Sub-directory → enumerate objects with this prefix.
	return p.fetchObjects(ctx, req, rel)
}

func (p *neofsProvider) fetchContainers(ctx context.Context, req cfapi.FetchPlaceholdersRequest) error {
	containers, err := p.neo.ListContainers(ctx)
	if err != nil {
		return err
	}

	children := make([]cfapi.Placeholder, 0, len(containers))
	for _, cid := range containers {
		if _, ignored := p.ignoreContainers[cid]; ignored {
			continue
		}
		children = append(children, cfapi.Placeholder{
			Name:        cid,
			IsDirectory: true,
			FileIdentity: cfapi.IdentityFromString("container:" + cid),
		})
	}
	return p.findSession(req.ConnectionKey).CreatePlaceholders(p.root, children)
}

func (p *neofsProvider) fetchObjects(ctx context.Context, req cfapi.FetchPlaceholdersRequest, rel string) error {
	// First segment of rel is the container ID.
	parts := splitPath(rel)
	if len(parts) == 0 {
		return nil
	}

	containerID := parts[0]
	prefix := ""
	if len(parts) > 1 {
		prefix = filepath.Join(parts[1:]...) + "/"
	}

	entries, err := p.neo.ListEntriesByPrefix(ctx, containerID, prefix)
	if err != nil {
		return err
	}

	dirPath := filepath.Join(p.root, rel)
	children := make([]cfapi.Placeholder, 0, len(entries))
	for _, e := range entries {
		isDir := e.IsDirectory
		ident := cfapi.IdentityFromString(containerID + ":" + e.ObjectID)
		children = append(children, cfapi.Placeholder{
			Name:         e.Name,
			IsDirectory:  isDir,
			Size:         e.Size,
			FileIdentity: ident,
		})
	}
	return p.findSession(req.ConnectionKey).CreatePlaceholders(dirPath, children)
}

// FetchData is called when Windows reads bytes from a placeholder file.
func (p *neofsProvider) FetchData(req cfapi.FetchDataRequest, sess *cfapi.TransferSession) error {
	// Identity format: "containerID:objectID"
	identity := string(req.FileIdentity)
	p.log.Debug("cfapi: FetchData", "path", req.NormalizedPath, "id", identity,
		"offset", req.RequiredOffset, "length", req.RequiredLength)

	containerID, objectID, err := parseIdentity(identity)
	if err != nil {
		sess.Fail(-0x3FFFFFBF)
		return err
	}

	ctx := context.Background()
	const chunkSize = 4 << 20 // 4 MB chunks
	end := req.RequiredOffset + req.RequiredLength
	offset := req.RequiredOffset

	for offset < end {
		readLen := int64(chunkSize)
		if offset+readLen > end {
			readLen = end - offset
		}

		buf, err := p.neo.ReadObjectRange(ctx, containerID, objectID, offset, readLen)
		if err != nil {
			sess.Fail(-0x3FFFFFBF)
			return err
		}

		if err := sess.Write(buf, offset); err != nil {
			return err
		}
		offset += int64(len(buf))
	}
	return nil
}

func (p *neofsProvider) findSession(_ int64) *cfapi.Session { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func splitPath(rel string) []string {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "/" || clean == "." || clean == "" {
		return nil
	}
	var parts []string
	cur := clean
	for cur != "" && cur != "." && cur != "/" {
		dir, base := filepath.Split(cur)
		if base != "" {
			parts = append([]string{base}, parts...)
		}
		cur = filepath.Clean(dir)
	}
	return parts
}

func parseIdentity(id string) (string, string, error) {
	for i, c := range id {
		if c == ':' {
			return id[:i], id[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("cfapi: invalid file identity %q", id)
}

