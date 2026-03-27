//go:build windows

package fs

// Windows mount adapter â€” delegates to internal/cfapi instead of go-fuse.
// The public API (MountParams, MountedFS, Mount, Unmount, Shutdown) is
// identical to the Linux/macOS version in mount.go so the tray app compiles
// unchanged on all platforms.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mathias/neofs-mount/internal/audit"
	"github.com/mathias/neofs-mount/internal/cache"
	"github.com/mathias/neofs-mount/internal/cfapi"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/mathias/neofs-mount/internal/uploads"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
	windows "golang.org/x/sys/windows"
)

// errUploadClosedSkipped means we intentionally did not ObjectPut (dedupe uses nil, not this).
var errUploadClosedSkipped = errors.New("cfapi: upload closed skipped")

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
	UploadHistory      *uploads.History

	// AuditLogPath is the append-only JSONL audit file; empty disables.
	AuditLogPath string

	// FetchDirCacheTTL is how long directory placeholder listings are reused before
	// re-querying NeoFS. Zero means 5 seconds.
	FetchDirCacheTTL time.Duration

	// HydrationCacheMaxObjectBytes caps full-object downloads into the disk cache for
	// FetchData; larger objects use ranged network reads only. Zero means 64 MiB.
	HydrationCacheMaxObjectBytes int64
}

// MountedFS mirrors the Linux/macOS definition.
type MountedFS struct {
	log     *slog.Logger
	session *cfapi.Session
	prov    *neofsProvider
	neo     *neofs.Client
	audit   *audit.Log
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

	auditLog, aerr := audit.Open(p.AuditLogPath)
	if aerr != nil {
		log.Warn("audit log disabled", "path", p.AuditLogPath, "err", aerr)
		auditLog = nil
	}
	if auditLog != nil {
		auditLog.Record("session_mount", map[string]any{
			"mountpoint": p.Mountpoint,
			"backend":    "cfapi",
			"note":       "Moving a file out does not delete NeoFS objects; if an object still exists at that path, a cloud placeholder is recreated so Explorer keeps showing it.",
		})
	}

	fetchTTL := p.FetchDirCacheTTL
	if fetchTTL <= 0 {
		fetchTTL = defaultFetchDirCacheTTL
	}
	hydrMax := p.HydrationCacheMaxObjectBytes
	if hydrMax <= 0 {
		hydrMax = defaultHydrationCacheMaxObject
	}
	prov := &neofsProvider{
		log:                          log,
		neo:                          neo,
		cache:                        cch,
		ro:                           p.ReadOnly,
		ignoreContainers:             makeIgnoreSet(p.IgnoreContainerIDs),
		uploadTracker:                p.UploadTracker,
		uploadHistory:                p.UploadHistory,
		root:                         p.Mountpoint,
		audit:                        auditLog,
		fetchDirCacheTTL:             fetchTTL,
		hydrationCacheMaxObjectBytes: hydrMax,
	}

	// Register as a sync provider (idempotent; safe to call on every launch).
	if err := cfapi.RegisterSyncRoot(p.Mountpoint, "neoFS Mount", "1.0"); err != nil {
		log.Warn("cfapi RegisterSyncRoot (may already be registered)", "err", err)
	}

	session, err := cfapi.Connect(p.Mountpoint, prov, log)
	if err != nil {
		_ = neo.Close()
		if auditLog != nil {
			_ = auditLog.Close()
		}
		return nil, err
	}
	prov.session = session
	prov.startUploadWatcher()

	return &MountedFS{
		log:     log,
		session: session,
		prov:    prov,
		neo:     neo,
		audit:   auditLog,
	}, nil
}

// Unmount disconnects the CfApi session.
func (m *MountedFS) Unmount() error {
	if m == nil || m.session == nil {
		return nil
	}
	if m.prov != nil {
		m.prov.stopUploadWatcher()
	}
	return m.session.Disconnect()
}

