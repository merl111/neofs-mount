package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	apistatus "github.com/nspcc-dev/neofs-sdk-go/client/status"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"

	"github.com/mathias/neofs-mount/internal/cache"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/mathias/neofs-mount/internal/uploads"
)

type rootNode struct {
	fs.Inode

	log          *slog.Logger
	neo          *neofs.Client
	cache        *cache.Cache
	dirCache     *dirCache
	ro           bool
	epoch        uint64
	uploadTracker *uploads.Tracker

	mu            sync.Mutex
	containerByUI map[string]cid.ID

	entriesMu      sync.Mutex
	rootEntries    []fuse.DirEntry
	rootEntriesAt  time.Time
	rootEntriesTTL time.Duration

	ignoreContainers map[string]struct{}
}

func (n *rootNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Attr.Mode = fuse.S_IFDIR | 0o555
	if !n.ro {
		out.Attr.Mode = fuse.S_IFDIR | 0o755
	}
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	return 0
}

func (n *rootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Root listing is relatively expensive (container name lookups), so keep it hot briefly.
	n.entriesMu.Lock()
	if !n.rootEntriesAt.IsZero() && n.rootEntriesTTL > 0 && time.Since(n.rootEntriesAt) < n.rootEntriesTTL {
		out := make([]fuse.DirEntry, len(n.rootEntries))
		copy(out, n.rootEntries)
		n.entriesMu.Unlock()
		return fs.NewListDirStream(out), 0
	}
	n.entriesMu.Unlock()

	containers, err := n.neo.ListContainers(ctx)
	if err != nil {
		return nil, errno(err)
	}

	// Stable ordering.
	sort.Slice(containers, func(i, j int) bool {
		return containers[i].EncodeToString() < containers[j].EncodeToString()
	})

	type named struct {
		id   cid.ID
		name string
	}

	namedList := make([]named, 0, len(containers))
	for _, id := range containers {
		if n.isIgnored(id.EncodeToString()) {
			continue
		}
		ui := id.EncodeToString()
		if cnr, err := n.neo.ContainerGet(ctx, id); err == nil {
			if nm := strings.TrimSpace(cnr.Name()); nm != "" {
				ui = sanitizeDirName(nm)
			}
		}
		if ui == "" {
			ui = id.EncodeToString()
		}
		namedList = append(namedList, named{id: id, name: ui})
	}

	// Ensure UI names are unique; fall back to container IDs on collision.
	count := map[string]int{}
	for _, it := range namedList {
		count[it.name]++
	}
	for i := range namedList {
		if count[namedList[i].name] > 1 {
			namedList[i].name = namedList[i].id.EncodeToString()
		}
	}

	sort.Slice(namedList, func(i, j int) bool { return namedList[i].name < namedList[j].name })

	m := make(map[string]cid.ID, len(namedList))
	entries := make([]fuse.DirEntry, 0, len(namedList))
	for _, it := range namedList {
		m[it.name] = it.id
		entries = append(entries, fuse.DirEntry{Name: it.name, Mode: fuse.S_IFDIR})
	}
	n.mu.Lock()
	n.containerByUI = m
	n.mu.Unlock()

	n.entriesMu.Lock()
	n.rootEntries = entries
	n.rootEntriesAt = time.Now()
	if n.rootEntriesTTL == 0 {
		n.rootEntriesTTL = 30 * time.Second
	}
	n.entriesMu.Unlock()

	return fs.NewListDirStream(entries), 0
}

func (n *rootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var cnr cid.ID
	if err := cnr.DecodeString(name); err != nil {
		// Try name-based mapping from most recent Readdir().
		n.mu.Lock()
		if n.containerByUI != nil {
			if id, ok := n.containerByUI[name]; ok {
				cnr = id
				n.mu.Unlock()
				goto found
			}
		}
		n.mu.Unlock()

		// containerByUI may not be populated yet (no prior ls of the mount root).
		// Trigger an on-demand scan to build the name → container ID map, then retry.
		if _, errno := n.Readdir(ctx); errno == 0 {
			n.mu.Lock()
			id, ok := n.containerByUI[name]
			n.mu.Unlock()
			if ok {
				cnr = id
				goto found
			}
		}

		out.SetEntryTimeout(5 * time.Second) // negative: short TTL so retries work
		return nil, syscall.ENOENT
	}
found:
	if n.isIgnored(cnr.EncodeToString()) {
		out.SetEntryTimeout(5 * time.Second) // negative: short TTL
		return nil, syscall.ENOENT
	}

	child := &containerNode{
		log:           n.log,
		neo:           n.neo,
		cache:         n.cache,
		dirCache:      n.dirCache,
		ro:            n.ro,
		cnr:           cnr,
		path:          "",
		uploadTracker: n.uploadTracker,
	}

	out.Attr.Mode = fuse.S_IFDIR | 0o555
	if !n.ro {
		out.Attr.Mode = fuse.S_IFDIR | 0o755
	}
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	out.SetEntryTimeout(5 * time.Minute)
	out.SetAttrTimeout(5 * time.Minute)

	st := fs.StableAttr{Mode: fuse.S_IFDIR}
	return n.NewInode(ctx, child, st), 0
}


