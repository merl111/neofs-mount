//go:build windows

// Package cfapi provides Go bindings for the Windows Cloud Files API (CfApi).
// This allows neofs-mount to register as a Cloud Sync Provider, making NeoFS
// containers appear as a native folder in Windows Explorer without any third-party
// drivers.
//
// Requirements:
//   - Windows 10 version 1709 (Build 16299) or later
//   - cldapi.lib / cfgmgr32.lib must be available to the linker
//
// The build tag "windows" ensures this file is only compiled on Windows.

package cfapi

/*
#cgo LDFLAGS: -lcldapi -lole32 -lrpcrt4
#include "cfapi_windows.h"

// ─── C-to-Go thunk exports ────────────────────────────────────────────────
// CGo doesn't let Go functions be used directly as C function pointers.
// We define thin C trampoline functions that call back into Go via exported
// Go symbols.

extern void goFetchData(CONST CF_CALLBACK_INFO*, CONST CF_CALLBACK_PARAMETERS*);
extern void goFetchPlaceholders(CONST CF_CALLBACK_INFO*, CONST CF_CALLBACK_PARAMETERS*);

static void c_fetch_data_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goFetchData(i, p);
}

static void c_fetch_placeholders_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goFetchPlaceholders(i, p);
}

static HRESULT cfapi_connect(LPCWSTR path, LPVOID ctx, CF_CONNECTION_KEY* key) {
    CF_CALLBACK_REGISTRATION table[] = {
        { CF_CALLBACK_TYPE_FETCH_DATA,         c_fetch_data_cb         },
        { CF_CALLBACK_TYPE_FETCH_PLACEHOLDERS, c_fetch_placeholders_cb },
        CF_CALLBACK_REGISTRATION_END
    };
    return CfConnectSyncRoot(path, table, ctx, 0, key);
}

static HRESULT cfapi_transfer_data(
    CF_CONNECTION_KEY connKey,
    CF_TRANSFER_KEY   txKey,
    LPCVOID buf, LONGLONG offset, LONGLONG length,
    NTSTATUS status)
{
    CF_OPERATION_INFO opInfo = {0};
    opInfo.StructSize    = sizeof(opInfo);
    opInfo.Type          = CF_OPERATION_TYPE_TRANSFER_DATA;
    opInfo.ConnectionKey = connKey;
    opInfo.TransferKey   = txKey;

    CF_OPERATION_PARAMETERS opParams = {0};
    opParams.ParamSize                      = sizeof(opParams);
    opParams.TransferData.Flags             = 0;
    opParams.TransferData.CompletionStatus  = status;
    opParams.TransferData.Buffer            = buf;
    opParams.TransferData.Offset.QuadPart   = offset;
    opParams.TransferData.Length.QuadPart   = length;

    return CfExecute(&opInfo, &opParams);
}
*/
import "C"

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// Public API types
// ---------------------------------------------------------------------------

// FetchDataRequest is received when Windows requests bytes of a placeholder file.
type FetchDataRequest struct {
	ConnectionKey int64
	TransferKey   int64
	NormalizedPath string    // full path relative to sync root
	FileIdentity   []byte    // opaque ID we stored during CreatePlaceholders
	RequiredOffset int64
	RequiredLength int64
}

// FetchPlaceholdersRequest is received when Windows opens a directory.
type FetchPlaceholdersRequest struct {
	ConnectionKey  int64
	TransferKey    int64
	NormalizedPath string
}

// Placeholder describes a file or directory to be created in the sync root.
type Placeholder struct {
	Name         string
	IsDirectory  bool
	Size         int64       // 0 for directories
	FileIdentity []byte      // opaque blob stored by Windows, returned on fetch
}

// Provider is the callback interface callers must implement.
type Provider interface {
	// FetchPlaceholders is called when Windows opens a directory.
	// The implementation should call sess.CreatePlaceholders with the children.
	FetchPlaceholders(req FetchPlaceholdersRequest) error

	// FetchData is called when Windows needs to read bytes from a file.
	// The implementation must call sess.TransferData repeatedly until the
	// required range is fully covered, then call sess.TransferData with a zero-
	// length buffer and StatusOK to signal completion.
	FetchData(req FetchDataRequest, sess *TransferSession) error
}

// ---------------------------------------------------------------------------
// Session (connection) lifecycle
// ---------------------------------------------------------------------------

// Session holds an active CfApi connection.
type Session struct {
	log     *slog.Logger
	connKey C.CF_CONNECTION_KEY
	root    string
	prov    Provider
}

// global registry so C callbacks can look up the Session by connection key.
var (
	sessionsMu sync.Mutex
	sessions   = map[int64]*Session{}
)

func registerSession(s *Session) {
	sessionsMu.Lock()
	sessions[int64(s.connKey)] = s
	sessionsMu.Unlock()
}

