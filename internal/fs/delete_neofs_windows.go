//go:build windows

package fs

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mathias/neofs-mount/internal/neofs"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
)

// IgnoreSetFromIDs builds the ignore map used by ListContainersForUI.
func IgnoreSetFromIDs(ids []string) map[string]struct{} {
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

// DeleteNeoFSAtMountPath removes all NeoFS objects whose FilePath/Key matches neoRelPath or lives under it
// (prefix). absTarget must be under mountRoot; container folder names are resolved with uiNameToCID.
func DeleteNeoFSAtMountPath(ctx context.Context, neo *neofs.Client, mountRoot, absTarget string, ignore map[string]struct{}) (deleted int, cnrStr, neoRel string, err error) {
	_, alias, err := ListContainersForUI(ctx, neo, ignore)
	if err != nil {
		return 0, "", "", err
	}
	cnrStr, neoRel, ok := MountDiskToNeoUploadWithUI(mountRoot, absTarget, alias)
	if !ok {
		return 0, "", "", fmt.Errorf("path is not under the mount or does not map to a container object path")
	}
	var cnr cid.ID
	if err := cnr.DecodeString(cnrStr); err != nil {
		return 0, "", "", err
	}
	n, err := deleteNeoFSObjectsUnderPrefix(ctx, neo, cnr, neoRel)
	return n, cnrStr, neoRel, err
}

func deleteNeoFSObjectsUnderPrefix(ctx context.Context, neo *neofs.Client, cnr cid.ID, neoRelPath string) (int, error) {
	prefix := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(neoRelPath), "/"))
	entries, _, err := neo.ListEntriesByHeadScan(ctx, cnr)
	if err != nil {
		return 0, err
	}
	type hit struct {
		id   oid.ID
		path string
	}
	var hits []hit
	seen := map[string]struct{}{}
	for _, e := range entries {
		for _, raw := range []string{e.FilePath, e.Key} {
			p := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(raw), "/"))
			if p == "" {
				continue
			}
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				key := e.ObjectID.EncodeToString()
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				hits = append(hits, hit{id: e.ObjectID, path: p})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return len(hits[i].path) > len(hits[j].path) })
	n := 0
	for _, h := range hits {
		if err := neo.ObjectDelete(ctx, cnr, h.id); err != nil {
			return n, fmt.Errorf("delete object at %q: %w", h.path, err)
		}
		n++
	}
	neo.InvalidateContainerScan(cnr)
	return n, nil
}