func sanitizeDirName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "/", "_")
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}

func (n *rootNode) isIgnored(containerID string) bool {
	if n == nil || n.ignoreContainers == nil {
		return false
	}
	_, ok := n.ignoreContainers[containerID]
	return ok
}

func makeIgnoreSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		m[id] = struct{}{}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

type containerNode struct {
	fs.Inode

	log          *slog.Logger
	neo          *neofs.Client
	cache        *cache.Cache
	dirCache     *dirCache
	ro           bool
	cnr          cid.ID
	path         string
	uploadTracker *uploads.Tracker
}

func (n *containerNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	children, err := n.listChildren(ctx)
	if err != 0 {
		return nil, err
	}
	return fs.NewListDirStream(children), 0
}

func (n *containerNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Attr.Mode = fuse.S_IFDIR | 0o555
	if !n.ro {
		out.Attr.Mode = fuse.S_IFDIR | 0o755
	}
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	return 0
}

func (n *containerNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	out.Attr.Mode = fuse.S_IFDIR | 0o555
	if !n.ro {
		out.Attr.Mode = fuse.S_IFDIR | 0o755
	}
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	return 0
}

func (n *containerNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return nil, syscall.ENOENT
	}

	prefix := joinRel(n.path, name)

	entries, _, scanErr := n.neo.ListEntriesByHeadScan(ctx, n.cnr)
	if scanErr != nil {
		if n.log != nil {
			n.log.Debug("lookup failed", "container", n.cnr.EncodeToString(), "name", name, "err", scanErr)
		}
		return nil, errno(scanErr)
	}

	var foundFile oid.ID
	var foundFileSize int64
	var foundFileTime time.Time
	foundDir := false

	for _, e := range entries {
		fp := cleanLeadingSlash(e.FilePath)
		ky := cleanLeadingSlash(e.Key)

		// Match against FilePath or Key as a path.
		for _, p := range []string{fp, ky} {
			if p == "" {
				continue
			}
			if p == prefix {
				foundFile = e.ObjectID
				foundFileSize = e.Size
				foundFileTime = e.Time
			} else if strings.HasPrefix(p, prefix+"/") {
				foundDir = true
			}
		}

		// Flat root: also match by FileName, Name, or object ID.
		if foundFile.IsZero() && !foundDir && n.path == "" {
			fn := strings.TrimPrefix(e.FileName, "/")
			nm := strings.TrimPrefix(e.Name, "/")
			if fn == name || nm == name || e.ObjectID.EncodeToString() == name {
				foundFile = e.ObjectID
				foundFileSize = e.Size
				foundFileTime = e.Time
			}
		}
	}

	if foundDir {
		if n.log != nil {
			n.log.Debug("lookup dir", "container", n.cnr.EncodeToString(), "name", name)
		}
		child := &containerNode{
			log:      n.log,
			neo:      n.neo,
			cache:    n.cache,
			dirCache: n.dirCache,
			ro:       n.ro,
			cnr:      n.cnr,
			path:     prefix,
		}
		out.Attr.Mode = fuse.S_IFDIR | 0o555
		if !n.ro {
			out.Attr.Mode = fuse.S_IFDIR | 0o755
		}
		out.Attr.Uid = uint32(os.Getuid())
		out.Attr.Gid = uint32(os.Getgid())
		out.SetEntryTimeout(5 * time.Minute)
		out.SetAttrTimeout(5 * time.Minute)
		st := fs.StableAttr{Mode: fuse.S_IFDIR}
		return n.NewInode(ctx, child, st), 0
	}

	if !foundFile.IsZero() {
		if n.log != nil {
			n.log.Debug("lookup file", "container", n.cnr.EncodeToString(), "name", name, "obj", foundFile.EncodeToString(), "size", foundFileSize)
		}
		out.Attr.Mode = fuse.S_IFREG | 0o444
		if !n.ro {
			out.Attr.Mode = fuse.S_IFREG | 0o644
		}
		out.Attr.Uid = uint32(os.Getuid())
		out.Attr.Gid = uint32(os.Getgid())
		
		size := uint64(max0(foundFileSize))
		fileTime := foundFileTime

		if size == 0 || fileTime.IsZero() {
			if hdr, err := n.neo.ObjectHead(ctx, n.cnr, foundFile); err == nil {
				if size == 0 {
					size = hdr.PayloadSize()
				}
				if fileTime.IsZero() {
					// Extract timestamp from head fallback
					for _, a := range hdr.Attributes() {
						if a.Key() == "Timestamp" || a.Key() == "LastModified" {
							if t, err := time.Parse(time.RFC3339Nano, a.Value()); err == nil {
								fileTime = t
							} else if t, err := time.Parse(time.RFC3339, a.Value()); err == nil {
								fileTime = t
							} else if sec, err := strconv.ParseInt(a.Value(), 10, 64); err == nil {
								fileTime = time.Unix(sec, 0)
							}
							break
						}
					}
				}
			}
		}

		out.Attr.Size = size
		if !fileTime.IsZero() {
			out.Attr.SetTimes(nil, &fileTime, &fileTime)
		}
		out.SetEntryTimeout(5 * time.Minute)
		out.SetAttrTimeout(5 * time.Minute)

		child := &fileNode{
			log:      n.log,
			neo:      n.neo,
			cache:    n.cache,
			ro:       n.ro,
			cnr:      n.cnr,
			obj:      foundFile,
			relPath:  prefix,
			fileSize: size,
			fileTime: fileTime,
		}
		st := fs.StableAttr{Mode: fuse.S_IFREG}
		return n.NewInode(ctx, child, st), 0
	}

	if n.log != nil {
		n.log.Debug("lookup miss", "container", n.cnr.EncodeToString(), "name", name, "entries", len(entries))
	}
	// Cache the negative lookup too, so the kernel doesn't spam us with `.git` misses on every shell keypress.
	// Short TTL for negative entries — long TTL would cause the kernel to cache
	// "not found" for minutes, making newly uploaded files invisible.
	out.SetEntryTimeout(5 * time.Second)
	return nil, syscall.ENOENT
}

