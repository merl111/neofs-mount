package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mathias/neofs-mount/internal/config"
	"github.com/mathias/neofs-mount/internal/fs"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/nspcc-dev/neofs-sdk-go/object"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
)

func pathHasPrefixFold(path, prefix string) bool {
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	lp := strings.ToLower(path)
	lpr := strings.ToLower(prefix)
	if lp == lpr {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(lpr, sep) {
		lpr += sep
	}
	return strings.HasPrefix(lp, lpr)
}

func pathBasedNeoFSAttrsReport(absTarget, absMount string, cfg *config.FileConfig) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	neo, err := neofs.New(ctx, neofs.Params{
		Endpoint:  *cfg.Endpoint,
		WalletKey: *cfg.WalletKey,
	})
	if err != nil {
		return fmt.Sprintf("NeoFS connection error:\n%v", err), false
	}
	defer neo.Close()

	_, aliasMap, err := fs.ListContainersForUI(ctx, neo, fs.IgnoreSetFromIDs(cfg.IgnoreContainerIDs))
	if err != nil {
		return fmt.Sprintf("Could not list containers:\n%v", err), false
	}

	cnrStr, neoRel, ok := fs.MountDiskToNeoUploadWithUI(absMount, absTarget, aliasMap)
	if !ok {
		return fmt.Sprintf("Could not map this path to container/object layout under the mount.\n\nPath: %s", absTarget), true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Path\n  %s\n\n", absTarget)
	fmt.Fprintf(&b, "Kind\n  Regular file on disk (FUSE view)\n\n")
	fmt.Fprintf(&b, "NeoFS mapping\n  Container: %s\n  Object path: %s\n\n", cnrStr, neoRel)

	var cnr cid.ID
	if err := cnr.DecodeString(cnrStr); err != nil {
		fmt.Fprintf(&b, "Invalid container id: %v", err)
		return b.String(), false
	}

	ids, err := neo.FindObjectIDsByExactPath(ctx, cnr, neoRel)
	if err != nil {
		fmt.Fprintf(&b, "Lookup error:\n%v", err)
		return b.String(), false
	}
	if len(ids) == 0 {
		fmt.Fprintf(&b, "NeoFS object\n  No object with this FilePath/Key yet (not uploaded or still syncing).\n")
		return b.String(), true
	}
	if len(ids) > 1 {
		fmt.Fprintf(&b, "Note: multiple objects match this path (%d); showing the first.\n\n", len(ids))
	}
	hdr, err := neo.ObjectHead(ctx, cnr, ids[0])
	if err != nil {
		fmt.Fprintf(&b, "ObjectHead error:\n%v", err)
		return b.String(), false
	}
	writeObjectHeadFromHDR(&b, ids[0], hdr)
	return b.String(), true
}

func writeObjectHeadFromHDR(b *strings.Builder, objectID oid.ID, hdr *object.Object) {
	fmt.Fprintf(b, "Object ID\n  %s\n\n", objectID.EncodeToString())

	if v := hdr.Version(); v != nil {
		fmt.Fprintf(b, "Header version\n  %s\n\n", v.String())
	}
	fmt.Fprintf(b, "Type\n  %s\n\n", hdr.Type().String())
	fmt.Fprintf(b, "Owner\n  %s\n\n", hdr.Owner().EncodeToString())
	fmt.Fprintf(b, "Creation epoch\n  %d\n\n", hdr.CreationEpoch())
	fmt.Fprintf(b, "Payload size\n  %d bytes\n\n", hdr.PayloadSize())

	if cs, ok := hdr.PayloadChecksum(); ok {
		fmt.Fprintf(b, "Payload checksum\n  %s\n\n", cs.String())
	}
	if hh, ok := hdr.PayloadHomomorphicHash(); ok {
		fmt.Fprintf(b, "Homomorphic hash\n  %s\n\n", hh.String())
	}

	attrs := hdr.Attributes()
	if len(attrs) == 0 {
		fmt.Fprintf(b, "User attributes\n  (none)\n")
		return
	}

	keys := make([]string, 0, len(attrs))
	seen := make(map[string]string, len(attrs))
	for _, at := range attrs {
		k := at.Key()
		if k == "" {
			continue
		}
		keys = append(keys, k)
		seen[k] = at.Value()
	}
	sort.Strings(keys)

	fmt.Fprintf(b, "User attributes (%d)\n", len(keys))
	for _, k := range keys {
		v := seen[k]
		if strings.ContainsAny(v, "\n\r") {
			fmt.Fprintf(b, "  %s:\n    %q\n", k, v)
		} else {
			fmt.Fprintf(b, "  %s: %s\n", k, v)
		}
	}
}

func splitIdentityAtColon(s string) (before, after string) {
	for i, c := range s {
		if c == ':' {
			return s[:i], s[i+1:]
		}
	}
	return "", s
}

func pinStateName(v uint32) string {
	switch v {
	case 0:
		return "Unspecified"
	case 1:
		return "Pinned"
	case 2:
		return "Unpinned"
	case 3:
		return "Excluded"
	case 4:
		return "Inherit"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func inSyncName(v uint32) string {
	switch v {
	case 0:
		return "Not in sync (Explorer shows pending activity; upload/hydration/errors use this state)"
	case 1:
		return "In sync (Explorer shows up-to-date cloud state)"
	default:
		return fmt.Sprintf("%d", v)
	}
}
