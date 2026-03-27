//go:build windows

package fs

// MountDiskToNeoUpload returns the container ID and NeoFS object path when the first segment
// is a valid container ID string. Use MountDiskToNeoUploadWithUI if Explorer shows friendly names.
func MountDiskToNeoUpload(mountRoot, absPath string) (containerID, neoRelPath string, ok bool) {
	return MountDiskToNeoUploadWithUI(mountRoot, absPath, nil)
}