func (n *containerNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.ro {
		return syscall.EROFS
	}
	if name == "" || newName == "" || strings.Contains(name, "/") || strings.Contains(newName, "/") {
		return syscall.EINVAL
	}
	if flags != 0 {
		// We don't currently support RENAME_EXCHANGE/RENAME_NOREPLACE semantics.
		return syscall.ENOTSUP
	}

	dstParent, ok := newParent.(*containerNode)
	if !ok {
		return syscall.EXDEV
	}
	if dstParent.cnr != n.cnr {
		return syscall.EXDEV
	}

	srcRel := joinRel(n.path, name)
	dstRel := joinRel(dstParent.path, newName)

	// Find source objects (exact FilePath match).
	srcIDs, err := n.findObjectsByExactPath(ctx, srcRel)
	if err != nil {
		return errno(err)
	}
	if len(srcIDs) == 0 {
		return syscall.ENOENT
	}

	// If destination exists, we overwrite best-effort (delete then put).
	if dstIDs, err := n.findObjectsByExactPath(ctx, dstRel); err == nil {
		for _, id := range dstIDs {
			_ = n.neo.ObjectDelete(ctx, n.cnr, id)
		}
	}

	// Copy payload by streaming src -> put dst, then delete src.
	for _, srcID := range srcIDs {
		_, r, err := n.neo.ObjectGet(ctx, n.cnr, srcID)
		if err != nil {
			return errno(err)
		}
		newID, putErr := n.neo.ObjectPut(ctx, n.cnr, dstRel, r, "")
		_ = r.Close()
		if putErr != nil {
			return errno(putErr)
		}
		_ = newID

		if delErr := n.neo.ObjectDelete(ctx, n.cnr, srcID); delErr != nil {
			// Copy succeeded but delete failed: surface error.
			return errno(delErr)
		}
	}

	if n.dirCache != nil {
		n.dirCache.InvalidateContainer(n.cnr.EncodeToString())
	}
	n.neo.InvalidateContainerScan(n.cnr)
	return 0
}

