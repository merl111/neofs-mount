//go:build windows

package cfapi

/*
#include <stdint.h>
#include "cfapi_windows.h"

static HRESULT cfapi_read_placeholder_basic(uintptr_t fileHandle, PVOID buf, DWORD bufLen, DWORD* retLen) {
	return CfGetPlaceholderInfo((HANDLE)fileHandle, CF_PLACEHOLDER_INFO_BASIC, buf, bufLen, retLen);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrNotACloudFile is returned (wrapped) when CfGetPlaceholderInfo fails with
// ERROR_NOT_A_CLOUD_FILE — the path is a normal file on disk, not a Cloud Files placeholder.
var ErrNotACloudFile = errors.New("cfapi: not a cloud file")

const hresultNotACloudFile = 0x80070178 // HRESULT_FROM_WIN32(ERROR_NOT_A_CLOUD_FILE)

// PlaceholderBasic holds CfGetPlaceholderInfo(CF_PLACEHOLDER_INFO_BASIC) fields we care about.
type PlaceholderBasic struct {
	PinState     uint32
	InSyncState  uint32
	FileIdentity []byte
}

// ReadPlaceholderBasic opens path (file or directory) and reads cloud placeholder identity + pin state.
// Requires READ_ATTRIBUTES; directories need FILE_FLAG_BACKUP_SEMANTICS.
func ReadPlaceholderBasic(path string) (*PlaceholderBasic, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("cfapi: open %q: %w", path, err)
	}
	defer windows.CloseHandle(h)

	var buf C.CF_PLACEHOLDER_BASIC_INFO
	var retLen C.DWORD
	hr := C.cfapi_read_placeholder_basic(
		C.uintptr_t(uintptr(h)),
		(C.PVOID)(unsafe.Pointer(&buf)),
		C.DWORD(unsafe.Sizeof(buf)),
		&retLen,
	)
	if hr != 0 {
		u := uint32(hr)
		if u == hresultNotACloudFile {
			return nil, fmt.Errorf("%w (CfGetPlaceholderInfo HRESULT 0x%08x)", ErrNotACloudFile, u)
		}
		return nil, fmt.Errorf("cfapi: CfGetPlaceholderInfo HRESULT 0x%08x", u)
	}
	n := int(buf.FileIdentityLength)
	if n <= 0 {
		return &PlaceholderBasic{
			PinState:    uint32(buf.PinState),
			InSyncState: uint32(buf.InSyncState),
		}, nil
	}
	const maxIdentity = 4096
	if n > maxIdentity {
		n = maxIdentity
	}
	id := C.GoBytes(unsafe.Pointer(&buf.FileIdentity[0]), C.int(n))
	return &PlaceholderBasic{
		PinState:     uint32(buf.PinState),
		InSyncState:  uint32(buf.InSyncState),
		FileIdentity: id,
	}, nil
}
