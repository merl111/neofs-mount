//go:build !windows && !linux

package explorerpin

// RegisterFileAttrsShellVerb is a no-op on this platform.
func RegisterFileAttrsShellVerb(_ string) {}

// RegisterNeoFSContextMenuVerbs is a no-op outside Windows / Linux.
func RegisterNeoFSContextMenuVerbs(_ string) {}

// UnregisterNeoFSContextMenuVerbs is a no-op outside Windows / Linux.
func UnregisterNeoFSContextMenuVerbs() {}
