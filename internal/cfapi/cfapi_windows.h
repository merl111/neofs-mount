// cfapi_windows.h — minimal CfApi declarations for the CGo bindings.
// Mirrors the relevant subset of <cfapi.h> from the Windows 10 SDK.
// This file is intentionally standalone to avoid depending on the full SDK
// being present at the same path on every developer machine; the linker will
// still pick up cldapi.lib via the #pragma comment in cfapi.go.

#ifndef NEOFS_CFAPI_H
#define NEOFS_CFAPI_H

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <objidl.h>   /* LPCWSTR, GUID, ... */

// ── provider registration ─────────────────────────────────────────────────

typedef struct CF_SYNC_REGISTRATION {
    DWORD     StructSize;
    LPCWSTR   ProviderName;
    LPCWSTR   ProviderVersion;
    GUID      ProviderId;
} CF_SYNC_REGISTRATION;

typedef enum CF_POPULATION_POLICY_PRIMARY {
    CF_POPULATION_POLICY_PARTIAL    = 0,
    CF_POPULATION_POLICY_FULL       = 2,
    CF_POPULATION_POLICY_ALWAYS_FULL = 3,
} CF_POPULATION_POLICY_PRIMARY;

typedef enum CF_HYDRATION_POLICY_PRIMARY {
    CF_HYDRATION_POLICY_PARTIAL    = 0,
    CF_HYDRATION_POLICY_PROGRESSIVE = 1,
    CF_HYDRATION_POLICY_FULL        = 2,
    CF_HYDRATION_POLICY_ALWAYS_FULL = 3,
} CF_HYDRATION_POLICY_PRIMARY;

typedef struct CF_HYDRATION_POLICY {
    CF_HYDRATION_POLICY_PRIMARY  Primary;
    WORD                         Modifier;
} CF_HYDRATION_POLICY;

typedef struct CF_POPULATION_POLICY {
    CF_POPULATION_POLICY_PRIMARY Primary;
    WORD                         Modifier;
} CF_POPULATION_POLICY;

typedef struct CF_SYNC_POLICIES {
    DWORD                StructSize;
    CF_HYDRATION_POLICY  Hydration;
    CF_POPULATION_POLICY Population;
    DWORD                InSyncPolicy;
    DWORD                HardLinkPolicy;
    DWORD                PlaceholderManagement;
} CF_SYNC_POLICIES;

HRESULT WINAPI CfRegisterSyncRoot(
    LPCWSTR                       SyncRootPath,
    const CF_SYNC_REGISTRATION*   Registration,
    const CF_SYNC_POLICIES*       Policies,
    DWORD                         RegisterFlags
);

HRESULT WINAPI CfUnregisterSyncRoot(
    LPCWSTR SyncRootPath
);

// ── connection / callbacks ─────────────────────────────────────────────────

typedef LONGLONG CF_CONNECTION_KEY;

typedef enum CF_CALLBACK_TYPE {
    CF_CALLBACK_TYPE_FETCH_DATA           = 0,
    CF_CALLBACK_TYPE_VALIDATE_DATA        = 1,
    CF_CALLBACK_TYPE_CANCEL_FETCH_DATA    = 2,
    CF_CALLBACK_TYPE_FETCH_PLACEHOLDERS   = 3,
    CF_CALLBACK_TYPE_CANCEL_FETCH_PLACEHOLDERS = 4,
    CF_CALLBACK_TYPE_OPEN_COMPLETION      = 5,
    CF_CALLBACK_TYPE_CLOSE_COMPLETION     = 6,
    CF_CALLBACK_TYPE_DEHYDRATE            = 7,
    CF_CALLBACK_TYPE_DEHYDRATE_COMPLETION = 8,
    CF_CALLBACK_TYPE_DELETE               = 9,
    CF_CALLBACK_TYPE_DELETE_COMPLETION    = 10,
    CF_CALLBACK_TYPE_RENAME               = 11,
    CF_CALLBACK_TYPE_RENAME_COMPLETION    = 12,
    CF_CALLBACK_TYPE_NONE                 = 0xFFFFFFFF,
} CF_CALLBACK_TYPE;

typedef struct CF_CALLBACK_INFO {
    DWORD               StructSize;
    CF_CONNECTION_KEY   ConnectionKey;
    LPVOID              CallbackContext;
    LPCWSTR             VolumeDosName;
    LPCWSTR             VolumeGuidName;
    LPCWSTR             VolumeName;
    DWORD               VolumeSerialNumber;
    LARGE_INTEGER       SyncRootFileId;
    LPCVOID             SyncRootIdentity;
    DWORD               SyncRootIdentityLength;
    LARGE_INTEGER       FileId;
    LARGE_INTEGER       FileSize;
    LPCVOID             FileIdentity;
    DWORD               FileIdentityLength;
    LPCWSTR             NormalizedPath;
    LARGE_INTEGER       TransferKey;
    BYTE                PriorityHint;
    PKOROUTINE_WAIT_BLOCK CorrelationVector; /* opaque for us */
    LPVOID              ProcessInfo;
    LARGE_INTEGER       RequestKey;
} CF_CALLBACK_INFO;

