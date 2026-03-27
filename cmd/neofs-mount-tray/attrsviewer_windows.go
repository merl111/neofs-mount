//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mathias/neofs-mount/internal/cfapi"
	"github.com/mathias/neofs-mount/internal/config"
	"github.com/mathias/neofs-mount/internal/neofs"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
)

func buildNeoFSAttrsReport(targetPath string) (string, bool) {
	cfgPath := config.DefaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Sprintf("Error: could not load config from %s:\n%v", cfgPath, err), false
	}
	if cfg.Mountpoint == nil || *cfg.Mountpoint == "" {
		return "Error: mountpoint is not set in config.", false
	}
	if cfg.Endpoint == nil || cfg.WalletKey == nil {
		return "Error: endpoint or wallet key missing in config.", false
	}

	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}
	absMount, err := filepath.Abs(*cfg.Mountpoint)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}
	if !pathHasPrefixFold(absTarget, absMount) {
		return fmt.Sprintf("Not under NeoFS mount.\n\nMount: %s\nPath:  %s", absMount, absTarget), false
	}

	ph, err := cfapi.ReadPlaceholderBasic(absTarget)
	if err != nil {
		if errors.Is(err, cfapi.ErrNotACloudFile) {
			return pathBasedNeoFSAttrsReport(absTarget, absMount, cfg)
		}
		return fmt.Sprintf("Not a NeoFS cloud placeholder (or could not read placeholder info):\n%v\n\nIf this path is inside your NeoFS mount, try again after the folder has finished syncing.", err), false
	}

	idStr := string(ph.FileIdentity)
	cnrStr, tail := splitIdentityAtColon(idStr)

	var b strings.Builder
	fmt.Fprintf(&b, "Path\n  %s\n\n", absTarget)
	fmt.Fprintf(&b, "Placeholder\n  Pin state:    %s\n  In-sync:      %s\n\n", pinStateName(ph.PinState), inSyncName(ph.InSyncState))
	fmt.Fprintf(&b, "FileIdentity\n  %q\n\n", idStr)

	if cnrStr == "" {
		fmt.Fprintf(&b, "Could not parse container id from FileIdentity.")
		return b.String(), true
	}

	fmt.Fprintf(&b, "Container ID\n  %s\n\n", cnrStr)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	neo, err := neofs.New(ctx, neofs.Params{
		Endpoint:  *cfg.Endpoint,
		WalletKey: *cfg.WalletKey,
	})
	if err != nil {
		fmt.Fprintf(&b, "NeoFS connection error:\n%v", err)
		return b.String(), false
	}
	defer neo.Close()

	var cnr cid.ID
	if err := cnr.DecodeString(cnrStr); err != nil {
		fmt.Fprintf(&b, "Invalid container id: %v", err)
		return b.String(), false
	}

	if tail == "" || strings.HasPrefix(tail, "dir:") {
		if tail == "" {
			fmt.Fprintf(&b, "Kind\n  Container root (directory placeholder)\n")
		} else {
			fmt.Fprintf(&b, "Kind\n  Directory placeholder\n  Relative path: %s\n", strings.TrimPrefix(tail, "dir:"))
		}
		return b.String(), true
	}

	var objectID oid.ID
	if err := objectID.DecodeString(tail); err != nil {
		fmt.Fprintf(&b, "Invalid object id in FileIdentity: %q (%v)", tail, err)
		return b.String(), false
	}

	hdr, err := neo.ObjectHead(ctx, cnr, objectID)
	if err != nil {
		fmt.Fprintf(&b, "ObjectHead error:\n%v", err)
		return b.String(), false
	}
	writeObjectHeadFromHDR(&b, objectID, hdr)
	return b.String(), true
}
