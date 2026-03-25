// Package cfapi_test provides integration tests for the Windows Cloud Files API bindings.
// These tests will be auto-skipped on non-Windows platforms.
package cfapi_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// requireWindows skips the test if not running on Windows.
func requireWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skipf("skipping Windows-only test on %s", runtime.GOOS)
	}
}

// TestRegisterUnregisterSyncRoot exercises the round-trip of RegisterSyncRoot then UnregisterSyncRoot.
// This requires:
//   - Windows 10 1709+ (build 16299+)
//   - The Windows 10 SDK with cldapi.lib
//   - Running as a normal user (no admin required for user-space CfApi)
func TestRegisterUnregisterSyncRoot(t *testing.T) {
	requireWindows(t)

	// Use a temp directory as the sync root.
	root := t.TempDir()

	// Import the cfapi package only when running on Windows; the build tag
	// ensures this file compiles on all platforms but the test skips early.
	// The actual cfapi import lives in the windows-only build via init below.
	t.Logf("sync root: %s", root)

	// Register.
	if err := register(root, "neoFS Mount Test", "0.1"); err != nil {
		// HRESULT 0x80070005 = E_ACCESSDENIED (common if root already registered).
		t.Fatalf("RegisterSyncRoot: %v", err)
	}

	// Unregister — must not fail.
	if err := unregister(root); err != nil {
		t.Fatalf("UnregisterSyncRoot: %v", err)
	}
}

// TestConnectDisconnect verifies the basic connection lifecycle.
func TestConnectDisconnect(t *testing.T) {
	requireWindows(t)

	root := t.TempDir()

	if err := register(root, "neoFS Mount Test", "0.1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = unregister(root) })

	sess, err := connectNoop(root)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := sess.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
}

// TestCreatePlaceholders verifies that placeholder files are created on disk.
func TestCreatePlaceholders(t *testing.T) {
	requireWindows(t)

	root := t.TempDir()
	if err := register(root, "neoFS Mount Test", "0.1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = unregister(root) })

	sess, err := connectNoop(root)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = sess.Disconnect() }()

	placeholders := []placeholderDef{
		{Name: "file1.txt", IsDirectory: false, Size: 1024, FileIdentity: []byte("container1:obj1")},
		{Name: "subdir",    IsDirectory: true,  Size: 0,    FileIdentity: []byte("container:subdir")},
	}

	if err := createPlaceholders(sess, root, placeholders); err != nil {
		t.Fatalf("CreatePlaceholders: %v", err)
	}

	// Verify the files/dirs appear on disk.
	file1 := filepath.Join(root, "file1.txt")
	if _, err := os.Stat(file1); err != nil {
		t.Errorf("file1.txt not found: %v", err)
	}
	subdir := filepath.Join(root, "subdir")
	info, err := os.Stat(subdir)
	if err != nil {
		t.Errorf("subdir not found: %v", err)
	} else if !info.IsDir() {
		t.Errorf("subdir is not a directory")
	}
}

// TestSplitPath exercises the path splitting helper used by the Windows adapter.
func TestSplitPath(t *testing.T) {
	cases := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{".", nil},
		{"containerA", []string{"containerA"}},
		{`containerA\subdir\file.txt`, []string{"containerA", "subdir", "file.txt"}},
		{`/containerA/subdir`, []string{"containerA", "subdir"}},
	}

	for _, tc := range cases {
		got := splitPathForTest(tc.input)
		if !equal(got, tc.expected) {
			t.Errorf("splitPath(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
