//go:build windows

// Package cfapi provides Go bindings for the Windows Cloud Files API (CfApi).
// This allows neofs-mount to register as a Cloud Sync Provider, making NeoFS
// containers appear as a native folder in Windows Explorer without any third-party
// drivers.
//
// Requirements:
//   - Windows 10 version 1709 (Build 16299) or later
//   - MinGW: link against ${SRCDIR}/libcldapi.a (import lib built from cldapi.def; see Makefile).
//     MSVC may use the Windows SDK cldapi.lib via CGO_LDFLAGS if you override LDFLAGS.
//
// The build tag "windows" ensures this file is only compiled on Windows.

package cfapi

/*
#cgo windows CFLAGS: -O2 -g0
#cgo LDFLAGS: ${SRCDIR}/libcldapi.a -lole32 -lrpcrt4
#include "cfapi_windows.h"

// ─── C-to-Go thunk exports ────────────────────────────────────────────────
// CGo doesn't let Go functions be used directly as C function pointers.
// We define thin C trampoline functions that call back into Go via exported
// Go symbols.
#include <stdlib.h>
#include <string.h>

extern void goFetchData(CF_CALLBACK_INFO*, CF_CALLBACK_PARAMETERS*);
extern void goFetchPlaceholders(CF_CALLBACK_INFO*, CF_CALLBACK_PARAMETERS*);
extern void goNotifyCloseCompletion(CF_CALLBACK_INFO*, CF_CALLBACK_PARAMETERS*);
extern void goNotifyRenameCompletion(CF_CALLBACK_INFO*, CF_CALLBACK_PARAMETERS*);
extern void goNotifyDeleteCompletion(CF_CALLBACK_INFO*, CF_CALLBACK_PARAMETERS*);
extern void goValidateData(CF_CALLBACK_INFO*, CF_CALLBACK_PARAMETERS*);

static void c_fetch_data_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goFetchData((CF_CALLBACK_INFO*)i, (CF_CALLBACK_PARAMETERS*)p);
}

static void c_fetch_placeholders_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goFetchPlaceholders((CF_CALLBACK_INFO*)i, (CF_CALLBACK_PARAMETERS*)p);
}

static void c_close_completion_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goNotifyCloseCompletion((CF_CALLBACK_INFO*)i, (CF_CALLBACK_PARAMETERS*)p);
}

static void c_rename_completion_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goNotifyRenameCompletion((CF_CALLBACK_INFO*)i, (CF_CALLBACK_PARAMETERS*)p);
}

// cfapi_ack_delete completes CF_CALLBACK_TYPE_DELETE with an NTSTATUS.
static HRESULT cfapi_ack_delete(CF_CONNECTION_KEY connKey, LONGLONG txKey, NTSTATUS status) {
    CF_OPERATION_INFO opInfo = {0};
    opInfo.StructSize    = sizeof(opInfo);
    opInfo.Type          = CF_OPERATION_TYPE_ACK_DELETE;
    opInfo.ConnectionKey = connKey;
    opInfo.TransferKey   = txKey;

    CF_OPERATION_PARAMETERS opParams = {0};
    opParams.ParamSize                    = sizeof(opParams);
    opParams.AckDelete.Flags              = 0;
    opParams.AckDelete.CompletionStatus   = status;
    return CfExecute(&opInfo, &opParams);
}

// cfapi_ack_rename completes CF_CALLBACK_TYPE_RENAME with an NTSTATUS.
static HRESULT cfapi_ack_rename(CF_CONNECTION_KEY connKey, LONGLONG txKey, NTSTATUS status) {
    CF_OPERATION_INFO opInfo = {0};
    opInfo.StructSize    = sizeof(opInfo);
    opInfo.Type          = CF_OPERATION_TYPE_ACK_RENAME;
    opInfo.ConnectionKey = connKey;
    opInfo.TransferKey   = txKey;

    CF_OPERATION_PARAMETERS opParams = {0};
    opParams.ParamSize                    = sizeof(opParams);
    opParams.AckRename.Flags              = 0;
    opParams.AckRename.CompletionStatus   = status;
    return CfExecute(&opInfo, &opParams);
}

static void c_rename_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    (void)p;
    LONGLONG txKey = i->TransferKey.QuadPart;
    cfapi_ack_rename(i->ConnectionKey, txKey, (NTSTATUS)0); // always approve
}

static void c_delete_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    (void)p;
    LONGLONG txKey = i->TransferKey.QuadPart;
    cfapi_ack_delete(i->ConnectionKey, txKey, (NTSTATUS)0); // always approve
}

static void c_delete_completion_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goNotifyDeleteCompletion((CF_CALLBACK_INFO*)i, (CF_CALLBACK_PARAMETERS*)p);
}

static void c_validate_data_cb(CONST CF_CALLBACK_INFO* i, CONST CF_CALLBACK_PARAMETERS* p) {
    goValidateData((CF_CALLBACK_INFO*)i, (CF_CALLBACK_PARAMETERS*)p);
}

static HRESULT cfapi_connect(LPCWSTR path, LPVOID ctx, CF_CONNECTION_KEY* key) {
    CF_CALLBACK_REGISTRATION table[] = {
        { CF_CALLBACK_TYPE_FETCH_DATA,           c_fetch_data_cb         },
        { CF_CALLBACK_TYPE_VALIDATE_DATA,        c_validate_data_cb      },
        { CF_CALLBACK_TYPE_FETCH_PLACEHOLDERS,   c_fetch_placeholders_cb },
        { CF_CALLBACK_TYPE_CLOSE_COMPLETION,     c_close_completion_cb   },
        { CF_CALLBACK_TYPE_RENAME,               c_rename_cb             },
        { CF_CALLBACK_TYPE_RENAME_COMPLETION,    c_rename_completion_cb  },
        { CF_CALLBACK_TYPE_DELETE,               c_delete_cb             },
        { CF_CALLBACK_TYPE_DELETE_COMPLETION,     c_delete_completion_cb },
        CF_CALLBACK_REGISTRATION_END
    };
    return CfConnectSyncRoot(path, table, ctx, 0, key);
}

static HRESULT cfapi_transfer_data(
    CF_CONNECTION_KEY connKey,
    LARGE_INTEGER*    txKeyPtr,
    LPCVOID buf, LONGLONG offset, LONGLONG length,
    NTSTATUS status)
{
    CF_OPERATION_INFO opInfo = {0};
    opInfo.StructSize    = sizeof(opInfo);
    opInfo.Type          = CF_OPERATION_TYPE_TRANSFER_DATA;
    opInfo.ConnectionKey = connKey;
    opInfo.TransferKey   = txKeyPtr->QuadPart;

    CF_OPERATION_PARAMETERS opParams = {0};
    opParams.ParamSize                      = sizeof(opParams);
    opParams.TransferData.Flags             = 0;
    opParams.TransferData.CompletionStatus  = status;
    opParams.TransferData.Buffer            = buf;
    opParams.TransferData.Offset.QuadPart   = offset;
    opParams.TransferData.Length.QuadPart   = length;

    return CfExecute(&opInfo, &opParams);
}

static HRESULT cfapi_ack_data(
    CF_CONNECTION_KEY connKey,
    LONGLONG txKey,
    LONGLONG offset, LONGLONG length)
{
    CF_OPERATION_INFO opInfo = {0};
    opInfo.StructSize    = sizeof(opInfo);
    opInfo.Type          = CF_OPERATION_TYPE_ACK_DATA;
    opInfo.ConnectionKey = connKey;
    opInfo.TransferKey   = txKey;

    CF_OPERATION_PARAMETERS opParams = {0};
    opParams.ParamSize                      = sizeof(opParams);
    opParams.AckData.Flags             = 0;
    opParams.AckData.CompletionStatus  = 0;
    opParams.AckData.Offset.QuadPart   = offset;
    opParams.AckData.Length.QuadPart   = length;

    return CfExecute(&opInfo, &opParams);
}

// cfapi_transfer_placeholder sends one TRANSFER_PLACEHOLDERS CfExecute. For a
// directory with N children, every call must use the same PlaceholderTotalCount=N;
// PlaceholderCount is 0 (empty completion) or 1; EntriesProcessed is the count
// already transferred in prior calls in this callback (0..N-1). The last call
// should set opFlags to CF_OPERATION_TRANSFER_PLACEHOLDERS_FLAG_DISABLE_ON_DEMAND_POPULATION.
static HRESULT cfapi_transfer_placeholder(
    CF_CONNECTION_KEY connKey,
    LARGE_INTEGER*    txKeyPtr,
    LPCWSTR relFileName,
    LPCVOID identity, DWORD identityLen,
    LONGLONG fileSize, DWORD attrs,
    LONGLONG placeholderTotalCount,
    DWORD placeholderCount,
    DWORD entriesProcessedSoFar,
    DWORD opFlags)
{
    CF_OPERATION_INFO opInfo = {0};
    opInfo.StructSize    = sizeof(opInfo);
    opInfo.Type          = CF_OPERATION_TYPE_TRANSFER_PLACEHOLDERS;
    opInfo.ConnectionKey = connKey;
    opInfo.TransferKey   = txKeyPtr->QuadPart;

    CF_PLACEHOLDER_CREATE_INFO pi = {0};
    CF_PLACEHOLDER_CREATE_INFO* pArray = NULL;
    if (placeholderCount > 0 && relFileName != NULL) {
        pi.RelativeFileName                    = relFileName;
        pi.FileIdentity                        = identity;
        pi.FileIdentityLength                  = identityLen;
        pi.FsMetadata.BasicInfo_FileAttributes = attrs;
        pi.FsMetadata.FileSize.QuadPart        = fileSize;
        // MARK_IN_SYNC only. Do not set DISABLE_ON_DEMAND on directory placeholders:
        // that flag means the directory cannot grow via on-demand population, which
        // breaks "New file", paste of a new name, etc. inside container folders (0x80070781).
        // Use CreatePlaceholders(..., disableOnDemand) for the *operation* final opFlags
        // when a parent listing should stop refetching (e.g. sync root container list).
        pi.Flags = CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC;
        pArray = &pi;
    }

    CF_OPERATION_PARAMETERS opParams = {0};
    opParams.ParamSize = sizeof(opParams);
    opParams.TransferPlaceholders.Flags                   = opFlags;
    opParams.TransferPlaceholders.CompletionStatus        = 0;
    opParams.TransferPlaceholders.PlaceholderTotalCount.QuadPart = placeholderTotalCount;
    opParams.TransferPlaceholders.PlaceholderArray        = pArray;
    opParams.TransferPlaceholders.PlaceholderCount        = placeholderCount;
    opParams.TransferPlaceholders.EntriesProcessed        = entriesProcessedSoFar;

	return CfExecute(&opInfo, &opParams);
}

// cfapi_ack_data_fail completes ValidateData with a failing NTSTATUS (session gone / error).
static HRESULT cfapi_ack_data_fail(
    CF_CONNECTION_KEY connKey,
    LONGLONG txKey,
    LONGLONG offset, LONGLONG length,
    NTSTATUS status)
{
    CF_OPERATION_INFO opInfo = {0};
    opInfo.StructSize    = sizeof(opInfo);
    opInfo.Type          = CF_OPERATION_TYPE_ACK_DATA;
    opInfo.ConnectionKey = connKey;
    opInfo.TransferKey   = txKey;

    CF_OPERATION_PARAMETERS opParams = {0};
    opParams.ParamSize                      = sizeof(opParams);
    opParams.AckData.Flags                  = 0;
    opParams.AckData.CompletionStatus       = status;
    opParams.AckData.Offset.QuadPart        = offset;
    opParams.AckData.Length.QuadPart        = length;

    return CfExecute(&opInfo, &opParams);
}

// cfapi_get_transfer_key safely extracts the quad part from a transfer key
static LONGLONG cfapi_get_transfer_key(LARGE_INTEGER* keyPtr) {
    return keyPtr->QuadPart;
}

// Use the callback's TransferKey field via C (correct layout); Go cgo can mis-place
// LARGE_INTEGER when accessed as info.TransferKey[0], breaking CfExecute and dedup keys.
static LARGE_INTEGER* cfapi_info_transfer_key_ptr(CF_CALLBACK_INFO* i) {
    return &i->TransferKey;
}

// cfapi_get_fetch_data_offset safely extracts the RequiredFileOffset
static LONGLONG cfapi_get_fetch_data_offset(CONST CF_CALLBACK_PARAMETERS* p) {
    return p->FetchData.RequiredFileOffset.QuadPart;
}

// cfapi_get_fetch_data_length safely extracts the RequiredLength
static LONGLONG cfapi_get_fetch_data_length(CONST CF_CALLBACK_PARAMETERS* p) {
    return p->FetchData.RequiredLength.QuadPart;
}

static DWORD cfapi_get_close_completion_flags(CONST CF_CALLBACK_PARAMETERS* p) {
    return p->CloseCompletion.Flags;
}

static LPCWSTR cfapi_get_rename_completion_source(CONST CF_CALLBACK_PARAMETERS* p) {
    return p->RenameCompletion.SourcePath;
}

static LONGLONG cfapi_get_validate_data_offset(CONST CF_CALLBACK_PARAMETERS* p) {
    return p->ValidateData.RequiredFileOffset.QuadPart;
}

static LONGLONG cfapi_get_validate_data_length(CONST CF_CALLBACK_PARAMETERS* p) {
    return p->ValidateData.RequiredLength.QuadPart;
}

// cfapi_create_file_placeholder calls CfCreatePlaceholders for one child under baseDirectoryPath.
static HRESULT cfapi_create_file_placeholder(
    LPCWSTR baseDirectoryPath,
    LPCWSTR relativeFileName,
    LONGLONG fileSize,
    DWORD fileAttributes,
    LPCVOID fileIdentity,
    DWORD fileIdentityLength)
{
    CF_FS_METADATA meta = {0};
    meta.BasicInfo_FileAttributes = fileAttributes;
    meta.FileSize.QuadPart = fileSize;

    CF_PLACEHOLDER_CREATE_INFO info = {0};
    info.RelativeFileName = relativeFileName;
    info.FsMetadata = meta;
    info.FileIdentity = fileIdentity;
    info.FileIdentityLength = fileIdentityLength;

    DWORD entriesProcessed = 0;
    return CfCreatePlaceholders(
        baseDirectoryPath,
        &info,
        1,
        0,
        &entriesProcessed);
}

// cfapi_convert_to_placeholder opens a file and converts it to a cloud placeholder.
static HRESULT cfapi_convert_to_placeholder(
    LPCWSTR filePath,
    LPCVOID fileIdentity,
    DWORD fileIdentityLength,
    DWORD convertFlags)
{
    HANDLE h = CreateFileW(
        filePath,
        GENERIC_READ | GENERIC_WRITE,
        FILE_SHARE_READ,
        NULL,
        OPEN_EXISTING,
        FILE_FLAG_BACKUP_SEMANTICS, // needed for directories
        NULL);
    if (h == INVALID_HANDLE_VALUE)
        return HRESULT_FROM_WIN32(GetLastError());

    HRESULT hr = CfConvertToPlaceholder(
        h,
        fileIdentity,
        fileIdentityLength,
        (CF_CONVERT_FLAGS)convertFlags,
        NULL,
        NULL);
    CloseHandle(h);
    return hr;
}

// cfapi_set_in_sync_state drives Explorer's per-file cloud state (in-sync vs pending).
// BACKUP_SEMANTICS is for directories only; using it on normal files can break CreateFile
// for some cloud placeholders.
static HRESULT cfapi_set_in_sync_state(LPCWSTR filePath, int inSync) {
    DWORD attrs = GetFileAttributesW(filePath);
    DWORD openFlags = 0;
    if (attrs != INVALID_FILE_ATTRIBUTES && (attrs & FILE_ATTRIBUTE_DIRECTORY))
        openFlags = FILE_FLAG_BACKUP_SEMANTICS;

    HANDLE h = CreateFileW(
        filePath,
        GENERIC_READ | GENERIC_WRITE,
        FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
        NULL,
        OPEN_EXISTING,
        openFlags,
        NULL);
    if (h == INVALID_HANDLE_VALUE)
        return HRESULT_FROM_WIN32(GetLastError());

    CF_IN_SYNC_STATE state = inSync ? CF_IN_SYNC_STATE_IN_SYNC : CF_IN_SYNC_STATE_NOT_IN_SYNC;
    HRESULT hr = CfSetInSyncState(h, state, CF_SET_INSYNC_FLAG_NONE, NULL);
    CloseHandle(h);
    return hr;
}
*/
import "C"

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// Public API types
// ---------------------------------------------------------------------------

