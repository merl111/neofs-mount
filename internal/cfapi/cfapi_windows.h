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

/* NTSTATUS is not defined by MinGW's windows.h when WIN32_LEAN_AND_MEAN is set */
#ifndef NTSTATUS
typedef LONG NTSTATUS;
#endif

// ── provider registration ─────────────────────────────────────────────────

typedef struct CF_SYNC_REGISTRATION {
    DWORD     StructSize;
    LPCWSTR   ProviderName;
    LPCWSTR   ProviderVersion;
    LPCVOID   SyncRootIdentity;
    DWORD     SyncRootIdentityLength;
    LPCVOID   FileIdentity;
    DWORD     FileIdentityLength;
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

#define CF_INSYNC_POLICY_NONE                               0x00000000
#define CF_INSYNC_POLICY_PRESERVE_INSYNC_FOR_SYNC_ENGINE 0x80000000

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

#ifndef CF_REGISTER_FLAG_UPDATE
#define CF_REGISTER_FLAG_UPDATE 0x00000001
#endif

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
    LPCWSTR             VolumeGuidName;
    LPCWSTR             VolumeDosName;
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
    CONST void*         CorrelationVector; /* opaque for us */
    LPVOID              ProcessInfo;
    LARGE_INTEGER       RequestKey;
} CF_CALLBACK_INFO;

/* Layout must match Windows SDK cfapi.h — the first DWORD of FetchData is Flags,
 * not RequiredFileOffset. An incorrect layout reads garbage offsets/lengths and can
 * crash cldapi / Explorer. */
typedef struct CF_CALLBACK_PARAMETERS {
    DWORD   ParamSize;
    union {
        struct {
            DWORD         Flags;
            LARGE_INTEGER RequiredFileOffset;
            LARGE_INTEGER RequiredLength;
            LARGE_INTEGER OptionalFileOffset;
            LARGE_INTEGER OptionalLength;
            LARGE_INTEGER LastDehydrationTime;
            DWORD         LastDehydrationReason;
        } FetchData;
        struct {
            DWORD         Flags;
            LARGE_INTEGER RequiredFileOffset;
            LARGE_INTEGER RequiredLength;
        } ValidateData;
        struct {
            DWORD   Flags;
            LPCWSTR Pattern;
        } FetchPlaceholders;
        struct {
            DWORD Flags;
        } CloseCompletion;
        struct {
            DWORD Flags;
        } Delete;
        struct {
            DWORD Flags;
        } DeleteCompletion;
        struct {
            DWORD   Flags;
            LPCWSTR TargetPath;
        } Rename;
        struct {
            DWORD   Flags;
            LPCWSTR SourcePath;
        } RenameCompletion;
        DWORD RawParams[32];
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

#define CF_PLACEHOLDER_CREATE_FLAG_NONE                           0x00000000
#define CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC                  0x00000001
#define CF_PLACEHOLDER_CREATE_FLAG_SUPERSEDE                     0x00000002
#define CF_PLACEHOLDER_CREATE_FLAG_DISABLE_ON_DEMAND_POPULATION  0x00000008

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

#define CF_OPERATION_TRANSFER_PLACEHOLDERS_FLAG_NONE                              0
#define CF_OPERATION_TRANSFER_PLACEHOLDERS_FLAG_DISABLE_ON_DEMAND_POPULATION    0x00000001

typedef struct CF_OPERATION_INFO {
    DWORD             StructSize;
    CF_OPERATION_TYPE Type;
    CF_CONNECTION_KEY ConnectionKey;
    CF_TRANSFER_KEY   TransferKey;
    CONST void*       CorrelationVector;
    CONST void*       SyncStatus;
    LONGLONG          RequestKey;
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
        struct {
            DWORD         Flags;
            NTSTATUS      CompletionStatus;
            LARGE_INTEGER PlaceholderTotalCount;
            CF_PLACEHOLDER_CREATE_INFO* PlaceholderArray;
            DWORD         PlaceholderCount;
            DWORD         EntriesProcessed;
        } TransferPlaceholders;
        struct {
            DWORD         Flags;
            NTSTATUS      CompletionStatus;
            LARGE_INTEGER Offset;
            LARGE_INTEGER Length;
        } AckData;
        struct {
            DWORD    Flags;
            NTSTATUS CompletionStatus;
        } AckDelete;
        struct {
            DWORD    Flags;
            NTSTATUS CompletionStatus;
        } AckRename;
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

// ── placeholder query (Explorer context menu / diagnostics) ─────────────

typedef enum CF_PLACEHOLDER_INFO_CLASS {
    CF_PLACEHOLDER_INFO_BASIC = 0,
    CF_PLACEHOLDER_INFO_STANDARD = 1,
} CF_PLACEHOLDER_INFO_CLASS;

typedef enum CF_PIN_STATE {
    CF_PIN_STATE_UNSPECIFIED = 0,
    CF_PIN_STATE_PINNED = 1,
    CF_PIN_STATE_UNPINNED = 2,
    CF_PIN_STATE_EXCLUDED = 3,
    CF_PIN_STATE_INHERIT = 4,
} CF_PIN_STATE;

typedef enum CF_IN_SYNC_STATE {
    CF_IN_SYNC_STATE_NOT_IN_SYNC = 0,
    CF_IN_SYNC_STATE_IN_SYNC = 1,
} CF_IN_SYNC_STATE;

typedef enum CF_SET_INSYNC_FLAGS {
    CF_SET_INSYNC_FLAG_NONE = 0,
} CF_SET_INSYNC_FLAGS;

HRESULT WINAPI CfSetInSyncState(
    HANDLE FileHandle,
    CF_IN_SYNC_STATE InSyncState,
    CF_SET_INSYNC_FLAGS InSyncFlags,
    USN* InoutSyncUsn
);

#define CFAPI_MAX_FILE_IDENTITY 4096

typedef struct CF_PLACEHOLDER_BASIC_INFO {
    CF_PIN_STATE PinState;
    CF_IN_SYNC_STATE InSyncState;
    LARGE_INTEGER FileId;
    LARGE_INTEGER SyncRootFileId;
    ULONG FileIdentityLength;
    BYTE FileIdentity[CFAPI_MAX_FILE_IDENTITY];
} CF_PLACEHOLDER_BASIC_INFO;

HRESULT WINAPI CfGetPlaceholderInfo(
    HANDLE FileHandle,
    CF_PLACEHOLDER_INFO_CLASS InfoClass,
    PVOID InfoBuffer,
    DWORD InfoBufferLength,
    LPDWORD ReturnedLength
);

// ── convert regular file to cloud placeholder ───────────────────────────

typedef DWORD CF_CONVERT_FLAGS;
#define CF_CONVERT_FLAG_NONE                           0x00000000
#define CF_CONVERT_FLAG_MARK_IN_SYNC                   0x00000001
#define CF_CONVERT_FLAG_DEHYDRATE                      0x00000002
#define CF_CONVERT_FLAG_ENABLE_ON_DEMAND_POPULATION    0x00000004
#define CF_CONVERT_FLAG_ALWAYS_FULL                    0x00000008

HRESULT WINAPI CfConvertToPlaceholder(
    HANDLE             FileHandle,
    LPCVOID            FileIdentity,
    DWORD              FileIdentityLength,
    CF_CONVERT_FLAGS   ConvertFlags,
    LONG_PTR*          ConvertUsn,
    LPOVERLAPPED       Overlapped
);

#endif /* NEOFS_CFAPI_H */
