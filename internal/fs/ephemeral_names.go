package fs

import "strings"

// isEphemeralEditorHiddenName reports whether a single path segment is a GTK/GLib
// atomic-save temporary file name. These should not be uploaded to NeoFS and are
// hidden from FUSE listings so they do not clutter containers when rename-on-close races.
func isEphemeralEditorHiddenName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".goutputstream-") {
		return true
	}
	if strings.HasPrefix(name, ".gsavefile-") {
		return true
	}
	return false
}
