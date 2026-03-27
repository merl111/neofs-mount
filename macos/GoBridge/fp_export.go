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
	"sync"
	"time"

	"log/slog"

	"github.com/mathias/neofs-mount/internal/neofs"
)

var (
	fpMu     sync.Mutex
	fpClient *neofs.Client
	fpLog    = slog.Default()
)

//export NeoFsFpVersion
func NeoFsFpVersion() C.int {
	return 3
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

func main() {}