func (n *containerNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.ro {
		return nil, nil, 0, syscall.EROFS
	}
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return nil, nil, 0, syscall.EINVAL
	}
	if n.cache == nil {
		return nil, nil, 0, syscall.EIO
	}

	relPath := joinRel(n.path, name)

	tmpDir := n.cache.Dir()
	if tmpDir == "" {
		return nil, nil, 0, syscall.EIO
	}

	f, err := os.CreateTemp(tmpDir, "neofs-upload-*.tmp")
	if err != nil {
		return nil, nil, 0, syscall.EIO
	}

	fn := &fileNode{
		log:      n.log,
		neo:      n.neo,
		cache:    n.cache,
		ro:       n.ro,
		cnr:      n.cnr,
		obj:      oid.ID{}, // set on upload
		relPath:  relPath,
		fileSize: 0,
	}
	st := fs.StableAttr{Mode: fuse.S_IFREG}
	in := n.NewInode(ctx, fn, st)

	h := &uploadFileHandle{
		log:           n.log,
		neo:           n.neo,
		cache:         n.cache,
		dirCache:      n.dirCache,
		uploadTracker: n.uploadTracker,
		node:          fn,
		tmpPath:       f.Name(),
		f:             f,
		cnr:           n.cnr,
		relPath:       relPath,
	}

	if n.dirCache != nil {
		n.dirCache.InvalidateContainer(n.cnr.EncodeToString())
	}
	n.neo.InvalidateContainerScan(n.cnr)
	return in, h, 0, 0
}

func (n *containerNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.ro {
		return syscall.EROFS
	}
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return syscall.EINVAL
	}

	relPath := joinRel(n.path, name)
	ids, err := n.findObjectsByExactPath(ctx, relPath)
	if err != nil {
		return errno(err)
	}
	if len(ids) == 0 {
		return syscall.ENOENT
	}

	for _, id := range ids {
		if err := n.neo.ObjectDelete(ctx, n.cnr, id); err != nil {
			return errno(err)
		}
	}

	if n.dirCache != nil {
		n.dirCache.InvalidateContainer(n.cnr.EncodeToString())
	}
	n.neo.InvalidateContainerScan(n.cnr)
	return 0
}

func (n *containerNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	// In NeoFS, empty directories are just objects with ContentType "application/x-directory".
	// Deleting them is identical to deleting a file.
	return n.Unlink(ctx, name)
}

func (n *containerNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.ro {
		return nil, syscall.EROFS
	}
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return nil, syscall.EINVAL
	}

	relPath := joinRel(n.path, name)

	// Since we are uploading an empty object, we don't need a real file.
	// But neo.ObjectPut expects an io.Reader. A strings.Reader is sufficient.
	emptyReader := strings.NewReader("")
	
	newID, err := n.neo.ObjectPutContentType(ctx, n.cnr, relPath, emptyReader, "", "application/x-directory")
	if err != nil {
		return nil, errno(err)
	}

	if n.log != nil {
		n.log.Info("created directory", "path", relPath, "obj", newID.EncodeToString())
	}

	if n.dirCache != nil {
		n.dirCache.InvalidateContainer(n.cnr.EncodeToString())
	}
	n.neo.InvalidateContainerScan(n.cnr)

	child := &containerNode{
		log:      n.log,
		neo:      n.neo,
		cache:    n.cache,
		dirCache: n.dirCache,
		ro:       n.ro,
		cnr:      n.cnr,
		path:     relPath,
	}

	out.Attr.Mode = fuse.S_IFDIR | 0o555
	if !n.ro {
		out.Attr.Mode = fuse.S_IFDIR | 0o755
	}
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	out.SetEntryTimeout(5 * time.Minute)
	out.SetAttrTimeout(5 * time.Minute)

	st := fs.StableAttr{Mode: fuse.S_IFDIR}
	return n.NewInode(ctx, child, st), 0
}

func (n *containerNode) findObjectsByExactPath(ctx context.Context, relPath string) ([]oid.ID, error) {
	relPath = strings.TrimPrefix(relPath, "/")
	entries, _, err := n.neo.ListEntriesByHeadScan(ctx, n.cnr)
	if err != nil {
		return nil, err
	}
	var out []oid.ID
	for _, e := range entries {
		if cleanLeadingSlash(e.FilePath) == relPath || cleanLeadingSlash(e.Key) == relPath {
			out = append(out, e.ObjectID)
		}
	}
	return out, nil
}

