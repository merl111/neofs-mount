//go:build darwin && cgo

// Package main builds to a C static archive for the macOS File Provider extension.
//   cd macos/GoBridge && go build -buildmode=c-archive -o libneofsfp.a .
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
	"unsafe"

	"log/slog"

	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"

	"github.com/mathias/neofs-mount/internal/neofs"
)

var (
	fpMu      sync.Mutex
	fpClient  *neofs.Client
	fpLog     *slog.Logger
	fpTempDir string
)

func init() {
	fpLog = slog.Default()
}

var logSetupOnce sync.Once

func setupLogFile(dir string) {
	logSetupOnce.Do(func() {
		logPath := dir + "/neofs-fp.log"
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		fpLog = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
		fpLog.Info("Go bridge log initialized", "pid", fmt.Sprint(os.Getpid()))
	})
}

//export NeoFsFpVersion
func NeoFsFpVersion() C.int {
	return 4
}

// NeoFsFpInit connects to NeoFS (endpoint + wallet key path). Returns 0 on success; negative on error.
//
//export NeoFsFpInit
func NeoFsFpInit(endpoint *C.char, walletKey *C.char) C.int {
	if endpoint == nil || walletKey == nil {
		return -1
	}
	ep := C.GoString(endpoint)
	wk := C.GoString(walletKey)
	if ep == "" || wk == "" {
		return -2
	}

	fpMu.Lock()
	defer fpMu.Unlock()
	if fpClient != nil {
		_ = fpClient.Close()
		fpClient = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := neofs.New(ctx, neofs.Params{
		Logger:    fpLog,
		Endpoint:  ep,
		WalletKey: wk,
	})
	if err != nil {
		return -3
	}
	fpClient = c
	return 0
}

//export NeoFsFpShutdown
func NeoFsFpShutdown() {
	fpMu.Lock()
	defer fpMu.Unlock()
	if fpClient != nil {
		_ = fpClient.Close()
		fpClient = nil
	}
}

// NeoFsFpEnsureClient initialises the NeoFS client from a config file if not
// already connected. configPath is the absolute path to config.toml (the caller
// in Swift should resolve this via FileManager.containerURL).
// Returns 0 if the client is ready, negative on error.
//
//export NeoFsFpEnsureClient
func NeoFsFpEnsureClient(configPath *C.char) C.int {
	cp := C.GoString(configPath)
	if cp == "" {
		return -1
	}

	// Set up file-based logging in the same directory as config.toml
	// (App Group container, writable by the sandboxed extension).
	dir := cp
	if idx := lastSlash(cp); idx >= 0 {
		dir = cp[:idx]
	}
	setupLogFile(dir)

	fpMu.Lock()
	if fpClient != nil {
		fpMu.Unlock()
		return 0
	}
	fpMu.Unlock()

	b, err := os.ReadFile(cp)
	if err != nil {
		fpLog.Error("EnsureClient: ReadFile failed", "path", cp, "err", err)
		return -5
	}

	ep := parseTomlValue(string(b), "endpoint")
	wk := parseTomlValue(string(b), "wallet_key")
	if ep == "" || wk == "" {
		return -2
	}

	cep := C.CString(ep)
	cwk := C.CString(wk)
	defer C.free(unsafe.Pointer(cep))
	defer C.free(unsafe.Pointer(cwk))
	return NeoFsFpInit(cep, cwk)
}

type containerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NeoFsFpListContainers returns a JSON array of {id, name} objects.
// Caller must free() the returned string. Returns NULL on error.
//
//export NeoFsFpListContainers
func NeoFsFpListContainers() *C.char {
	fpMu.Lock()
	c := fpClient
	fpMu.Unlock()
	if c == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids, err := c.ListContainers(ctx)
	if err != nil {
		fpLog.Error("ListContainers", "err", err)
		return nil
	}

	result := make([]containerInfo, 0, len(ids))
	for _, id := range ids {
		name := id.EncodeToString()
		cnr, err := c.ContainerGet(ctx, id)
		if err == nil {
			if n := cnr.Name(); n != "" {
				name = n
			}
		}
		result = append(result, containerInfo{
			ID:   id.EncodeToString(),
			Name: name,
		})
	}

	j, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return C.CString(string(j))
}

type dirEntryJSON struct {
	Name        string `json:"name"`
	ObjectID    string `json:"objectID,omitempty"`
	Size        int64  `json:"size"`
	IsDirectory bool   `json:"isDirectory"`
}

// NeoFsFpListEntries returns a JSON array of immediate children under prefix
// within the given container. Caller must free() the returned string.
// Returns NULL on error.
//
//export NeoFsFpListEntries
func NeoFsFpListEntries(containerID *C.char, prefix *C.char) *C.char {
	fpMu.Lock()
	c := fpClient
	fpMu.Unlock()
	if c == nil {
		return nil
	}

	cidStr := C.GoString(containerID)
	pfx := ""
	if prefix != nil {
		pfx = C.GoString(prefix)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entries, err := c.ListEntriesByPrefix(ctx, cidStr, pfx)
	if err != nil {
		fpLog.Error("ListEntriesByPrefix", "container", cidStr, "prefix", pfx, "err", err)
		return nil
	}

	result := make([]dirEntryJSON, 0, len(entries))
	for _, e := range entries {
		result = append(result, dirEntryJSON{
			Name:        e.Name,
			ObjectID:    e.ObjectID,
			Size:        e.Size,
			IsDirectory: e.IsDirectory,
		})
	}

	j, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return C.CString(string(j))
}

//export NeoFsFpSetTempDir
func NeoFsFpSetTempDir(dir *C.char) {
	fpTempDir = C.GoString(dir)
}

// NeoFsFpFetchObject downloads an object to a temporary file and returns the
// file path. Caller must free() the returned string. Returns NULL on error.
//
//export NeoFsFpFetchObject
func NeoFsFpFetchObject(containerID *C.char, objectID *C.char) *C.char {
	fpMu.Lock()
	c := fpClient
	fpMu.Unlock()
	if c == nil {
		fpLog.Error("FetchObject: client is nil")
		return nil
	}

	cidStr := C.GoString(containerID)
	oidStr := C.GoString(objectID)
	fpLog.Info("FetchObject: start", "cid", cidStr, "oid", oidStr)

	var cnr cid.ID
	if err := cnr.DecodeString(cidStr); err != nil {
		fpLog.Error("FetchObject: bad container ID", "cid", cidStr, "err", err)
		return nil
	}
	var obj oid.ID
	if err := obj.DecodeString(oidStr); err != nil {
		fpLog.Error("FetchObject: bad object ID", "oid", oidStr, "err", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fpLog.Info("FetchObject: calling ObjectGet")
	_, reader, err := c.ObjectGet(ctx, cnr, obj)
	if err != nil {
		fpLog.Error("FetchObject: ObjectGet failed", "cid", cidStr, "oid", oidStr, "err", err)
		return nil
	}
	defer reader.Close()

	tmp, err := os.CreateTemp(fpTempDir, "neofs-fp-*")
	if err != nil {
		fpLog.Error("FetchObject: CreateTemp failed", "err", err)
		return nil
	}
	fpLog.Info("FetchObject: writing to temp file", "path", tmp.Name())
	n, copyErr := io.Copy(tmp, reader)
	tmp.Close()
	if copyErr != nil {
		fpLog.Warn("FetchObject: io.Copy error", "err", copyErr, "bytesWritten", n)
		if n == 0 {
			os.Remove(tmp.Name())
			fpLog.Error("FetchObject: no data written, removing temp file")
			return nil
		}
	}
	fpLog.Info("FetchObject: done", "path", tmp.Name(), "bytes", n)

	return C.CString(tmp.Name())
}

// NeoFsFpDeleteObject deletes a single object from a container.
// Returns 0 on success; negative on error.
//
//export NeoFsFpDeleteObject
func NeoFsFpDeleteObject(containerID *C.char, objectID *C.char) C.int {
	fpMu.Lock()
	c := fpClient
	fpMu.Unlock()
	if c == nil {
		fpLog.Error("DeleteObject: client is nil")
		return -1
	}

	cidStr := C.GoString(containerID)
	oidStr := C.GoString(objectID)
	fpLog.Info("DeleteObject: start", "cid", cidStr, "oid", oidStr)

	var cnr cid.ID
	if err := cnr.DecodeString(cidStr); err != nil {
		fpLog.Error("DeleteObject: bad container ID", "cid", cidStr, "err", err)
		return -2
	}
	var obj oid.ID
	if err := obj.DecodeString(oidStr); err != nil {
		fpLog.Error("DeleteObject: bad object ID", "oid", oidStr, "err", err)
		return -3
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.ObjectDelete(ctx, cnr, obj); err != nil {
		fpLog.Error("DeleteObject: ObjectDelete failed", "cid", cidStr, "oid", oidStr, "err", err)
		return -4
	}

	fpLog.Info("DeleteObject: done", "cid", cidStr, "oid", oidStr)
	return 0
}

// parseTomlValue is a minimal parser for key = "value" lines.
func parseTomlValue(toml, key string) string {
	for _, line := range splitLines(toml) {
		trimmed := trimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		prefix := key + " = "
		if !hasPrefix(trimmed, prefix) {
			prefix = key + "= "
			if !hasPrefix(trimmed, prefix) {
				prefix = key + " ="
				if !hasPrefix(trimmed, prefix) {
					continue
				}
			}
		}
		rest := trimSpace(trimmed[len(prefix):])
		if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
			return rest[1 : len(rest)-1]
		}
		if len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\'' {
			return rest[1 : len(rest)-1]
		}
		return rest
	}
	return ""
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func main() {}