// FetchDataRequest is received when Windows requests bytes of a placeholder file.
type FetchDataRequest struct {
	ConnectionKey  int64
	TransferKey    int64
	NormalizedPath string // full path relative to sync root
	FileIdentity   []byte // opaque ID we stored during CreatePlaceholders
	RequiredOffset int64
	RequiredLength int64
}

// FetchPlaceholdersRequest is received when Windows opens a directory.
type FetchPlaceholdersRequest struct {
	ConnectionKey  int64
	TransferKey    int64
	NormalizedPath string
}

// NotifyFileCloseRequest is CF_CALLBACK_TYPE_NOTIFY_FILE_CLOSE_COMPLETION.
type NotifyFileCloseRequest struct {
	ConnectionKey  int64
	NormalizedPath string
	FileIdentity   []byte
	Deleted        bool // CF_CALLBACK_CLOSE_COMPLETION_FLAG_DELETED
}

// NotifyRenameCompletionRequest is CF_CALLBACK_TYPE_NOTIFY_FILE_RENAME_COMPLETION.
type NotifyRenameCompletionRequest struct {
	ConnectionKey  int64
	NormalizedPath string // destination path after rename
	SourcePath     string // wide path from callback params (may be empty)
}

// NotifyDeleteCompletionRequest is CF_CALLBACK_TYPE_NOTIFY_DELETE_COMPLETION.
type NotifyDeleteCompletionRequest struct {
	ConnectionKey  int64
	NormalizedPath string
	FileIdentity   []byte
}

