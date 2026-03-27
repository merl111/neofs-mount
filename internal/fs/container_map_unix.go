//go:build linux || darwin

package fs

import (
	"path/filepath"
	"strings"
)

// MountDiskToNeoUploadWithUI maps an absolute disk path under mountRoot to container ID and NeoFS object path.
func MountDiskToNeoUploadWithUI(mountRoot, absPath string, uiNameToCID map[string]string) (containerID, neoRelPath string, ok bool) {
	mountRoot = filepath.Clean(mountRoot)
	absPath = filepath.Clean(absPath)
	rel, err := filepath.Rel(mountRoot, absPath)
	if err != nil {
		return "", "", false
	}
	if rel == "." {
		return "", "", false
	}
	if strings.HasPrefix(rel, "..") {
		return "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	var segs []string
	for _, p := range parts {
		if p != "" {
			segs = append(segs, p)
		}
	}
	if len(segs) < 2 {
		return "", "", false
	}
	cidStr, ok := ResolveContainerSegment(segs[0], uiNameToCID)
	if !ok {
		return "", "", false
	}
	return cidStr, strings.Join(segs[1:], "/"), true
}