func unregisterSession(s *Session) {
	sessionsMu.Lock()
	delete(sessions, int64(s.connKey))
	sessionsMu.Unlock()
}

func sessionForKey(k int64) *Session {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	return sessions[k]
}

// RegisterSyncRoot registers the folder at syncRootPath as a Cloud Sync Root.
// providerName and providerVersion appear in Windows Settings → Apps → Connected Sync Providers.
// This only needs to be called once per machine; subsequent launches just call Connect.
func RegisterSyncRoot(syncRootPath, providerName, providerVersion string) error {
	rootW, err := windows.UTF16PtrFromString(syncRootPath)
	if err != nil {
		return fmt.Errorf("cfapi: UTF16 sync root path: %w", err)
	}
	nameW, err := windows.UTF16PtrFromString(providerName)
	if err != nil {
		return fmt.Errorf("cfapi: UTF16 provider name: %w", err)
	}
	verW, err := windows.UTF16PtrFromString(providerVersion)
	if err != nil {
		return fmt.Errorf("cfapi: UTF16 provider version: %w", err)
	}

	// Fixed GUID for neoFS-mount cloud provider:
	// {9f3a1c2e-4b5d-6e7f-8a9b-0c1d2e3f4a5b}
	providerID := C.GUID{
		Data1: 0x9f3a1c2e,
		Data2: 0x4b5d,
		Data3: 0x6e7f,
		Data4: [8]C.uchar{0x8a, 0x9b, 0x0c, 0x1d, 0x2e, 0x3f, 0x4a, 0x5b},
	}

	reg := C.CF_SYNC_REGISTRATION{
		StructSize:      C.DWORD(unsafe.Sizeof(C.CF_SYNC_REGISTRATION{})),
		ProviderName:    (*C.WCHAR)(unsafe.Pointer(nameW)),
		ProviderVersion: (*C.WCHAR)(unsafe.Pointer(verW)),
		ProviderId:      providerID,
	}

	policies := C.CF_SYNC_POLICIES{
		StructSize: C.DWORD(unsafe.Sizeof(C.CF_SYNC_POLICIES{})),
		Hydration: C.CF_HYDRATION_POLICY{
			Primary: C.CF_HYDRATION_POLICY_PROGRESSIVE, // stream on demand
		},
		Population: C.CF_POPULATION_POLICY{
			Primary: C.CF_POPULATION_POLICY_PARTIAL, // enumerate directories lazily
		},
	}

	hr := C.CfRegisterSyncRoot(
		(*C.WCHAR)(unsafe.Pointer(rootW)),
		&reg,
		&policies,
		0,
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: CfRegisterSyncRoot HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// UnregisterSyncRoot removes the Cloud Sync Root registration.
func UnregisterSyncRoot(syncRootPath string) error {
	rootW, err := windows.UTF16PtrFromString(syncRootPath)
	if err != nil {
		return err
	}
	hr := C.CfUnregisterSyncRoot((*C.WCHAR)(unsafe.Pointer(rootW)))
	if hr != 0 {
		return fmt.Errorf("cfapi: CfUnregisterSyncRoot HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// Connect attaches the callback table and starts listening.
func Connect(syncRootPath string, provider Provider, log *slog.Logger) (*Session, error) {
	rootW, err := windows.UTF16PtrFromString(syncRootPath)
	if err != nil {
		return nil, fmt.Errorf("cfapi: UTF16 path: %w", err)
	}

	s := &Session{
		log:  log,
		root: syncRootPath,
		prov: provider,
	}

	hr := C.cfapi_connect(
		(*C.WCHAR)(unsafe.Pointer(rootW)),
		unsafe.Pointer(s), // passed back in CallbackContext
		&s.connKey,
	)
	if hr != 0 {
		return nil, fmt.Errorf("cfapi: CfConnectSyncRoot HRESULT 0x%08x", uint32(hr))
	}

	registerSession(s)
	return s, nil
}

// Disconnect detaches the callback table.
func (s *Session) Disconnect() error {
	unregisterSession(s)
	hr := C.CfDisconnectSyncRoot(s.connKey)
	if hr != 0 {
		return fmt.Errorf("cfapi: CfDisconnectSyncRoot HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// ---------------------------------------------------------------------------
// CreatePlaceholders — called from FetchPlaceholders handler
// ---------------------------------------------------------------------------

// CreatePlaceholders creates child placeholders in the given directory.
func (s *Session) CreatePlaceholders(dirPath string, children []Placeholder) error {
	if len(children) == 0 {
		return nil
	}

	dirW, err := windows.UTF16PtrFromString(dirPath)
	if err != nil {
		return err
	}

	infos := make([]C.CF_PLACEHOLDER_CREATE_INFO, len(children))
	// Keep Go references alive for the duration of this call.
	namesBuf := make([]*uint16, len(children))

	for i, ch := range children {
		nameW, err := windows.UTF16PtrFromString(ch.Name)
		if err != nil {
			return fmt.Errorf("cfapi: CreatePlaceholders child name: %w", err)
		}
		namesBuf[i] = nameW

		// We store the FileIdentity as-is; Windows gives it back to us in FetchData.
		identity := ch.FileIdentity
		if len(identity) == 0 {
			identity = []byte(ch.Name)
		}

		attrs := uint32(0)
		if ch.IsDirectory {
			attrs = windows.FILE_ATTRIBUTE_DIRECTORY
		}

		info := &infos[i]
		info.RelativeFileName = (*C.WCHAR)(unsafe.Pointer(nameW))
		info.FileIdentity = C.LPCVOID(unsafe.Pointer(&identity[0]))
		info.FileIdentityLength = C.DWORD(len(identity))
		info.Flags = 0
		info.FsMetadata.BasicInfo_FileAttributes = C.DWORD(attrs)
		info.FsMetadata.FileSize.QuadPart = C.LONGLONG(ch.Size)
		// timestamps: leave zero → Windows sets to current time
	}

	var processed C.DWORD
	hr := C.CfCreatePlaceholders(
		(*C.WCHAR)(unsafe.Pointer(dirW)),
		&infos[0],
		C.DWORD(len(infos)),
		0,
		&processed,
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: CfCreatePlaceholders HRESULT 0x%08x (processed %d/%d)",
			uint32(hr), uint32(processed), len(children))
	}
	return nil
}

// ---------------------------------------------------------------------------
// TransferSession — used inside FetchData callbacks
// ---------------------------------------------------------------------------

// TransferSession is scoped to a single FetchData callback invocation.
type TransferSession struct {
	connKey int64
	txKey   int64
}

// Write sends a chunk of file data back to Windows.
func (ts *TransferSession) Write(buf []byte, offset int64) error {
	if len(buf) == 0 {
		return nil
	}
	hr := C.cfapi_transfer_data(
		C.CF_CONNECTION_KEY(ts.connKey),
		C.CF_TRANSFER_KEY(ts.txKey),
		unsafe.Pointer(&buf[0]),
		C.LONGLONG(offset),
		C.LONGLONG(int64(len(buf))),
		0, // STATUS_SUCCESS
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: TransferData HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// Fail signals to Windows that we couldn't produce the data.
func (ts *TransferSession) Fail(ntstatus int32) {
	C.cfapi_transfer_data(
		C.CF_CONNECTION_KEY(ts.connKey),
		C.CF_TRANSFER_KEY(ts.txKey),
		nil, 0, 0,
		C.NTSTATUS(ntstatus),
	)
}

// ---------------------------------------------------------------------------
// C callback trampolines — called from C, dispatch to Go
// ---------------------------------------------------------------------------

//export goFetchData
func goFetchData(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	s := sessionForKey(int64(info.ConnectionKey))
	if s == nil {
		return
	}

	identity := C.GoBytes(unsafe.Pointer(info.FileIdentity), C.int(info.FileIdentityLength))
	path := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))

	req := FetchDataRequest{
		ConnectionKey:  int64(info.ConnectionKey),
		TransferKey:    int64(info.TransferKey),
		NormalizedPath: path,
		FileIdentity:   identity,
		RequiredOffset: int64(params.FetchData.RequiredFileOffset.QuadPart),
		RequiredLength: int64(params.FetchData.RequiredLength.QuadPart),
	}
	sess := &TransferSession{
		connKey: int64(info.ConnectionKey),
		txKey:   int64(info.TransferKey),
	}

	if err := s.prov.FetchData(req, sess); err != nil {
		s.log.Error("cfapi: FetchData failed", "path", path, "err", err)
		sess.Fail(-0x3FFFFFBF) // STATUS_CLOUD_FILE_PROVIDER_UNKNOWN_ERROR
	}
}

//export goFetchPlaceholders
func goFetchPlaceholders(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	s := sessionForKey(int64(info.ConnectionKey))
	if s == nil {
		return
	}

	path := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))

	req := FetchPlaceholdersRequest{
		ConnectionKey:  int64(info.ConnectionKey),
		TransferKey:    int64(info.TransferKey),
		NormalizedPath: path,
	}

	if err := s.prov.FetchPlaceholders(req); err != nil {
		s.log.Error("cfapi: FetchPlaceholders failed", "path", path, "err", err)
	}
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

// IdentityFromString encodes a plain string as a stable file identity bytes.
func IdentityFromString(s string) []byte { return []byte(s) }

// hexID returns a short hex string for logging.
func hexID(b []byte) string {
	if len(b) > 8 {
		return hex.EncodeToString(b[:8]) + "…"
	}
	return hex.EncodeToString(b)
}
