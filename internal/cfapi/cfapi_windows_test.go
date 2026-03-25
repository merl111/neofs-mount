//go:build windows

// Bridge file: exposes internal cfapi symbols to the test suite on Windows.
package cfapi_test

import (
	"log/slog"

	"github.com/mathias/neofs-mount/internal/cfapi"
	"path/filepath"
)

type placeholderDef = cfapi.Placeholder

func register(root, name, version string) error {
	return cfapi.RegisterSyncRoot(root, name, version)
}

func unregister(root string) error {
	return cfapi.UnregisterSyncRoot(root)
}

// noopProvider satisfies cfapi.Provider without doing anything.
type noopProvider struct{}

func (n *noopProvider) FetchPlaceholders(req cfapi.FetchPlaceholdersRequest) error { return nil }
func (n *noopProvider) FetchData(req cfapi.FetchDataRequest, sess *cfapi.TransferSession) error {
	return nil
}

func connectNoop(root string) (*cfapi.Session, error) {
	return cfapi.Connect(root, &noopProvider{}, slog.Default())
}

func createPlaceholders(sess *cfapi.Session, root string, defs []placeholderDef) error {
	return sess.CreatePlaceholders(root, defs)
}

func splitPathForTest(p string) []string {
	// Mirror the logic from mount_windows.go.
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == "/" || clean == "." || clean == "" {
		return nil
	}
	var parts []string
	cur := clean
	for cur != "" && cur != "." && cur != "/" {
		dir, base := filepath.Split(cur)
		if base != "" {
			parts = append([]string{base}, parts...)
		}
		cur = filepath.Clean(dir)
	}
	return parts
}
