//go:build windows

package fs

import (
	"path/filepath"
	"strings"
)

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