func (n *containerNode) listChildren(ctx context.Context) ([]fuse.DirEntry, syscall.Errno) {
	dirPrefix := n.path
	if dirPrefix != "" {
		dirPrefix += "/"
	}

	if n.dirCache != nil {
		if cached, ok := n.dirCache.Get(n.cnr.EncodeToString(), dirPrefix); ok {
			return cached, 0
		}
	}

	entries, _, scanErr := n.neo.ListEntriesByHeadScan(ctx, n.cnr)
	if scanErr != nil {
		if n.log != nil {
			n.log.Debug("readdir failed", "container", n.cnr.EncodeToString(), "prefix", dirPrefix, "err", scanErr)
		}
		return nil, errno(scanErr)
	}
	if n.log != nil {
		n.log.Debug("readdir", "container", n.cnr.EncodeToString(), "prefix", dirPrefix, "results", len(entries))
	}

	type childInfo struct {
		isDir bool
		size  uint64
		objID oid.ID
	}

	children := map[string]childInfo{}
	for _, e := range entries {
		if n.log != nil {
			n.log.Debug("readdir entry", "container", n.cnr.EncodeToString(),
				"obj", e.ObjectID.EncodeToString(), "FilePath", e.FilePath,
				"FileName", e.FileName, "Name", e.Name, "Key", e.Key, "Size", e.Size)
		}
		p := cleanLeadingSlash(e.FilePath)
		if p == "" {
			p = cleanLeadingSlash(e.Key)
		}
		if p == "" && dirPrefix == "" {
			// Flat root fallback via FileName, Name, or object ID.
			p = strings.TrimPrefix(e.FileName, "/")
			if p == "" {
				p = strings.TrimPrefix(e.Name, "/")
			}
			if p == "" {
				p = e.ObjectID.EncodeToString()
			}
		}
		if !strings.HasPrefix(p, dirPrefix) {
			continue
		}
		rest := strings.TrimPrefix(p, dirPrefix)
		if rest == "" {
			continue
		}

		seg, tail, hasSlash := strings.Cut(rest, "/")
		if seg == "" {
			continue
		}

		if hasSlash {
			// It's a descendant of a subdirectory; create/keep dir child.
			info := children[seg]
			info.isDir = true
			children[seg] = info
			_ = tail
			continue
		}

		// Direct file.
		// If there is both a file and a dir with the same name, prefer dir.
		info := children[seg]
		if info.isDir {
			continue
		}
		children[seg] = childInfo{
			isDir: false,
			size:  uint64(max0(e.Size)),
			objID: e.ObjectID,
		}
	}

	names := make([]string, 0, len(children))
	for n := range children {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		info := children[name]
		mode := uint32(fuse.S_IFREG)
		if info.isDir {
			mode = fuse.S_IFDIR
		}
		out = append(out, fuse.DirEntry{Name: name, Mode: mode})
	}

	if n.dirCache != nil {
		n.dirCache.Put(n.cnr.EncodeToString(), dirPrefix, out)
	}
	return out, 0
}

type fileNode struct {
	fs.Inode

	log *slog.Logger
	neo *neofs.Client
	cache *cache.Cache
	ro  bool

	cnr cid.ID
	obj oid.ID

	relPath  string
	fileSize uint64
	fileTime time.Time

	attrMu      sync.Mutex
	attrFetched time.Time
	attrTTL     time.Duration
	attrs       map[string]string
	attrErr     error
}

func (n *fileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Attr.Mode = fuse.S_IFREG | 0o444
	if !n.ro {
		out.Attr.Mode = fuse.S_IFREG | 0o644
	}
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())

	if n.fileSize == 0 && !n.obj.IsZero() {
		// Fetch real size from ObjectHead.
		if hdr, err := n.neo.ObjectHead(ctx, n.cnr, n.obj); err == nil {
			n.fileSize = hdr.PayloadSize()
			// Extract time if missing
			if n.fileTime.IsZero() {
				for _, a := range hdr.Attributes() {
					if a.Key() == "Timestamp" || a.Key() == "LastModified" {
						if t, err := time.Parse(time.RFC3339Nano, a.Value()); err == nil {
							n.fileTime = t
						} else if t, err := time.Parse(time.RFC3339, a.Value()); err == nil {
							n.fileTime = t
						} else if sec, err := strconv.ParseInt(a.Value(), 10, 64); err == nil {
							n.fileTime = time.Unix(sec, 0)
						}
						break
					}
				}
			}
		}
	}
	out.Attr.Size = n.fileSize
	if !n.fileTime.IsZero() {
		out.Attr.SetTimes(nil, &n.fileTime, &n.fileTime)
	}
	return 0
}

