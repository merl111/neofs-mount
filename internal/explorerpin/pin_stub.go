//go:build !windows

package explorerpin

// Register pins the mount folder under This PC in Explorer (Windows only).
func Register(_, _, _ string, _ []byte) error { return nil }

// Unregister removes the Explorer entry.
func Unregister() error { return nil }

// RegisterFileAttrsShellVerb is a no-op on non-Windows builds.
func RegisterFileAttrsShellVerb(_ string) {}

// RegisterNeoFSContextMenuVerbs is a no-op on non-Windows builds.
func RegisterNeoFSContextMenuVerbs(_ string) {}

// UnregisterNeoFSContextMenuVerbs is a no-op on non-Windows builds.
func UnregisterNeoFSContextMenuVerbs() {}
