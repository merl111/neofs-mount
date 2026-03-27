//go:build windows

package fs

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mathias/neofs-mount/internal/neofs"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
)

// ContainerUIEntry is one container row for Explorer (friendly folder name + real CID string).
type ContainerUIEntry struct {
	DisplayName string
	CIDStr      string
}

// SanitizeContainerDirName maps a NeoFS container name to a safe single path segment.
// Characters that are illegal in Windows file names are replaced with underscores.
func SanitizeContainerDirName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	s = strings.ReplaceAll(s, "\x00", "")
	for _, c := range []string{`\`, `/`, `:`, `*`, `?`, `"`, `<`, `>`, `|`} {
		s = strings.ReplaceAll(s, c, "_")
	}
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}

// ListContainersForUI returns sorted Explorer folder names and a map displayName → container ID string.
// Logic matches the FUSE root listing (friendly name from ContainerGet, collision fallback to CID).
func ListContainersForUI(ctx context.Context, neo *neofs.Client, ignore map[string]struct{}) ([]ContainerUIEntry, map[string]string, error) {
	containers, err := neo.ListContainers(ctx)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(containers, func(i, j int) bool {
		return containers[i].EncodeToString() < containers[j].EncodeToString()
	})

	type named struct {
		id   cid.ID
		name string
	}
	var namedList []named
	for _, id := range containers {
		idStr := id.EncodeToString()
		if ignore != nil {
			if _, ign := ignore[idStr]; ign {
				continue
			}
		}
		ui := idStr
		if cnr, err := neo.ContainerGet(ctx, id); err == nil {
			if nm := SanitizeContainerDirName(cnr.Name()); nm != "" {
				ui = nm
			}
		}
		if ui == "" {
			ui = idStr
		}
		namedList = append(namedList, named{id: id, name: ui})
	}

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

	entries := make([]ContainerUIEntry, 0, len(namedList))
	alias := make(map[string]string, len(namedList))
	for _, it := range namedList {
		idStr := it.id.EncodeToString()
		entries = append(entries, ContainerUIEntry{DisplayName: it.name, CIDStr: idStr})
		alias[it.name] = idStr
	}
	return entries, alias, nil
}

// ResolveContainerSegment maps the first path segment under the mount to a container ID string.
// seg may be a raw CID or a display folder name when uiNameToCID is non-nil.
func ResolveContainerSegment(seg string, uiNameToCID map[string]string) (cidStr string, ok bool) {
	var id cid.ID
	if err := id.DecodeString(seg); err == nil {
		return seg, true
	}
	if uiNameToCID == nil {
		return "", false
	}
	if idStr, hit := uiNameToCID[seg]; hit {
		return idStr, true
	}
	for k, v := range uiNameToCID {
		if strings.EqualFold(k, seg) {
			return v, true
		}
	}
	return "", false
}

// MountDiskToNeoUploadWithUI is like MountDiskToNeoUpload but resolves the container segment
// using display names from ListContainersForUI when uiNameToCID is non-nil.
func MountDiskToNeoUploadWithUI(mountRoot, absPath string, uiNameToCID map[string]string) (containerID, neoRelPath string, ok bool) {
	mountRoot = filepath.Clean(mountRoot)
	absPath = filepath.Clean(absPath)
	mStrip := stripDriveLetter(mountRoot)
	pStrip := stripDriveLetter(absPath)
	if !strings.HasPrefix(strings.ToLower(pStrip), strings.ToLower(mStrip)) {
		return "", "", false
	}
	rel := strings.TrimLeft(pStrip[len(mStrip):], `\/`)
	parts := splitPath(rel)
	if len(parts) < 2 {
		return "", "", false
	}
	cidStr, ok := ResolveContainerSegment(parts[0], uiNameToCID)
	if !ok {
		return "", "", false
	}
	return cidStr, strings.Join(parts[1:], "/"), true
}