// Setattr accepts attribute changes (timestamps, mode, etc.) as a no-op.
// NeoFS objects are immutable, so we can't actually change these, but
// returning success lets touch, cp, editors, and other tools work.
func (n *fileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	out.Attr.Mode = fuse.S_IFREG | 0o444
	if !n.ro {
		out.Attr.Mode = fuse.S_IFREG | 0o644
	}
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	out.Attr.Size = n.fileSize
	return 0
}

func (n *fileNode) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	if n.obj.IsZero() {
		return 0, syscall.ENOENT
	}

	attrs, err := n.getAttrs(ctx)
	if err != nil {
		return 0, errno(err)
	}

	// Expose object attributes as: user.neofs.<Key>
	const prefix = "user.neofs."
	var names []byte
	for k := range attrs {
		if k == "" {
			continue
		}
		name := []byte(prefix + k)
		names = append(names, name...)
		names = append(names, 0)
	}

	if len(dest) == 0 {
		return uint32(len(names)), 0
	}
	if len(dest) < len(names) {
		return uint32(len(names)), syscall.ERANGE
	}
	copy(dest, names)
	return uint32(len(names)), 0
}

func (n *fileNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	const prefix = "user.neofs."
	if !strings.HasPrefix(attr, prefix) {
		return 0, syscall.ENODATA
	}
	if n.obj.IsZero() {
		return 0, syscall.ENOENT
	}

	key := strings.TrimPrefix(attr, prefix)
	if key == "" {
		return 0, syscall.ENODATA
	}

	attrs, err := n.getAttrs(ctx)
	if err != nil {
		return 0, errno(err)
	}
	val, ok := attrs[key]
	if !ok {
		return 0, syscall.ENODATA
	}

	b := []byte(val)
	if len(dest) == 0 {
		return uint32(len(b)), 0
	}
	if len(dest) < len(b) {
		return uint32(len(b)), syscall.ERANGE
	}
	copy(dest, b)
	return uint32(len(b)), 0
}

func (n *fileNode) getAttrs(ctx context.Context) (map[string]string, error) {
	n.attrMu.Lock()
	ttl := n.attrTTL
	if ttl == 0 {
		// Objects in NeoFS are immutable once written — their attributes never change.
		// Cache aggressively like goofys does for S3 objects.
		ttl = 5 * time.Minute
	}
	// If we already fetched recently, serve from cache (including cached errors).
	if n.attrs != nil && !n.attrFetched.IsZero() && time.Since(n.attrFetched) < ttl {
		defer n.attrMu.Unlock()
		if n.attrErr != nil {
			return nil, n.attrErr
		}
		return n.attrs, nil
	}
	// If error was cached recently and attrs are nil, also serve it.
	if n.attrErr != nil && !n.attrFetched.IsZero() && time.Since(n.attrFetched) < ttl && n.attrs == nil {
		defer n.attrMu.Unlock()
		return nil, n.attrErr
	}
	n.attrMu.Unlock()

	hdr, err := n.neo.ObjectHead(ctx, n.cnr, n.obj)
	n.attrMu.Lock()
	defer n.attrMu.Unlock()
	n.attrFetched = time.Now()
	n.attrErr = err
	if err != nil {
		return nil, err
	}

	m := make(map[string]string, 16)
	for _, a := range hdr.Attributes() {
		k := a.Key()
		if k == "" {
			continue
		}
		m[k] = a.Value()
	}
	n.attrs = m
	n.attrErr = nil
	return n.attrs, nil
}