// Shutdown cleans up all resources.
func (m *MountedFS) Shutdown(_ context.Context) error {
	if m == nil {
		return nil
	}
	if m.prov != nil {
		m.prov.stopUploadWatcher()
	}
	if m.session != nil {
		_ = m.session.Disconnect()
	}
	if m.neo != nil {
		_ = m.neo.Close()
	}
	if m.audit != nil {
		m.audit.Record("session_unmount", map[string]any{"backend": "cfapi"})
		_ = m.audit.Close()
		m.audit = nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// makeIgnoreSet â€” shared helper (duplicated for the windows build unit since
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
	uploadHistory    *uploads.History
	root             string         // sync root path on disk
	session          *cfapi.Session // set after Connect(); used by placeholder callbacks

	uploadWatchStop chan struct{}
	uploadWatchWG   sync.WaitGroup

	// Per NeoFS object key (containerID/neoRelPath) — serializes Put/Delete to avoid races
	// when the watcher and CfApi callbacks overlap (e.g. rapid moves).
	uploadMu    sync.Mutex
	uploadLocks map[string]*sync.Mutex

	// After a successful put, suppress duplicate uploads for the same key when CfApi and the
	// directory watcher both fire (e.g. moving a folder into the sync root).
	uploadDedupeMu sync.Mutex
	uploadDedupe   map[string]uploadDedupeEntry

	audit *audit.Log

	// containerUI maps Explorer folder name → container ID string (refreshed when listing containers).
	aliasMu     sync.RWMutex
	containerUI map[string]string

	// Cached container list to avoid hammering the network on every FetchPlaceholders.
	cnrCacheMu   sync.Mutex
	cnrCacheAt   time.Time
	cnrCacheList []ContainerUIEntry
	cnrCacheMap  map[string]string

	// Paths recently deleted via CfApi (NotifyFileClose Deleted=true).
	// The watcher checks this to avoid restoring placeholders we just deleted.
	recentDeletesMu sync.Mutex
	recentDeletes   map[string]time.Time

	// Per-directory FetchPlaceholders serialization: prevents CfApi from
	// flooding us with 100+ concurrent callbacks for the same directory
	// (thundering herd when the first callback is slow on network I/O).
	fetchDirMu    sync.Mutex
	fetchDirLocks map[string]*fetchDirEntry

	// One leader FetchPlaceholders per (ConnectionKey, TransferKey, NormalizedPath); duplicate
	// callbacks get TransferFetchPlaceholdersEmpty. Path is in the key so unrelated fetches
	// never share a wait group if Windows reuses transfer keys. Never hold a mutex across
	// CreatePlaceholders/CfExecute — CfApi can re-enter FetchPlaceholders for the same dir
	// and would deadlock (Explorer “Calculating…” forever).
	fetchPHByTransfer sync.Map // string -> *fetchPHWait

	fetchDirCacheTTL             time.Duration
	hydrationCacheMaxObjectBytes int64
}

const (
	defaultFetchDirCacheTTL        = 5 * time.Second
	defaultHydrationCacheMaxObject = 64 << 20 // align with FUSE stream threshold in tree.go
)

// fetchPHWait groups callbacks that share the same CfApi transfer operation.
type fetchPHWait struct {
	done chan struct{}
	err  error
}

type fetchDirEntry struct {
	mu       sync.Mutex // NeoFS fetch + listing cache
	children []cfapi.Placeholder
	err      error
	at       time.Time
}

// uploadRapidDedupeWindow collapses nearly simultaneous duplicate uploads (CfApi close + directory
// watcher). Do not use ModTime or a long TTL: cloud placeholders often keep the same mtime after
// save, so "same size + same mtime within 12s" falsely skipped real edits.
const uploadRapidDedupeWindow = 900 * time.Millisecond

const uploadDedupeMapTTL = 45 * time.Second // drop map entries after no activity

type uploadDedupeEntry struct {
	size       int64
	recordedAt time.Time
}

// stripDriveLetter returns the path without a leading "C:" (or "C:\") so we can compare
// NormalizedPath (often "C:\mount\…") with the sync root consistently.
func stripDriveLetter(p string) string {
	p = filepath.Clean(p)
	if strings.HasPrefix(strings.ToLower(p), `\\?\`) {
		p = p[4:]
		p = filepath.Clean(p)
	}
	if len(p) >= 2 && p[1] == ':' {
		return p[2:]
	}
	return p
}

// longWindowsPath expands 8.3 components so mountDiskToNeo matches ListContainersForUI paths.
func longWindowsPath(abs string) string {
	abs = filepath.Clean(abs)
	if abs == "" {
		return abs
	}
	p0, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return abs
	}
	size := uint32(len(abs) + 64)
	for range 8 {
		buf := make([]uint16, size)
		n, err := windows.GetLongPathName(p0, &buf[0], size)
		if err != nil || n == 0 {
			return abs
		}
		if n < size {
			return windows.UTF16ToString(buf[:n])
		}
		size = n + 1
	}
	return abs
}

// normalizedToRel maps CfApi NormalizedPath to a path relative to the sync root.
func (p *neofsProvider) normalizedToRel(normalizedPath string) string {
	absPath := filepath.Clean(normalizedPath)
	rootStrip := stripDriveLetter(p.root)
	pathStrip := stripDriveLetter(absPath)
	var rel string
	if strings.HasPrefix(strings.ToLower(pathStrip), strings.ToLower(rootStrip)) {
		rel = pathStrip[len(rootStrip):]
	} else {
		// Fallback: some callers pass paths already relative to volume root.
		rel = pathStrip
	}
	return strings.TrimLeft(rel, `\/`)
}

func (p *neofsProvider) mountDiskToNeo(absPath string) (cidStr, neoRel string, ok bool) {
	p.aliasMu.RLock()
	m := p.containerUI
	p.aliasMu.RUnlock()
	return MountDiskToNeoUploadWithUI(p.root, absPath, m)
}

func (p *neofsProvider) resolveSeg(seg string) (cidStr string, ok bool) {
	p.aliasMu.RLock()
	m := p.containerUI
	p.aliasMu.RUnlock()
	return ResolveContainerSegment(seg, m)
}

// FetchPlaceholders is called when Windows opens a directory under the sync root.
// We translate the path back to a NeoFS container / prefix and enumerate objects.
// Every code path must complete the CfApi placeholder transfer (via completeFetchPlaceholders
// or root CreatePlaceholders); returning nil without doing so breaks Explorer ("cloud operation is invalid").
func (p *neofsProvider) FetchPlaceholders(req cfapi.FetchPlaceholdersRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rel := p.normalizedToRel(req.NormalizedPath)
	p.log.Debug("cfapi: FetchPlaceholders", "normalized", req.NormalizedPath, "rel", rel)

	pathKey := strings.ToLower(filepath.Clean(req.NormalizedPath))
	if pathKey == "." {
		pathKey = ""
	}
	if pathKey == "" {
		if rel == "" {
			pathKey = "<syncroot>"
		} else {
			pathKey = "rel:" + strings.ToLower(rel)
		}
	}
	waitKey := fmt.Sprintf("%d:%d", req.ConnectionKey, req.TransferKey) + "\x00" + pathKey
	wait := &fetchPHWait{done: make(chan struct{})}
	if v, loaded := p.fetchPHByTransfer.LoadOrStore(waitKey, wait); loaded {
		wait = v.(*fetchPHWait)
		<-wait.done
		if wait.err != nil {
			return wait.err
		}
		sess := p.findSession(req.ConnectionKey)
		if sess == nil {
			return fmt.Errorf("cfapi: no session")
		}
		return sess.TransferFetchPlaceholdersEmpty(req.ConnectionKey, req.TransferKey)
	}
	defer close(wait.done)

	if rel == "" {
		children, err := p.listContainerPlaceholders(ctx)
		if err != nil {
			wait.err = err
			return err
		}
		sess := p.findSession(req.ConnectionKey)
		if sess == nil {
			wait.err = fmt.Errorf("cfapi: no session")
			return wait.err
		}
		p.log.Debug("cfapi: fetchContainers: creating placeholders", "count", len(children), "root", p.root)
		err = sess.CreatePlaceholders(req.ConnectionKey, req.TransferKey, req.NormalizedPath, children, true)
		p.log.Debug("cfapi: fetchContainers: CreatePlaceholders done", "err", err)
		if err != nil {
			wait.err = err
		}
		return err
	}

	children, err := p.fetchObjectsCached(ctx, rel)
	if err != nil {
		wait.err = err
		return err
	}

	err2 := p.completeFetchPlaceholders(req, rel, children)
	if err2 != nil {
		wait.err = err2
	}
	return err2
}

func (p *neofsProvider) listContainerPlaceholders(ctx context.Context) ([]cfapi.Placeholder, error) {
	entries, alias, err := p.cachedContainerList(ctx)
	if err != nil {
		return nil, err
	}
	p.aliasMu.Lock()
	p.containerUI = alias
	p.aliasMu.Unlock()

	children := make([]cfapi.Placeholder, 0, len(entries))
	for _, e := range entries {
		children = append(children, cfapi.Placeholder{
			Name:         e.DisplayName,
			IsDirectory:  true,
			FileIdentity: cfapi.IdentityFromString(e.CIDStr + ":"),
		})
	}
	return children, nil
}

const cnrCacheTTL = 30 * time.Second

// cachedContainerList returns the container list, refreshing from the network
// at most once per cnrCacheTTL. Concurrent callers share a single fetch.
func (p *neofsProvider) cachedContainerList(ctx context.Context) ([]ContainerUIEntry, map[string]string, error) {
	p.cnrCacheMu.Lock()
	defer p.cnrCacheMu.Unlock()

	if p.cnrCacheList != nil && time.Since(p.cnrCacheAt) < cnrCacheTTL {
		return p.cnrCacheList, p.cnrCacheMap, nil
	}

	entries, alias, err := ListContainersForUI(ctx, p.neo, p.ignoreContainers)
	if err != nil {
		p.log.Error("cfapi: fetchContainers: ListContainersForUI failed", "err", err)
		if p.cnrCacheList != nil {
			return p.cnrCacheList, p.cnrCacheMap, nil
		}
		return nil, nil, err
	}

	p.cnrCacheAt = time.Now()
	p.cnrCacheList = entries
	p.cnrCacheMap = alias
	return entries, alias, nil
}

// placeholderDirForFetch is the directory Windows asked to populate (authoritative when set).
func (p *neofsProvider) placeholderDirForFetch(req cfapi.FetchPlaceholdersRequest, rel string) string {
	if req.NormalizedPath != "" {
		return filepath.Clean(req.NormalizedPath)
	}
	return filepath.Join(p.root, rel)
}

// completeFetchPlaceholders must run for every FetchPlaceholders callback — otherwise CfExecute
// never completes and Explorer breaks with "The cloud operation is invalid."
func (p *neofsProvider) completeFetchPlaceholders(req cfapi.FetchPlaceholdersRequest, rel string, children []cfapi.Placeholder) error {
	sess := p.findSession(req.ConnectionKey)
	if sess == nil {
		return fmt.Errorf("cfapi: no session")
	}
	dir := p.placeholderDirForFetch(req, rel)
	// disableOnDemand must be false for container/object directories so Explorer can
	// paste and create new files (log: empty vault dir + disableFlag=1 broke paste).
	// Root container list (fetchContainers) still uses true — that tree is listing-only.
	return sess.CreatePlaceholders(req.ConnectionKey, req.TransferKey, dir, children, false)
}

// objectPrefix returns the NeoFS prefix for the given relative path within
// a container (empty string for the container root).
func (p *neofsProvider) objectPrefix(rel string) string {
	parts := splitPath(rel)
	if len(parts) <= 1 {
		return ""
	}
	return filepath.Join(parts[1:]...) + "/"
}

// fetchObjectsCached serializes the NeoFS listing fetch per directory and caches briefly.
func (p *neofsProvider) fetchObjectsCached(ctx context.Context, rel string) ([]cfapi.Placeholder, error) {
	dirKey := strings.ToLower(rel)

	p.fetchDirMu.Lock()
	if p.fetchDirLocks == nil {
		p.fetchDirLocks = make(map[string]*fetchDirEntry)
	}
	entry, exists := p.fetchDirLocks[dirKey]
	if !exists {
		entry = &fetchDirEntry{}
		p.fetchDirLocks[dirKey] = entry
	}
	p.fetchDirMu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if exists && !entry.at.IsZero() && time.Since(entry.at) < p.fetchDirCacheTTL {
		p.log.Debug("cfapi: fetchObjectsCached: using cached result", "rel", rel)
		return entry.children, entry.err
	}

	children, err := p.fetchObjectsFromNeoFS(ctx, rel)
	entry.children = children
	entry.err = err
	entry.at = time.Now()

	if err == nil {
		go p.cleanupStalePlaceholders(splitPath(rel)[0], p.objectPrefix(rel), children)
	}

	return children, err
}

// fetchObjectsFromNeoFS does the actual NeoFS query for a directory listing.
func (p *neofsProvider) fetchObjectsFromNeoFS(ctx context.Context, rel string) ([]cfapi.Placeholder, error) {
	parts := splitPath(rel)
	if len(parts) == 0 {
		return nil, nil
	}
	cidStr, ok := p.resolveSeg(parts[0])
	if !ok {
		// containerUI may not be populated yet (user navigated to a container
		// directory before the root was enumerated). Force-load the container
		// list so resolveSeg can succeed.
		_, alias, err := p.cachedContainerList(ctx)
		if err == nil && alias != nil {
			p.aliasMu.Lock()
			p.containerUI = alias
			p.aliasMu.Unlock()
			cidStr, ok = p.resolveSeg(parts[0])
		}
		if !ok {
			p.log.Debug("cfapi: fetchObjects: unknown container folder (empty listing)", "segment", parts[0])
			return nil, nil
		}
	}
	prefix := ""
	if len(parts) > 1 {
		prefix = filepath.Join(parts[1:]...) + "/"
	}

	entries, err := p.neo.ListEntriesByPrefix(ctx, cidStr, prefix)
	if err != nil {
		return nil, err
	}

	children := make([]cfapi.Placeholder, 0, len(entries))
	for _, e := range entries {
		childRel := prefix + e.Name
		childDisk := filepath.Join(append([]string{p.root, parts[0]}, splitPath(childRel)...)...)
		if p.wasRecentlyDeleted(childDisk) {
			continue
		}

		isDir := e.IsDirectory
		var ident []byte
		if isDir {
			relWithin := strings.TrimSuffix(prefix, "/")
			if relWithin != "" {
				relWithin += "/"
			}
			relWithin += e.Name
			relWithin = filepath.ToSlash(relWithin)
			ident = cfapi.IdentityFromString(fmt.Sprintf("%s:dir:%s", cidStr, relWithin))
		} else {
			ident = cfapi.IdentityFromString(cidStr + ":" + e.ObjectID)
		}
		children = append(children, cfapi.Placeholder{
			Name:         e.Name,
			IsDirectory:  isDir,
			Size:         e.Size,
			FileIdentity: ident,
		})
	}
	return children, nil
}

// cleanupStalePlaceholders removes local placeholders whose backing NeoFS
// object no longer exists.  Called after fetchObjects so the authoritative
// NeoFS listing is available.  It compares the on-disk directory entries with
// the children list and removes anything that is stale.
func (p *neofsProvider) cleanupStalePlaceholders(seg, prefix string, children []cfapi.Placeholder) {
	dirDisk := filepath.Join(p.root, seg)
	if prefix != "" {
		dirDisk = filepath.Join(append([]string{p.root, seg}, splitPath(strings.TrimSuffix(prefix, "/"))...)...)
	}

	dirEntries, err := os.ReadDir(dirDisk)
	if err != nil {
		return
	}

	neoNames := make(map[string]struct{}, len(children))
	for _, ch := range children {
		neoNames[strings.ToLower(ch.Name)] = struct{}{}
	}

	for _, de := range dirEntries {
		name := de.Name()
		if _, exists := neoNames[strings.ToLower(name)]; exists {
			continue
		}
		fullPath := filepath.Join(dirDisk, name)
		if p.wasRecentlyDeleted(fullPath) {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		if !isCloudPlaceholder(fi) {
			continue
		}
		p.log.Info("cfapi: removing stale placeholder", "path", fullPath)
		if de.IsDir() {
			os.RemoveAll(fullPath)
		} else {
			os.Remove(fullPath)
		}
	}
}

// isCloudPlaceholder returns true if the file has cloud file attributes,
// meaning it was created by CfCreatePlaceholders or CfConvertToPlaceholder.
// Regular local files (e.g. dragged in by the user) won't have these.
const (
	fileAttrRecallOnDataAccess = 0x00400000
	fileAttrRecallOnOpen       = 0x00040000
	fileAttrOffline            = 0x00001000
	cloudPlaceholderMask       = fileAttrRecallOnDataAccess | fileAttrRecallOnOpen | fileAttrOffline
)

func isCloudPlaceholder(fi os.FileInfo) bool {
	wfd, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	return wfd.FileAttributes&cloudPlaceholderMask != 0
}

// errUseDiskFetch means the file is not (yet) a resolvable NeoFS object — read the local path.
var errUseDiskFetch = errors.New("cfapi: fetch data from disk")

// FetchData is called when Windows reads bytes from a placeholder file.
func (p *neofsProvider) FetchData(req cfapi.FetchDataRequest, sess *cfapi.TransferSession) error {
	identity := string(req.FileIdentity)
	p.log.Debug("cfapi: FetchData", "path", req.NormalizedPath, "id", identity,
		"offset", req.RequiredOffset, "length", req.RequiredLength)

	err := p.fetchDataFromNeoFS(req, sess, identity)
	if err == nil {
		return nil
	}
	if errors.Is(err, errUseDiskFetch) {
		// Locally created files (e.g. “New Text Document”) have no NeoFS object id yet;
		// Windows still issues FetchData — serve bytes from disk (0x80070781 otherwise).
		return p.fetchDataFromDisk(req, sess)
	}
	return err
}

func (p *neofsProvider) fetchDataFromNeoFS(req cfapi.FetchDataRequest, sess *cfapi.TransferSession, identity string) (err error) {
	containerIDStr, objectIDStr, err := parseIdentity(identity)
	if err != nil {
		return errUseDiskFetch
	}
	if objectIDStr == "" || strings.HasPrefix(objectIDStr, "dir:") {
		return errUseDiskFetch
	}

	var cnr cid.ID
	if err := cnr.DecodeString(containerIDStr); err != nil {
		return errUseDiskFetch
	}
	var objectID oid.ID
	if err := objectID.DecodeString(objectIDStr); err != nil {
		return errUseDiskFetch
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hdr, err := p.neo.ObjectHead(ctx, cnr, objectID)
	if err != nil {
		// The file has a valid NeoFS identity (container:object) so it was
		// backed by a real NeoFS object.  Do NOT fall back to fetchDataFromDisk:
		// the local file is a dehydrated cloud placeholder and os.Open would
		// trigger another FetchData callback, deadlocking until Explorer shows
		// ERROR_CLOUD_FILE_REQUEST_TIMEOUT (0x800701AA).
		p.log.Warn("cfapi: FetchData: NeoFS object unavailable, failing hydration",
			"path", req.NormalizedPath, "container", containerIDStr,
			"object", objectIDStr, "err", err)
		return fmt.Errorf("NeoFS object unavailable (container=%s object=%s): %w",
			containerIDStr, objectIDStr, err)
	}
	fileSize := int64(hdr.PayloadSize())

	start := req.RequiredOffset
	end := start + req.RequiredLength
	if req.RequiredLength < 0 {
		end = fileSize
	}
	if start < 0 {
		start = 0
	}
	if end > fileSize {
		end = fileSize
	}
	if start >= end {
		return nil
	}

	hydrationPath := req.NormalizedPath
	if hydrationPath != "" {
		p.setExplorerInSyncBadge(hydrationPath, false)
		defer func() {
			if err == nil {
				p.setExplorerInSyncBadge(hydrationPath, true)
			}
		}()
	}

	// Objects up to hydrationCacheMaxObjectBytes are fetched once into the shared disk cache
	// (same key space as FUSE); re-reads and overlapping ranges use ReadAt from the blob.
	// Larger objects keep using ranged NeoFS reads so Explorer does not block on a full download.
	var readChunk func(off, take int64) ([]byte, error)
	if fileSize <= p.hydrationCacheMaxObjectBytes && p.cache != nil {
		cpath, _, err := p.cache.GetOrFetch(ctx, cache.Key(containerIDStr, objectIDStr), func(ctx context.Context, w io.Writer) error {
			_, r, err := p.neo.ObjectGet(ctx, cnr, objectID)
			if err != nil {
				return err
			}
			defer r.Close()
			_, err = io.Copy(w, r)
			return err
		})
		if err != nil {
			sess.Fail(-0x3FFFFFBF)
			return err
		}
		f, err := os.Open(cpath)
		if err != nil {
			sess.Fail(-0x3FFFFFBF)
			return err
		}
		defer f.Close()
		readChunk = func(off, take int64) ([]byte, error) {
			buf := make([]byte, take)
			n, err := f.ReadAt(buf, off)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return buf[:n], nil
		}
	} else {
		readChunk = func(off, take int64) ([]byte, error) {
			return p.neo.ReadObjectRange(ctx, containerIDStr, objectIDStr, off, take)
		}
	}

	const align = int64(4096)
	const chunkMax = int64(4 << 20)
	a0 := start / align * align
	a1 := (end + align - 1) / align * align
	if a1 > fileSize {
		a1 = fileSize
	}

	for off := a0; off < a1; {
		remain := a1 - off
		if remain <= 0 {
			break
		}
		atEOF := off+remain >= fileSize
		var take int64
		if atEOF {
			take = remain
		} else {
			n := chunkMax
			if n > remain {
				n = remain
			}
			take = (n / align) * align
			if take == 0 {
				take = align
				if take > remain {
					take = remain
				}
			}
		}

		buf, err := readChunk(off, take)
		if err != nil {
			sess.Fail(-0x3FFFFFBF)
			return err
		}
		if err := sess.Write(buf, off); err != nil {
			sess.Fail(-0x3FFFFFBF)
			return err
		}
		off += int64(len(buf))
	}
	return nil
}

func (p *neofsProvider) fetchDataFromDisk(req cfapi.FetchDataRequest, sess *cfapi.TransferSession) error {
	path := req.NormalizedPath
	if path == "" {
		// CfApi sometimes fires FetchData with no NormalizedPath for locally
		// created files (paste, new file).  The data is already on disk so
		// there is nothing for us to transfer — complete without error.
		p.log.Debug("cfapi: FetchData: empty NormalizedPath, completing without transfer")
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		sess.Fail(-0x3FFFFFBF)
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		sess.Fail(-0x3FFFFFBF)
		return fmt.Errorf("cfapi: FetchData not a regular file")
	}
	fileSize := fi.Size()
	start := req.RequiredOffset
	end := start + req.RequiredLength
	if req.RequiredLength < 0 {
		end = fileSize
	}
	if start < 0 {
		start = 0
	}
	if end > fileSize {
		end = fileSize
	}
	if start >= end {
		return nil
	}
	n := int(end - start)
	buf := make([]byte, n)
	m, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		sess.Fail(-0x3FFFFFBF)
		return err
	}
	buf = buf[:m]
	if len(buf) == 0 {
		return nil
	}
	if err := sess.Write(buf, start); err != nil {
		sess.Fail(-0x3FFFFFBF)
		return err
	}
	return nil
}

func (p *neofsProvider) NotifyFileClose(req cfapi.NotifyFileCloseRequest) {
	if req.Deleted && req.NormalizedPath != "" {
		if !p.ro {
			go p.handleCloudFileDeleted(req.NormalizedPath)
		}
		return
	}
	if p.ro {
		return
	}
	rel := p.normalizedToRel(req.NormalizedPath)
	parts := splitPath(rel)
	if len(parts) < 2 {
		return
	}
	if _, ok := p.resolveSeg(parts[0]); !ok {
		return
	}
	diskPath := longWindowsPath(filepath.Join(append([]string{p.root}, parts...)...))
	if st, err := os.Stat(diskPath); err == nil && st.IsDir() {
		go p.maybeUploadPathFromWatcher(diskPath)
		return
	}
	go func() { _ = p.uploadClosedFile(context.Background(), diskPath) }()
}

func (p *neofsProvider) NotifyRenameCompletion(req cfapi.NotifyRenameCompletionRequest) {
	if p.ro {
		return
	}

	// Detect recycle-bin rename (Explorer "Delete" → move to $Recycle.Bin).
	// Use the SOURCE path to find the NeoFS object and delete it.
	if isRecycleBinPath(req.NormalizedPath) && req.SourcePath != "" {
		p.log.Info("cfapi: rename to Recycle Bin detected, deleting from NeoFS", "src", req.SourcePath)
		go p.handleCloudFileDeleted(req.SourcePath)
		return
	}

	if req.NormalizedPath == "" {
		return
	}
	rel := p.normalizedToRel(req.NormalizedPath)
	parts := splitPath(rel)
	if len(parts) < 2 {
		return
	}
	if _, ok := p.resolveSeg(parts[0]); !ok {
		return
	}
	diskPath := longWindowsPath(filepath.Join(append([]string{p.root}, parts...)...))
	if st, err := os.Stat(diskPath); err == nil && st.IsDir() {
		go p.maybeUploadPathFromWatcher(diskPath)
		return
	}
	var srcPath string
	if s := strings.TrimSpace(req.SourcePath); s != "" {
		srcPath = longWindowsPath(filepath.Clean(s))
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		err := p.uploadClosedFile(ctx, diskPath)
		if err != nil {
			if errors.Is(err, errUploadClosedSkipped) {
				p.log.Debug("cfapi: rename destination upload skipped", "dest", diskPath, "err", err)
			} else {
				p.log.Warn("cfapi: rename destination upload failed; old NeoFS path kept", "dest", diskPath, "err", err)
			}
			return
		}
		if srcPath == "" || isRecycleBinPath(srcPath) {
			return
		}
		p.deleteNeoFSObjectsAtRenamedSource(ctx, srcPath)
	}()
}

func (p *neofsProvider) NotifyDeleteCompletion(req cfapi.NotifyDeleteCompletionRequest) {
	if p.ro || req.NormalizedPath == "" {
		return
	}
	p.log.Info("cfapi: NotifyDeleteCompletion", "path", req.NormalizedPath)
	go p.handleCloudFileDeleted(req.NormalizedPath)
}

// handleCloudFileDeleted is called when CfApi reports a placeholder was deleted by the user.
// It deletes the corresponding NeoFS object(s) and records the path so the watcher
// doesn't try to restore the placeholder.
func (p *neofsProvider) handleCloudFileDeleted(normalizedPath string) {
	rel := p.normalizedToRel(normalizedPath)
	parts := splitPath(rel)
	if len(parts) < 2 {
		return
	}
	cidStr, ok := p.resolveSeg(parts[0])
	if !ok {
		return
	}
	var cnr cid.ID
	if err := cnr.DecodeString(cidStr); err != nil {
		return
	}
	neoPath := strings.Join(parts[1:], "/")
	diskPath := filepath.Join(append([]string{p.root}, parts...)...)

	p.recordRecentDelete(diskPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	n, err := deleteNeoFSObjectsUnderPrefix(ctx, p.neo, cnr, neoPath)
	if err != nil {
		p.log.Error("cfapi: delete from NeoFS failed", "path", neoPath, "err", err)
		return
	}
	if n > 0 {
		p.log.Info("cfapi: deleted NeoFS objects", "path", neoPath, "count", n, "container", cidStr)
		p.auditRecord("neofs_delete_via_explorer", map[string]any{
			"path": diskPath, "container_id": cidStr,
			"object_path": neoPath, "deleted_count": n,
		})
	}
	// Allow immediate re-upload at the same NeoFS path (copy/paste back, new file same name).
	// Without this, uploadDedupe still matches the pre-delete put and skips ObjectPut.
	p.clearUploadDedupeUnderNeoPrefix(cidStr, neoPath)
}

func (p *neofsProvider) recentDeleteKey(path string) string {
	return strings.ToLower(stripDriveLetter(filepath.Clean(path)))
}

func (p *neofsProvider) recordRecentDelete(diskPath string) {
	p.recentDeletesMu.Lock()
	if p.recentDeletes == nil {
		p.recentDeletes = make(map[string]time.Time)
	}
	p.recentDeletes[p.recentDeleteKey(diskPath)] = time.Now()
	p.recentDeletesMu.Unlock()
}

func (p *neofsProvider) clearRecentDelete(diskPath string) {
	key := p.recentDeleteKey(diskPath)
	p.recentDeletesMu.Lock()
	defer p.recentDeletesMu.Unlock()
	if p.recentDeletes != nil {
		delete(p.recentDeletes, key)
	}
}

const recentDeleteTTL = 120 * time.Second

func (p *neofsProvider) wasRecentlyDeleted(diskPath string) bool {
	key := p.recentDeleteKey(diskPath)
	p.recentDeletesMu.Lock()
	defer p.recentDeletesMu.Unlock()
	t, ok := p.recentDeletes[key]
	if !ok {
		return false
	}
	if time.Since(t) > recentDeleteTTL {
		delete(p.recentDeletes, key)
		return false
	}
	return true
}

// cfUploadReader counts bytes into the uploads tracker (same idea as FUSE uploadFileHandle).
type cfUploadReader struct {
	r io.Reader
	e *uploads.Entry
}

func (c *cfUploadReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 && c.e != nil {
		c.e.AddSent(int64(n))
	}
	return n, err
}

func (p *neofsProvider) lockForUploadTrackKey(trackKey string) *sync.Mutex {
	p.uploadMu.Lock()
	defer p.uploadMu.Unlock()
	if p.uploadLocks == nil {
		p.uploadLocks = make(map[string]*sync.Mutex)
	}
	m, ok := p.uploadLocks[trackKey]
	if !ok {
		m = &sync.Mutex{}
		p.uploadLocks[trackKey] = m
	}
	return m
}

func (p *neofsProvider) shouldSkipRapidDuplicatePut(trackKey string, fi os.FileInfo) bool {
	p.uploadDedupeMu.Lock()
	defer p.uploadDedupeMu.Unlock()
	prev, ok := p.uploadDedupe[trackKey]
	if !ok {
		return false
	}
	if time.Since(prev.recordedAt) > uploadRapidDedupeWindow {
		return false
	}
	if fi.Size() != prev.size {
		return false
	}
	return true
}

func (p *neofsProvider) recordDedupeUpload(trackKey string, fi os.FileInfo) {
	p.uploadDedupeMu.Lock()
	defer p.uploadDedupeMu.Unlock()
	if p.uploadDedupe == nil {
		p.uploadDedupe = make(map[string]uploadDedupeEntry)
	}
	now := time.Now()
	for k, v := range p.uploadDedupe {
		if now.Sub(v.recordedAt) > uploadDedupeMapTTL {
			delete(p.uploadDedupe, k)
		}
	}
	p.uploadDedupe[trackKey] = uploadDedupeEntry{
		size:       fi.Size(),
		recordedAt: now,
	}
}

// clearUploadDedupeUnderNeoPrefix drops size/mtime dedupe for a path (and children) after NeoFS delete,
// so a new local file at the same key is not mistaken for the old upload.
func (p *neofsProvider) clearUploadDedupeUnderNeoPrefix(containerIDStr, neoPath string) {
	base := containerIDStr + "/" + filepath.ToSlash(neoPath)
	p.uploadDedupeMu.Lock()
	defer p.uploadDedupeMu.Unlock()
	if p.uploadDedupe == nil {
		return
	}
	for k := range p.uploadDedupe {
		if k == base || strings.HasPrefix(k, base+"/") {
			delete(p.uploadDedupe, k)
		}
	}
}

// deleteNeoFSObjectsAtRenamedSource removes objects at the pre-rename path after a successful
// upload to the destination (Explorer rename / move within the sync root).
func (p *neofsProvider) deleteNeoFSObjectsAtRenamedSource(ctx context.Context, sourceAbs string) {
	sourceAbs = filepath.Clean(sourceAbs)
	if isRecycleBinPath(sourceAbs) {
		return
	}
	cidStr, neoRel, ok := p.mountDiskToNeo(sourceAbs)
	if !ok {
		return
	}
	var cnr cid.ID
	if err := cnr.DecodeString(cidStr); err != nil {
		return
	}
	n, err := deleteNeoFSObjectsUnderPrefix(ctx, p.neo, cnr, neoRel)
	if err != nil {
		p.log.Error("cfapi: rename delete source NeoFS failed", "neoPath", neoRel, "err", err)
		return
	}
	if n > 0 {
		p.log.Info("cfapi: rename removed old NeoFS path", "neoPath", neoRel, "count", n, "container", cidStr)
		p.auditRecord("neofs_delete_rename_source", map[string]any{
			"disk_path": sourceAbs, "container_id": cidStr,
			"object_path": filepath.ToSlash(neoRel), "deleted_count": n,
		})
	}
	p.clearUploadDedupeUnderNeoPrefix(cidStr, neoRel)
}

func (p *neofsProvider) auditRecord(op string, data map[string]any) {
	if p.audit != nil {
		p.audit.Record(op, data)
	}
}

// setExplorerInSyncBadge updates the Windows cloud placeholder overlay (CfSetInSyncState).
// inSync=false means "pending" to Explorer (covers upload in progress and hydration); true means synced.
func (p *neofsProvider) setExplorerInSyncBadge(absPath string, inSync bool) {
	if absPath == "" {
		return
	}
	if err := cfapi.SetInSyncState(absPath, inSync); err != nil && p.log != nil {
		p.log.Warn("cfapi: SetInSyncState failed", "path", absPath, "inSync", inSync, "err", err)
	}
}

func (p *neofsProvider) recordUploadHistory(started time.Time, neoKey, diskPath string, bytes int64, ok bool, detail string) {
	if p == nil || p.uploadHistory == nil {
		return
	}
	st := "ok"
	if !ok {
		st = "failed"
	}
	p.uploadHistory.Append(uploads.HistoryItem{
		StartedAt:  started,
		FinishedAt: time.Now(),
		NeoKey:     neoKey,
		DiskPath:   diskPath,
		Bytes:      bytes,
		Status:     st,
		Detail:     detail,
	})
}

func (p *neofsProvider) uploadClosedFile(ctx context.Context, diskPath string) error {
	// Do not gate on wasRecentlyDeleted here: after an intentional NeoFS delete the user may
	// immediately copy a new file to the same path; recentDeletes is only for suppressing
	// placeholder restore (tryRestorePlaceholderAfterRemove) and stale listing cleanup.

	diskPath = longWindowsPath(filepath.Clean(diskPath))
	containerIDStr, neoRelPath, mapped := p.mountDiskToNeo(diskPath)
	if !mapped {
		p.auditRecord("object_put_skipped", map[string]any{
			"source": "cfapi_close_or_watcher", "reason": "path_not_under_mount",
			"disk_path": diskPath,
		})
		return fmt.Errorf("%w: path not under mount", errUploadClosedSkipped)
	}
	if isEphemeralEditorHiddenName(filepath.Base(filepath.ToSlash(neoRelPath))) {
		p.auditRecord("object_put_skipped", map[string]any{
			"source": "cfapi_close_or_watcher", "reason": "ephemeral_editor_temp",
			"disk_path": diskPath, "container_id": containerIDStr,
			"object_path": filepath.ToSlash(neoRelPath),
		})
		return fmt.Errorf("%w: ephemeral editor temp", errUploadClosedSkipped)
	}

	defer func() {
		if r := recover(); r != nil {
			p.log.Error("cfapi: upload panic", "recover", r)
			p.auditRecord("object_put_panic", map[string]any{
				"disk_path": diskPath, "container_id": containerIDStr,
				"object_path": filepath.ToSlash(neoRelPath), "recover": fmt.Sprint(r),
			})
		}
	}()

	trackKey := containerIDStr + "/" + filepath.ToSlash(neoRelPath)
	mu := p.lockForUploadTrackKey(trackKey)
	mu.Lock()
	defer mu.Unlock()

	fi, err := os.Stat(diskPath)
	if err != nil {
		p.auditRecord("object_put_skipped", map[string]any{
			"source": "cfapi_close_or_watcher", "reason": "stat_failed", "disk_path": diskPath,
			"container_id": containerIDStr, "object_path": filepath.ToSlash(neoRelPath),
			"error": err.Error(),
		})
		return fmt.Errorf("stat %q: %w", diskPath, err)
	}
	if fi.IsDir() {
		p.auditRecord("object_put_skipped", map[string]any{
			"source": "cfapi_close_or_watcher", "reason": "is_directory", "disk_path": diskPath,
			"container_id": containerIDStr, "object_path": filepath.ToSlash(neoRelPath),
		})
		return fmt.Errorf("%w (is_directory)", errUploadClosedSkipped)
	}
	// Cloud placeholders can report non-"regular" Mode bits; if it is not a directory and we can open it, upload.
	var cnr cid.ID
	if err := cnr.DecodeString(containerIDStr); err != nil {
		p.log.Error("cfapi: upload close: bad container id", "id", containerIDStr, "err", err)
		p.auditRecord("object_put_skipped", map[string]any{
			"source": "cfapi_close_or_watcher", "reason": "bad_container_id",
			"disk_path": diskPath, "container_id": containerIDStr, "object_path": filepath.ToSlash(neoRelPath),
		})
		return fmt.Errorf("%w: bad container id", errUploadClosedSkipped)
	}

	if p.shouldSkipRapidDuplicatePut(trackKey, fi) {
		p.log.Debug("cfapi: upload skip: rapid duplicate put", "path", neoRelPath)
		p.auditRecord("object_put_skipped", map[string]any{
			"source": "cfapi_close_or_watcher", "reason": "rapid_duplicate_put",
			"disk_path": diskPath, "container_id": containerIDStr, "object_path": filepath.ToSlash(neoRelPath),
		})
		return nil
	}

	oldIDs, _ := p.neo.FindObjectIDsByExactPath(ctx, cnr, neoRelPath)
	f, err := os.Open(diskPath)
	if err != nil {
		p.log.Error("cfapi: upload close: open", "path", diskPath, "err", err)
		p.auditRecord("object_put_failed", map[string]any{
			"source": "cfapi_close_or_watcher", "stage": "open", "disk_path": diskPath,
			"container_id": containerIDStr, "object_path": filepath.ToSlash(neoRelPath), "error": err.Error(),
		})
		p.recordUploadHistory(time.Now(), trackKey, diskPath, fi.Size(), false, "open: "+err.Error())
		return fmt.Errorf("open %q: %w", diskPath, err)
	}
	defer f.Close()

	p.setExplorerInSyncBadge(diskPath, false)

	uploadStarted := time.Now()
	var payload io.Reader = f
	var trackerEntry *uploads.Entry
	if p.uploadTracker != nil {
		trackerEntry = p.uploadTracker.Register(trackKey, fi.Size())
		defer p.uploadTracker.Finish(trackKey)
		payload = &cfUploadReader{r: f, e: trackerEntry}
	}

	newID, putErr := p.neo.ObjectPut(ctx, cnr, neoRelPath, payload, "")
	if putErr != nil {
		p.log.Error("cfapi: upload close: ObjectPut failed", "path", neoRelPath, "err", putErr)
		p.auditRecord("object_put_failed", map[string]any{
			"source": "cfapi_close_or_watcher", "stage": "object_put", "disk_path": diskPath,
			"container_id": containerIDStr, "object_path": filepath.ToSlash(neoRelPath), "error": putErr.Error(),
		})
		p.recordUploadHistory(uploadStarted, trackKey, diskPath, fi.Size(), false, putErr.Error())
		return fmt.Errorf("object put %q: %w", neoRelPath, putErr)
	}
	var deleted []string
	for _, id := range oldIDs {
		if id == newID {
			continue
		}
		_ = p.neo.ObjectDelete(ctx, cnr, id)
		deleted = append(deleted, id.EncodeToString())
	}
	p.neo.InvalidateContainerScan(cnr)
	p.recordDedupeUpload(trackKey, fi)
	p.clearRecentDelete(diskPath)
	p.log.Info("cfapi: upload close ok", "path", neoRelPath, "obj", newID.EncodeToString())

	ident := cfapi.IdentityFromString(containerIDStr + ":" + newID.EncodeToString())
	if err := cfapi.ConvertToPlaceholder(diskPath, ident, true); err != nil {
		p.log.Debug("cfapi: convert to placeholder after upload", "path", diskPath, "err", err)
	}

	p.auditRecord("object_put_completed", map[string]any{
		"source": "cfapi_close_or_watcher", "disk_path": diskPath,
		"container_id": containerIDStr, "object_path": filepath.ToSlash(neoRelPath),
		"new_object_id": newID.EncodeToString(), "deleted_object_ids": deleted, "bytes": fi.Size(),
	})
	p.recordUploadHistory(uploadStarted, trackKey, diskPath, fi.Size(), true, "")
	return nil
}

func (p *neofsProvider) ValidateData(req cfapi.ValidateDataRequest, vs *cfapi.ValidateSession) error {
	ctx := context.Background()
	var fileSize int64
	identity := string(req.FileIdentity)
	if containerIDStr, objectIDStr, err := parseIdentity(identity); err == nil {
		var cnr cid.ID
		var objectID oid.ID
		if cnr.DecodeString(containerIDStr) == nil && objectID.DecodeString(objectIDStr) == nil {
			if hdr, err := p.neo.ObjectHead(ctx, cnr, objectID); err == nil {
				fileSize = int64(hdr.PayloadSize())
			}
		}
	}
	if fileSize == 0 {
		rel := p.normalizedToRel(req.NormalizedPath)
		parts := splitPath(rel)
		if len(parts) > 0 {
			diskPath := filepath.Join(append([]string{p.root}, parts...)...)
			if fi, err := os.Stat(diskPath); err == nil && !fi.IsDir() {
				fileSize = fi.Size()
			}
		}
	}
	return vs.AckRange(req.RequiredOffset, req.RequiredLength, fileSize)
}

func (p *neofsProvider) findSession(_ int64) *cfapi.Session { return p.session }

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