typedef struct CF_CALLBACK_PARAMETERS {
    DWORD   ParamSize;
    union {
        struct {
            LARGE_INTEGER RequiredFileOffset;
            LARGE_INTEGER RequiredLength;
            LARGE_INTEGER OptionalFileOffset;
            LARGE_INTEGER OptionalLength;
            LARGE_INTEGER LastDehydrationTime;
            DWORD         LastDehydrationReason;
        } FetchData;
        struct {
            LARGE_INTEGER FileOffset;
            LARGE_INTEGER Length;
        } ValidateData;
        struct {
            DWORD Flags;
        } FetchPlaceholders;
        DWORD RawParams[32]; /* safe padding */
    };
} CF_CALLBACK_PARAMETERS;

typedef VOID (*CF_CALLBACK)(
    CONST CF_CALLBACK_INFO*        CallbackInfo,
    CONST CF_CALLBACK_PARAMETERS*  CallbackParameters
);

typedef struct CF_CALLBACK_REGISTRATION {
    CF_CALLBACK_TYPE Type;
    CF_CALLBACK      Callback;
} CF_CALLBACK_REGISTRATION;

#define CF_CALLBACK_REGISTRATION_END { CF_CALLBACK_TYPE_NONE, NULL }

HRESULT WINAPI CfConnectSyncRoot(
    LPCWSTR                        SyncRootPath,
    const CF_CALLBACK_REGISTRATION* CallbackTable,
    LPVOID                          CallbackContext,
    DWORD                           ConnectFlags,
    CF_CONNECTION_KEY*              ConnectionKey
);

HRESULT WINAPI CfDisconnectSyncRoot(
    CF_CONNECTION_KEY ConnectionKey
);

// ── placeholders ──────────────────────────────────────────────────────────

typedef LONGLONG CF_TRANSFER_KEY;

typedef struct CF_FS_METADATA {
    FILETIME BasicInfo_CreationTime;
    FILETIME BasicInfo_LastAccessTime;
    FILETIME BasicInfo_LastWriteTime;
    FILETIME BasicInfo_ChangeTime;
    DWORD    BasicInfo_FileAttributes;
    LARGE_INTEGER FileSize;
} CF_FS_METADATA;

typedef struct CF_PLACEHOLDER_CREATE_INFO {
    LPCWSTR         RelativeFileName;
    CF_FS_METADATA  FsMetadata;
    LPCVOID         FileIdentity;
    DWORD           FileIdentityLength;
    DWORD           Flags;
    HRESULT         Result;
} CF_PLACEHOLDER_CREATE_INFO;

HRESULT WINAPI CfCreatePlaceholders(
    LPCWSTR                       BaseDirectoryPath,
    CF_PLACEHOLDER_CREATE_INFO*   PlaceholderArray,
    DWORD                         PlaceholderCount,
    DWORD                         CreateFlags,
    LPDWORD                       EntriesProcessed
);

// ── transfer / hydration ──────────────────────────────────────────────────

typedef enum CF_OPERATION_TYPE {
    CF_OPERATION_TYPE_TRANSFER_DATA        = 0,
    CF_OPERATION_TYPE_RETRIEVE_DATA        = 1,
    CF_OPERATION_TYPE_ACK_DATA             = 2,
    CF_OPERATION_TYPE_RESTART_HYDRATION    = 3,
    CF_OPERATION_TYPE_TRANSFER_PLACEHOLDERS = 4,
    CF_OPERATION_TYPE_ACK_DEHYDRATE        = 5,
    CF_OPERATION_TYPE_ACK_DELETE           = 6,
    CF_OPERATION_TYPE_ACK_RENAME           = 7,
} CF_OPERATION_TYPE;

typedef struct CF_OPERATION_INFO {
    DWORD             StructSize;
    CF_OPERATION_TYPE Type;
    CF_CONNECTION_KEY ConnectionKey;
    CF_TRANSFER_KEY   TransferKey;
    CORRELATION_VECTOR CorrelationVector; /* opaque 40 bytes */
    LPVOID            RequestKey;
} CF_OPERATION_INFO;

typedef struct CF_OPERATION_PARAMETERS {
    DWORD ParamSize;
    union {
        struct {
            DWORD         Flags;
            NTSTATUS      CompletionStatus;
            LPCVOID       Buffer;
            LARGE_INTEGER Offset;
            LARGE_INTEGER Length;
        } TransferData;
        DWORD RawParams[32];
    };
} CF_OPERATION_PARAMETERS;

HRESULT WINAPI CfExecute(
    const CF_OPERATION_INFO*       OpInfo,
    CF_OPERATION_PARAMETERS*       OpParams
);

// ── open/close (for TransferKey) ──────────────────────────────────────────

HRESULT WINAPI CfOpenFileWithOplock(
    LPCWSTR      FilePath,
    DWORD        Flags,
    HANDLE*      ProtectedHandle
);

VOID WINAPI CfCloseHandle(HANDLE FileHandle);

HRESULT WINAPI CfGetTransferKey(
    HANDLE           FileHandle,
    CF_TRANSFER_KEY* TransferKey
);

VOID WINAPI CfReleaseTransferKey(
    HANDLE           FileHandle,
    CF_TRANSFER_KEY* TransferKey
);

#endif /* NEOFS_CFAPI_H */