func (n *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.cache == nil {
		return nil, 0, syscall.EIO
	}

	// Write path: upload-on-close.
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		if n.ro {
			return nil, 0, syscall.EROFS
		}

		tmpDir := n.cache.Dir()
		if tmpDir == "" {
			return nil, 0, syscall.EIO
		}
		f, err := os.CreateTemp(tmpDir, "neofs-upload-*.tmp")
		if err != nil {
			return nil, 0, syscall.EIO
		}

		// Seed temp file with existing contents unless truncating.
		if flags&syscall.O_TRUNC == 0 && !n.obj.IsZero() {
			key := cache.Key(n.cnr.EncodeToString(), n.obj.EncodeToString())
			srcPath, _, err := n.cache.GetOrFetch(ctx, key, func(ctx context.Context, w io.Writer) error {
				_, r, err := n.neo.ObjectGet(ctx, n.cnr, n.obj)
				if err != nil {
					return err
				}
				defer r.Close()
				_, err = io.Copy(w, r)
				return err
			})
			if err == nil {
				_ = copyFileToWriter(srcPath, f)
			}
		}

		h := &uploadFileHandle{
			log:     n.log,
			neo:     n.neo,
			cache:   n.cache,
			node:    n,
			tmpPath: f.Name(),
			f:       f,
			cnr:     n.cnr,
			relPath: n.relPath,
		}
		return h, 0, 0
	}

	// Read path.
	if n.obj.IsZero() {
		return nil, 0, syscall.ENOENT
	}

	// Large-file fast path: stream directly from NeoFS without downloading the
	// entire object first. This keeps Open() non-blocking for multi-GB files so
	// the file explorer doesn't hang while waiting for a full download.
	const streamThreshold = 64 << 20 // 64 MB
	if n.fileSize >= streamThreshold {
		_, r, err := n.neo.ObjectGet(ctx, n.cnr, n.obj)
		if err != nil {
			return nil, 0, errno(err)
		}
		return &streamingFileHandle{r: r, size: int64(n.fileSize)}, fuse.FOPEN_DIRECT_IO, 0
	}

	key := cache.Key(n.cnr.EncodeToString(), n.obj.EncodeToString())
	path, size, err := n.cache.GetOrFetch(ctx, key, func(ctx context.Context, w io.Writer) error {
		_, r, err := n.neo.ObjectGet(ctx, n.cnr, n.obj)
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(w, r)
		return err
	})
	if err != nil {
		return nil, 0, errno(err)
	}

	f, err2 := os.Open(path)
	if err2 != nil {
		return nil, 0, syscall.EIO
	}

	n.fileSize = uint64(size)
	return &cachedFileHandle{f: f}, 0, 0
}

type cachedFileHandle struct {
	f *os.File
}

var _ = (fs.FileHandle)((*cachedFileHandle)(nil))
var _ = (fs.FileReader)((*cachedFileHandle)(nil))
var _ = (fs.FileReleaser)((*cachedFileHandle)(nil))

func (h *cachedFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, err := h.f.ReadAt(dest, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *cachedFileHandle) Release(ctx context.Context) syscall.Errno {
	if h.f != nil {
		_ = h.f.Close()
	}
	return 0
}

// streamingFileHandle serves large-object reads directly from a NeoFS gRPC
// stream without buffering the entire file. FOPEN_DIRECT_IO is set on open
// so the kernel won't try random-access pread semantics.
//
// FUSE with DIRECT_IO issues sequential Read calls with incrementing offsets,
// so we only need to track the current position and discard skipped bytes.
type streamingFileHandle struct {
	mu   sync.Mutex
	r    io.ReadCloser
	pos  int64 // bytes consumed from r so far
	size int64
}

var _ = (fs.FileHandle)((*streamingFileHandle)(nil))
var _ = (fs.FileReader)((*streamingFileHandle)(nil))
var _ = (fs.FileReleaser)((*streamingFileHandle)(nil))

func (h *streamingFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.r == nil {
		return fuse.ReadResultData(nil), 0
	}

	// Discard bytes if the kernel skipped forward.
	if off > h.pos {
		if _, err := io.CopyN(io.Discard, h.r, off-h.pos); err != nil && !errors.Is(err, io.EOF) {
			return nil, syscall.EIO
		}
		h.pos = off
	}
	if off < h.pos {
		// Backward seek not supported on a stream — signal EOF.
		return fuse.ReadResultData(nil), 0
	}

	n, err := io.ReadFull(h.r, dest)
	h.pos += int64(n)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *streamingFileHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.r != nil {
		_ = h.r.Close()
		h.r = nil
	}
	return 0
}


type uploadFileHandle struct {
	log          *slog.Logger
	neo          *neofs.Client
	cache        *cache.Cache
	dirCache     *dirCache
	uploadTracker *uploads.Tracker
	node         *fileNode

	tmpPath string
	f       *os.File

	cnr     cid.ID
	relPath string
}

var _ = (fs.FileHandle)((*uploadFileHandle)(nil))
var _ = (fs.FileWriter)((*uploadFileHandle)(nil))
var _ = (fs.FileReleaser)((*uploadFileHandle)(nil))

// progressReader wraps an io.Reader, counting bytes read and updating the upload tracker entry.
type progressReader struct {
	r     io.Reader
	entry *uploads.Entry
	sent  atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.sent.Add(int64(n))
		if p.entry != nil {
			p.entry.AddSent(int64(n))
		}
	}
	return n, err
}

func (p *progressReader) Sent() int64 { return p.sent.Load() }

