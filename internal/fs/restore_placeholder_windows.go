//go:build windows

package fs

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/mathias/neofs-mount/internal/cfapi"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
	"github.com/nspcc-dev/neofs-sdk-go/object"
	"golang.org/x/sys/windows"
)

func isNeoFSDirectoryMarker(hdr *object.Object) bool {
	if hdr == nil {
		return false
	}
	for _, a := range hdr.Attributes() {
		if a.Key() == object.AttributeContentType && a.Value() == "application/x-directory" {
			return true
		}
	}
	return false
}

// tryRestorePlaceholderAfterRemove recreates a cloud placeholder when the user removed or moved away
// the local file but NeoFS still has an object at that path, so Explorer keeps showing the entry.
func (p *neofsProvider) tryRestorePlaceholderAfterRemove(removedPath string) {
	if p.ro {
		return
	}
	removedPath = filepath.Clean(removedPath)
	if isRecycleBinPath(removedPath) {
		return
	}
	if p.wasRecentlyDeleted(removedPath) {
		return
	}
	// Something exists at this path again (e.g. racing create).
	if _, err := os.Stat(removedPath); err == nil {
		return
	}

	containerIDStr, neoRel, ok := p.mountDiskToNeo(removedPath)
	if !ok {
		return
	}

	var cnr cid.ID
	if err := cnr.DecodeString(containerIDStr); err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids, err := p.neo.FindObjectIDsByExactPath(ctx, cnr, neoRel)
	if err != nil || len(ids) == 0 {
		return
	}

	// Listing comes from a TTL-backed scan cache; another process may have deleted the object
	// (e.g. tray "Delete from NeoFS container"). Only restore after a live ObjectHead succeeds.
	var hdr *object.Object
	var chosen oid.ID
	for _, id := range ids {
		h, herr := p.neo.ObjectHead(ctx, cnr, id)
		if herr != nil {
			continue
		}
		hdr = h
		chosen = id
		break
	}
	if hdr == nil {
		p.neo.InvalidateContainerScan(cnr)
		return
	}

	parentDir := filepath.Dir(removedPath)
	baseName := filepath.Base(removedPath)
	if baseName == "." || baseName == ".." || baseName == "/" {
		return
	}

	isDir := isNeoFSDirectoryMarker(hdr)
	size := int64(hdr.PayloadSize())
	if isDir {
		size = 0
	}

	neoSlash := filepath.ToSlash(neoRel)
	var ident []byte
	if isDir {
		ident = cfapi.IdentityFromString(containerIDStr + ":dir:" + neoSlash)
	} else {
		ident = cfapi.IdentityFromString(containerIDStr + ":" + chosen.EncodeToString())
	}

	attrs := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if isDir {
		attrs = windows.FILE_ATTRIBUTE_DIRECTORY
	}

	if err := cfapi.RestoreLocalPlaceholder(parentDir, baseName, size, attrs, ident); err != nil {
		p.log.Debug("cfapi: restore placeholder", "path", removedPath, "err", err)
		return
	}

	p.neo.InvalidateContainerScan(cnr)
	p.auditRecord("placeholder_restored_after_local_remove", map[string]any{
		"path": removedPath, "container_id": containerIDStr,
		"object_path": neoSlash, "directory": isDir,
	})
	p.log.Info("cfapi: restored NeoFS placeholder after local remove", "path", removedPath)
}