// ValidateDataRequest is CF_CALLBACK_TYPE_VALIDATE_DATA.
type ValidateDataRequest struct {
	ConnectionKey  int64
	TransferKey    int64
	NormalizedPath string
	FileIdentity   []byte
	RequiredOffset int64
	RequiredLength int64 // -1 (CF_EOF) means through end of file; file size must be supplied to AckRange
}

// Placeholder describes a file or directory to be created in the sync root.
type Placeholder struct {
	Name         string
	IsDirectory  bool
	Size         int64  // 0 for directories
	FileIdentity []byte // opaque blob stored by Windows, returned on fetch
}

// Provider is the callback interface callers must implement.
type Provider interface {
	// FetchPlaceholders is called when Windows opens a directory.
	// The implementation should call sess.CreatePlaceholders with the children.
	FetchPlaceholders(req FetchPlaceholdersRequest) error

	// FetchData is called when Windows needs to read bytes from a file.
	// The implementation must call sess.Write for all required bytes, then the
	// runtime issues a zero-length TransferData to complete the callback.
	FetchData(req FetchDataRequest, sess *TransferSession) error

	// NotifyFileClose is invoked after a file under the sync root is closed
	// (e.g. finish uploading local changes).
	NotifyFileClose(req NotifyFileCloseRequest)

	// NotifyRenameCompletion is invoked after a successful rename/move into place
	// (Explorer often copies to a temp name then renames).
	NotifyRenameCompletion(req NotifyRenameCompletionRequest)

	// NotifyDeleteCompletion is called after a cloud placeholder was successfully deleted.
	NotifyDeleteCompletion(req NotifyDeleteCompletionRequest)

	// ValidateData acknowledges ranges of user-written data; call vs.AckRange.
	ValidateData(req ValidateDataRequest, vs *ValidateSession) error
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
// providerName and providerVersion appear in Windows Settings -> Apps -> Connected Sync Providers.
// This only needs to be called once per machine; subsequent launches just call Connect.
func RegisterSyncRoot(syncRootPath, providerName, providerVersion string) error {
	// Convert to UTF16 slices
	root16, err := windows.UTF16FromString(syncRootPath)
	if err != nil {
		return fmt.Errorf("cfapi: UTF16 sync root path: %w", err)
	}
	name16, err := windows.UTF16FromString(providerName)
	if err != nil {
		return fmt.Errorf("cfapi: UTF16 provider name: %w", err)
	}
	ver16, err := windows.UTF16FromString(providerVersion)
	if err != nil {
		return fmt.Errorf("cfapi: UTF16 provider version: %w", err)
	}

	// Allocate C memory to hold the UTF16 arrays so CGo doesn't panic on Go pointers
	rootSize := C.size_t(len(root16) * 2)
	nameSize := C.size_t(len(name16) * 2)
	verSize := C.size_t(len(ver16) * 2)

	rootC := C.malloc(rootSize)
	nameC := C.malloc(nameSize)
	verC := C.malloc(verSize)

	defer C.free(rootC)
	defer C.free(nameC)
	defer C.free(verC)

	// Copy data to C memory
	C.memcpy(rootC, unsafe.Pointer(&root16[0]), rootSize)
	C.memcpy(nameC, unsafe.Pointer(&name16[0]), nameSize)
	C.memcpy(verC, unsafe.Pointer(&ver16[0]), verSize)

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
		ProviderName:    (*C.WCHAR)(nameC),
		ProviderVersion: (*C.WCHAR)(verC),
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
		// NONE lets Explorer reflect CfSetInSyncState; PRESERVE can leave overlays stuck for some hosts.
		InSyncPolicy: C.CF_INSYNC_POLICY_NONE,
	}

	hr := C.CfRegisterSyncRoot(
		(*C.WCHAR)(rootC),
		&reg,
		&policies,
		0,
	)
	if hr != 0 {
		// Refresh registration when the sync root already exists (policies / provider metadata).
		hr = C.CfRegisterSyncRoot(
			(*C.WCHAR)(rootC),
			&reg,
			&policies,
			C.CF_REGISTER_FLAG_UPDATE,
		)
		if hr != 0 {
			return fmt.Errorf("cfapi: CfRegisterSyncRoot HRESULT 0x%08x", uint32(hr))
		}
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
		C.LPVOID(nil), // No longer passing 's'; we look it up by ConnectionKey in callbacks
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
	// Disconnect from CfApi first so in-flight callbacks can still resolve the session.
	// Unregistering before CfDisconnectSyncRoot caused callbacks to see no session, return
	// without CfExecute, and leave Explorer in a bad state ("The cloud operation is invalid").
	hr := C.CfDisconnectSyncRoot(s.connKey)
	unregisterSession(s)
	if hr != 0 {
		return fmt.Errorf("cfapi: CfDisconnectSyncRoot HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// ---------------------------------------------------------------------------
// CreatePlaceholders
// ---------------------------------------------------------------------------

// TransferFetchPlaceholdersEmpty completes extra FETCH_PLACEHOLDERS callbacks that share the same
// (ConnectionKey, TransferKey) as a leader that already ran CreatePlaceholders. Only one
// TRANSFER_PLACEHOLDERS sequence may carry data per transfer key; others must use an empty op.
func (s *Session) TransferFetchPlaceholdersEmpty(connKey, txKey int64) error {
	hr := C.cfapi_transfer_placeholder(
		C.CF_CONNECTION_KEY(connKey),
		(*C.LARGE_INTEGER)(unsafe.Pointer(&txKey)),
		nil, nil, 0,
		C.LONGLONG(0), C.DWORD(0),
		C.LONGLONG(0), C.DWORD(0), C.DWORD(0),
		C.DWORD(0),
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: TransferFetchPlaceholdersEmpty HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// CreatePlaceholders answers a FetchPlaceholders callback by transferring placeholders to Windows.
// If disableOnDemand is true, CfApi won't fire FetchPlaceholders for this directory again
// (appropriate for fully-known directories like the root container list). For object directories
// where users may paste/create new local files, pass false so CfApi keeps the directory
// "partially populated" and allows new file creation.
func (s *Session) CreatePlaceholders(connKey, txKey int64, dirPath string, children []Placeholder, disableOnDemand bool) error {
	valid := make([]Placeholder, 0, len(children))
	for _, ch := range children {
		if ch.Name == "" {
			continue
		}
		if len(ch.FileIdentity) == 0 {
			ch.FileIdentity = []byte(ch.Name)
		}
		if len(ch.FileIdentity) == 0 {
			continue
		}
		valid = append(valid, ch)
	}

	disableFlag := C.DWORD(0)
	if disableOnDemand {
		disableFlag = C.DWORD(1) // CF_OPERATION_TRANSFER_PLACEHOLDERS_FLAG_DISABLE_ON_DEMAND_POPULATION
	}

	if len(valid) == 0 {
		hr := C.cfapi_transfer_placeholder(
			C.CF_CONNECTION_KEY(connKey),
			(*C.LARGE_INTEGER)(unsafe.Pointer(&txKey)),
			nil, nil, 0,
			C.LONGLONG(0), C.DWORD(0),
			C.LONGLONG(0), C.DWORD(0), C.DWORD(0),
			disableFlag,
		)
		if hr != 0 {
			return fmt.Errorf("cfapi: TransferPlaceholder (empty) failed: HRESULT 0x%08x", uint32(hr))
		}
		return nil
	}

	n := int64(len(valid))
	for i, ch := range valid {
		name16, err := windows.UTF16FromString(ch.Name)
		if err != nil {
			return fmt.Errorf("cfapi: CreatePlaceholders child name: %w", err)
		}

		identity := ch.FileIdentity
		nameSize := C.size_t(len(name16) * 2)
		idSize := C.size_t(len(identity))

		nameC := C.malloc(nameSize)
		idC := C.malloc(idSize)
		if nameC == nil || idC == nil {
			if nameC != nil {
				C.free(nameC)
			}
			if idC != nil {
				C.free(idC)
			}
			return fmt.Errorf("cfapi: CreatePlaceholders malloc failed")
		}

		C.memcpy(nameC, unsafe.Pointer(&name16[0]), nameSize)
		C.memcpy(idC, unsafe.Pointer(&identity[0]), idSize)

		attrs := uint32(0)
		if ch.IsDirectory {
			attrs = windows.FILE_ATTRIBUTE_DIRECTORY
		}

		opFlags := C.DWORD(0)
		if i == len(valid)-1 {
			opFlags = disableFlag
		}

		hr := C.cfapi_transfer_placeholder(
			C.CF_CONNECTION_KEY(connKey),
			(*C.LARGE_INTEGER)(unsafe.Pointer(&txKey)),
			(*C.WCHAR)(nameC),
			C.LPCVOID(idC),
			C.DWORD(len(identity)),
			C.LONGLONG(ch.Size),
			C.DWORD(attrs),
			C.LONGLONG(n),
			C.DWORD(1),
			C.DWORD(i),
			opFlags,
		)

		C.free(nameC)
		C.free(idC)

		if hr != 0 {
			if uint32(hr) == 0x8007018e {
				return nil
			}
			return fmt.Errorf("cfapi: TransferPlaceholder HRESULT 0x%08x for %q", uint32(hr), ch.Name)
		}
	}

	return nil
}

// RestoreLocalPlaceholder calls CfCreatePlaceholders to materialize one cloud placeholder on disk.
// relativeFileName must be a single path segment (no separators). Use when NeoFS still has the
// object but the local file was removed (e.g. user moved it out of the sync root).
func RestoreLocalPlaceholder(baseDirectory, relativeFileName string, size int64, attributes uint32, identity []byte) error {
	if baseDirectory == "" || relativeFileName == "" {
		return fmt.Errorf("cfapi: empty path")
	}
	if strings.ContainsAny(relativeFileName, `\/`) {
		return fmt.Errorf("cfapi: relativeFileName must be one segment, got %q", relativeFileName)
	}
	if len(identity) == 0 {
		return fmt.Errorf("cfapi: empty identity")
	}
	baseW, err := windows.UTF16PtrFromString(baseDirectory)
	if err != nil {
		return err
	}
	relW, err := windows.UTF16PtrFromString(relativeFileName)
	if err != nil {
		return err
	}
	idLen := len(identity)
	idC := C.malloc(C.size_t(idLen))
	if idC == nil {
		return fmt.Errorf("cfapi: malloc identity")
	}
	defer C.free(idC)
	C.memcpy(idC, unsafe.Pointer(&identity[0]), C.size_t(idLen))

	hr := C.cfapi_create_file_placeholder(
		(*C.WCHAR)(unsafe.Pointer(baseW)),
		(*C.WCHAR)(unsafe.Pointer(relW)),
		C.LONGLONG(size),
		C.DWORD(attributes),
		C.LPCVOID(idC),
		C.DWORD(idLen),
	)
	if hr != 0 {
		u := uint32(hr)
		// Placeholder already present (or equivalent); treat as success.
		if u == 0x8007018e {
			return nil
		}
		return fmt.Errorf("cfapi: CfCreatePlaceholders HRESULT 0x%08x", u)
	}
	return nil
}

// ConvertToPlaceholder turns an existing regular file inside the sync root
// into a cloud file placeholder with the given identity.  After conversion
// CfApi will manage the file (delete/rename callbacks will fire).
func ConvertToPlaceholder(filePath string, identity []byte, markInSync bool) error {
	pathW, err := windows.UTF16PtrFromString(filePath)
	if err != nil {
		return err
	}
	if len(identity) == 0 {
		return fmt.Errorf("cfapi: ConvertToPlaceholder: empty identity")
	}
	idC := C.malloc(C.size_t(len(identity)))
	if idC == nil {
		return fmt.Errorf("cfapi: ConvertToPlaceholder: malloc")
	}
	defer C.free(idC)
	C.memcpy(idC, unsafe.Pointer(&identity[0]), C.size_t(len(identity)))

	flags := C.DWORD(0)
	if markInSync {
		flags = 1 // CF_CONVERT_FLAG_MARK_IN_SYNC
	}

	hr := C.cfapi_convert_to_placeholder(
		(*C.WCHAR)(unsafe.Pointer(pathW)),
		C.LPCVOID(idC),
		C.DWORD(len(identity)),
		flags,
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: CfConvertToPlaceholder HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// SetInSyncState updates the Windows cloud placeholder "in sync" flag. Explorer uses this for
// the small overlay on file icons (inSync=false: pending / activity; inSync=true: up to date).
// The Cloud Files API only exposes these two states; there is no separate "syncing" value.
func SetInSyncState(path string, inSync bool) error {
	pathW, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	v := C.int(0)
	if inSync {
		v = 1
	}
	hr := C.cfapi_set_in_sync_state((*C.WCHAR)(unsafe.Pointer(pathW)), v)
	if hr != 0 {
		return fmt.Errorf("cfapi: CfSetInSyncState HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// ---------------------------------------------------------------------------
// TransferSession - used inside FetchData callbacks
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
		(*C.LARGE_INTEGER)(unsafe.Pointer(&ts.txKey)),
		C.LPCVOID(unsafe.Pointer(&buf[0])),
		C.LONGLONG(offset),
		C.LONGLONG(int64(len(buf))),
		0, // STATUS_SUCCESS
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: TransferData HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// Complete finishes a FetchData callback (zero-length TRANSFER_DATA, STATUS_SUCCESS).
func (ts *TransferSession) Complete() error {
	hr := C.cfapi_transfer_data(
		C.CF_CONNECTION_KEY(ts.connKey),
		(*C.LARGE_INTEGER)(unsafe.Pointer(&ts.txKey)),
		C.LPCVOID(nil),
		C.LONGLONG(0),
		C.LONGLONG(0),
		0,
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: TransferData(complete) HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// Fail signals to Windows that we couldn't produce the data.
func (ts *TransferSession) Fail(ntstatus int32) {
	C.cfapi_transfer_data(
		C.CF_CONNECTION_KEY(ts.connKey),
		(*C.LARGE_INTEGER)(unsafe.Pointer(&ts.txKey)),
		C.LPCVOID(nil), 0, 0,
		C.NTSTATUS(ntstatus),
	)
}

// ---------------------------------------------------------------------------
// ValidateSession — CF_OPERATION_TYPE_ACK_DATA during ValidateData
// ---------------------------------------------------------------------------

// ValidateSession acknowledges locally modified bytes for ValidateData callbacks.
type ValidateSession struct {
	connKey int64
	txKey   int64
}

// AckRange acknowledges [reqOff, reqOff+reqLen) expanded to 4 KiB alignment.
// If reqLen < 0 (CF_EOF), end is fileSize. Pass fileSize 0 when unknown only if reqLen >= 0.
func (vs *ValidateSession) AckRange(reqOff, reqLen, fileSize int64) error {
	const align = int64(4096)
	end := reqOff + reqLen
	if reqLen < 0 {
		if fileSize <= 0 {
			return fmt.Errorf("cfapi: ValidateData ACK EOF span without file size")
		}
		end = fileSize
	}
	if end <= reqOff {
		return nil
	}
	a0 := reqOff / align * align
	a1 := (end + align - 1) / align * align
	if fileSize > 0 && a1 > fileSize {
		a1 = fileSize
	}
	ln := a1 - a0
	if ln <= 0 {
		return nil
	}
	hr := C.cfapi_ack_data(
		C.CF_CONNECTION_KEY(vs.connKey),
		C.LONGLONG(vs.txKey),
		C.LONGLONG(a0),
		C.LONGLONG(ln),
	)
	if hr != 0 {
		return fmt.Errorf("cfapi: AckData HRESULT 0x%08x offset=%d len=%d", uint32(hr), a0, ln)
	}
	return nil
}

// ---------------------------------------------------------------------------
// C callback trampolines
// ---------------------------------------------------------------------------

//export goFetchData
func goFetchData(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	connKey := int64(info.ConnectionKey)
	tkPtr := C.cfapi_info_transfer_key_ptr(info)
	txKey := int64(C.cfapi_get_transfer_key(tkPtr))
	sess := &TransferSession{connKey: connKey, txKey: txKey}

	s := sessionForKey(connKey)
	if s == nil {
		sess.Fail(-0x3FFFFFBF) // STATUS_CLOUD_FILE_PROVIDER_UNKNOWN_ERROR — must complete callback
		return
	}

	var identity []byte
	var path string

	if info.FileIdentity != nil && info.FileIdentityLength > 0 {
		identity = C.GoBytes(unsafe.Pointer(info.FileIdentity), C.int(info.FileIdentityLength))
	}
	if info.NormalizedPath != nil {
		path = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))
	}

	req := FetchDataRequest{
		ConnectionKey:  connKey,
		TransferKey:    txKey,
		NormalizedPath: path,
		FileIdentity:   identity,
		RequiredOffset: int64(C.cfapi_get_fetch_data_offset(params)),
		RequiredLength: int64(C.cfapi_get_fetch_data_length(params)),
	}

	if err := s.prov.FetchData(req, sess); err != nil {
		s.log.Error("cfapi: FetchData failed", "path", path, "err", err)
		sess.Fail(-0x3FFFFFBF) // STATUS_CLOUD_FILE_PROVIDER_UNKNOWN_ERROR
		return
	}
	if err := sess.Complete(); err != nil {
		s.log.Error("cfapi: FetchData completion failed", "path", path, "err", err)
	}
}

//export goFetchPlaceholders
func goFetchPlaceholders(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	connKey := int64(info.ConnectionKey)
	tkPtr := C.cfapi_info_transfer_key_ptr(info)
	txKey := int64(C.cfapi_get_transfer_key(tkPtr))

	var path string
	if info.NormalizedPath != nil {
		path = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))
	}

	s := sessionForKey(connKey)
	if s == nil {
		// No session — complete the callback with an empty transfer so
		// CfApi doesn't hang.  Don't disable on-demand population so a
		// retry can succeed after the session is established.
		C.cfapi_transfer_placeholder(
			C.CF_CONNECTION_KEY(info.ConnectionKey),
			tkPtr,
			nil, nil, 0,
			C.LONGLONG(0), C.DWORD(0),
			C.LONGLONG(0), C.DWORD(0), C.DWORD(0),
			C.DWORD(0),
		)
		return
	}

	req := FetchPlaceholdersRequest{
		ConnectionKey:  connKey,
		TransferKey:    txKey,
		NormalizedPath: path,
	}

	if err := s.prov.FetchPlaceholders(req); err != nil {
		s.log.Error("cfapi: FetchPlaceholders failed", "path", path, "err", err)
		C.cfapi_transfer_placeholder(
			C.CF_CONNECTION_KEY(info.ConnectionKey),
			tkPtr,
			nil, nil, 0,
			C.LONGLONG(0), C.DWORD(0),
			C.LONGLONG(0), C.DWORD(0), C.DWORD(0),
			C.DWORD(0),
		)
	}
}

//export goNotifyCloseCompletion
func goNotifyCloseCompletion(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	s := sessionForKey(int64(info.ConnectionKey))
	if s == nil {
		return
	}
	flags := uint32(C.cfapi_get_close_completion_flags(params))
	var path string
	if info.NormalizedPath != nil {
		path = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))
	}
	var identity []byte
	if info.FileIdentity != nil && info.FileIdentityLength > 0 {
		identity = C.GoBytes(unsafe.Pointer(info.FileIdentity), C.int(info.FileIdentityLength))
	}
	req := NotifyFileCloseRequest{
		ConnectionKey:  int64(info.ConnectionKey),
		NormalizedPath: path,
		FileIdentity:   identity,
		Deleted:        flags&0x1 != 0, // CF_CALLBACK_CLOSE_COMPLETION_FLAG_DELETED
	}
	s.prov.NotifyFileClose(req)
}

//export goNotifyRenameCompletion
func goNotifyRenameCompletion(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	s := sessionForKey(int64(info.ConnectionKey))
	if s == nil {
		return
	}
	var dest, src string
	if info.NormalizedPath != nil {
		dest = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))
	}
	sp := C.cfapi_get_rename_completion_source(params)
	if sp != nil {
		src = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(sp)))
	}
	s.prov.NotifyRenameCompletion(NotifyRenameCompletionRequest{
		ConnectionKey:  int64(info.ConnectionKey),
		NormalizedPath: dest,
		SourcePath:     src,
	})
}

//export goNotifyDeleteCompletion
func goNotifyDeleteCompletion(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	_ = params
	s := sessionForKey(int64(info.ConnectionKey))
	if s == nil {
		return
	}
	var path string
	if info.NormalizedPath != nil {
		path = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))
	}
	var identity []byte
	if info.FileIdentity != nil && info.FileIdentityLength > 0 {
		identity = C.GoBytes(unsafe.Pointer(info.FileIdentity), C.int(info.FileIdentityLength))
	}
	s.prov.NotifyDeleteCompletion(NotifyDeleteCompletionRequest{
		ConnectionKey:  int64(info.ConnectionKey),
		NormalizedPath: path,
		FileIdentity:   identity,
	})
}

//export goValidateData
func goValidateData(info *C.CF_CALLBACK_INFO, params *C.CF_CALLBACK_PARAMETERS) {
	connKey := int64(info.ConnectionKey)
	tkPtr := C.cfapi_info_transfer_key_ptr(info)
	txKey := int64(C.cfapi_get_transfer_key(tkPtr))
	s := sessionForKey(connKey)
	if s == nil {
		off := int64(C.cfapi_get_validate_data_offset(params))
		ln := int64(C.cfapi_get_validate_data_length(params))
		C.cfapi_ack_data_fail(
			C.CF_CONNECTION_KEY(connKey),
			C.LONGLONG(txKey),
			C.LONGLONG(off),
			C.LONGLONG(ln),
			C.NTSTATUS(-0x3FFFFFBF),
		)
		return
	}
	var path string
	if info.NormalizedPath != nil {
		path = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(info.NormalizedPath)))
	}
	var identity []byte
	if info.FileIdentity != nil && info.FileIdentityLength > 0 {
		identity = C.GoBytes(unsafe.Pointer(info.FileIdentity), C.int(info.FileIdentityLength))
	}
	req := ValidateDataRequest{
		ConnectionKey:    connKey,
		TransferKey:      txKey,
		NormalizedPath:   path,
		FileIdentity:     identity,
		RequiredOffset:   int64(C.cfapi_get_validate_data_offset(params)),
		RequiredLength:   int64(C.cfapi_get_validate_data_length(params)),
	}
	vs := &ValidateSession{connKey: connKey, txKey: txKey}
	if err := s.prov.ValidateData(req, vs); err != nil {
		s.log.Error("cfapi: ValidateData failed", "path", path, "err", err)
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
		return hex.EncodeToString(b[:8]) + "..."
	}
	return hex.EncodeToString(b)
}