func (h *uploadFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if h.f == nil {
		return 0, syscall.EIO
	}
	n, err := h.f.WriteAt(data, off)
	if err != nil {
		return uint32(n), syscall.EIO
	}
	return uint32(n), 0
}

func (h *uploadFileHandle) Release(ctx context.Context) syscall.Errno {
	if h.f == nil {
		return 0
	}
	_ = h.f.Close()
	h.f = nil

	// Capture values for the background goroutine
	ctxBg := context.Background()
	cnr := h.cnr
	relPath := h.relPath
	tmpPath := h.tmpPath
	neo := h.neo
	cacheStore := h.cache
	node := h.node
	dirCache := h.dirCache
	uploadTracker := h.uploadTracker

	go func() {
		start := time.Now()
		fi, statErr := os.Stat(tmpPath)
		fileBytes := int64(0)
		if statErr == nil {
			fileBytes = fi.Size()
		}
		if h.log != nil {
			h.log.Info("[upload] starting", "path", relPath, "bytes", fileBytes)
		}

		// Register with tracker (if available).
		var trackerEntry *uploads.Entry
		if uploadTracker != nil {
			trackerEntry = uploadTracker.Register(relPath, fileBytes)
			defer uploadTracker.Finish(relPath)
		}

		defer os.Remove(tmpPath)

		parent := &containerNode{neo: neo, cnr: cnr}
		var oldIDs []oid.ID
		if ids, err := parent.findObjectsByExactPath(ctxBg, relPath); err == nil {
			oldIDs = ids
		}

		src, err := os.Open(tmpPath)
		if err != nil {
			if h.log != nil {
				h.log.Error("[upload] FAILED", "path", relPath, "err", err, "elapsed", time.Since(start).Round(time.Millisecond))
			}
			return
		}

		// Wrap with progress reader so the tracker and log ticker get byte counts.
		pr := &progressReader{r: src, entry: trackerEntry}

		// Log progress every 10 seconds.
		if h.log != nil && fileBytes > 0 {
			ticker := time.NewTicker(10 * time.Second)
			done := make(chan struct{})
			defer close(done)
			go func() {
				for {
					select {
					case <-ticker.C:
						sent := pr.Sent()
						pct := int(float64(sent) / float64(fileBytes) * 100)
						h.log.Info("[upload] progress",
							"path", relPath,
							"sent", fmt.Sprintf("%d/%d", sent, fileBytes),
							"pct", fmt.Sprintf("%d%%", pct),
							"elapsed", time.Since(start).Round(time.Second),
						)
					case <-done:
						ticker.Stop()
						return
					}
				}
			}()
		}

		newID, putErr := neo.ObjectPut(ctxBg, cnr, relPath, pr, "")
		_ = src.Close()

		if putErr != nil {
			if h.log != nil {
				h.log.Error("[upload] FAILED", "path", relPath, "bytes", fileBytes, "err", putErr, "elapsed", time.Since(start).Round(time.Millisecond))
			}
			return
		}

		for _, id := range oldIDs {
			_ = neo.ObjectDelete(ctxBg, cnr, id)
		}

		key := cache.Key(cnr.EncodeToString(), newID.EncodeToString())
		if _, size, err := cacheStore.StoreFromPath(key, tmpPath); err == nil {
			node.obj = newID
			node.fileSize = uint64(size)
		} else {
			node.obj = newID
		}

		neo.InvalidateContainerScan(cnr)
		if dirCache != nil {
			dirCache.InvalidateContainer(cnr.EncodeToString())
		}

		if h.log != nil {
			h.log.Info("[upload] ok", "path", relPath, "obj", newID.EncodeToString(), "bytes", fileBytes, "elapsed", time.Since(start).Round(time.Millisecond))
		}
	}()

	return 0
}

func copyFileToWriter(srcPath string, dst io.Writer) error {
	in, err := os.Open(filepath.Clean(srcPath))
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(dst, in)
	return err
}

func errno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	switch {
	case errors.Is(err, apistatus.ErrObjectNotFound),
		errors.Is(err, apistatus.ErrContainerNotFound):
		return syscall.ENOENT
	case errors.Is(err, apistatus.ErrObjectAccessDenied):
		return syscall.EACCES
	case errors.Is(err, apistatus.ErrObjectAlreadyRemoved):
		return syscall.ENOENT
	default:
		return syscall.EIO
	}
}

func joinRel(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "/" + seg
}

func cleanLeadingSlash(p string) string {
	return strings.TrimPrefix(p, "/")
}

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

