//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"

	"github.com/mathias/neofs-mount/internal/fs"
)

func TestMount_ListAndRW(t *testing.T) {
	// FUSE mount is Linux-only; macOS uses File Provider (native app), not this test path.
	if runtime.GOOS != "linux" {
		t.Skip("FUSE integration mount runs on Linux only")
	}

	endpoint := os.Getenv("NEOFS_ENDPOINT")
	walletKey := os.Getenv("NEOFS_WALLET_KEY")
	containerStr := os.Getenv("NEOFS_TEST_CONTAINER_ID")
	if endpoint == "" || walletKey == "" || containerStr == "" {
		t.Skip("set NEOFS_ENDPOINT, NEOFS_WALLET_KEY, NEOFS_TEST_CONTAINER_ID to run")
	}

	var containerID cid.ID
	if err := containerID.DecodeString(containerStr); err != nil {
		t.Fatalf("invalid NEOFS_TEST_CONTAINER_ID: %v", err)
	}

	tmp := t.TempDir()
	mountpoint := filepath.Join(tmp, "mnt")
	cacheDir := filepath.Join(tmp, "cache")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mnt, err := fs.Mount(fs.MountParams{
		Endpoint:   endpoint,
		WalletKey:  walletKey,
		Mountpoint: mountpoint,
		ReadOnly:   false,
		CacheDir:   cacheDir,
		CacheSize:  256 << 20,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	defer mnt.Shutdown(context.Background())

	// Give the mount a moment to become ready.
	time.Sleep(500 * time.Millisecond)

	// Verify container directory exists.
	containerDir := filepath.Join(mountpoint, containerID.EncodeToString())
	if _, err := os.Stat(containerDir); err != nil {
		t.Fatalf("stat container dir: %v", err)
	}

	// Write a file.
	testPath := filepath.Join(containerDir, "neofs-mount-integration.txt")
	payload := []byte("hello from integration test\n")
	if err := os.WriteFile(testPath, payload, 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	// Read it back.
	got, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("readfile: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch: got=%q want=%q", string(got), string(payload))
	}

	// Delete it.
	if err := os.Remove(testPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

