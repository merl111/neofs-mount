package neofs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nspcc-dev/neo-go/pkg/crypto/keys"
	"github.com/nspcc-dev/neofs-sdk-go/client"
	"github.com/nspcc-dev/neofs-sdk-go/container"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	neofsecdsa "github.com/nspcc-dev/neofs-sdk-go/crypto/ecdsa"
	"github.com/nspcc-dev/neofs-sdk-go/object"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
	"github.com/nspcc-dev/neofs-sdk-go/session"
	"github.com/nspcc-dev/neofs-sdk-go/user"
)

type Client struct {
	log *slog.Logger

	c      *client.Client // read connection: LIST, HEAD, GET, SEARCH
	cw     *client.Client // write connection: ObjectPut only (separate from reads)
	signer user.Signer

	mu             sync.Mutex
	sessionCache   map[sessionCacheKey]cachedSession
	searchStrategy map[string]int // container ID -> searchStrategy* constant

	scanMu       sync.Mutex
	scanCache    map[string]cachedScanEntries
	scanInflight map[string]*scanFlight
}

// searchStrategy* constants track which search method works per container.
const (
	searchStrategyUnknown    = 0
	searchStrategyV2Root     = 1 // SearchV2 with root filter
	searchStrategyV2All      = 2 // SearchV2 without filter
	searchStrategyStreamRoot = 3 // legacy ObjectSearch stream with root filter
	searchStrategyStreamAll  = 4 // legacy stream without filter
)

type cachedScanEntries struct {
	at      time.Time
	entries []SearchEntry
	trie    *listingTrie // immediate children per NeoFS path prefix; built with entries
}

// listingTrie indexes object paths for O(depth) directory listing without scanning all entries.
type listingTrie struct {
	root *listingTrieNode
}

type listingTrieNode struct {
	children map[string]*listingTrieNode
	order    []string // stable child name order (insertion order)
	file     *SearchEntry
}

func entryPathKey(e *SearchEntry) string {
	key := e.FilePath
	if key == "" {
		key = e.Key
	}
	if key == "" {
		key = e.Name
	}
	if key == "" {
		key = e.FileName
	}
	if key == "" {
		return ""
	}
	return strings.TrimPrefix(key, "/")
}

// normalizeNeoFSPrefix converts a caller prefix (e.g. from filepath.Join) to NeoFS slash form.
func normalizeNeoFSPrefix(prefix string) string {
	prefix = filepath.ToSlash(strings.TrimSpace(prefix))
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

func buildListingTrie(entries []SearchEntry) *listingTrie {
	t := &listingTrie{root: &listingTrieNode{}}
	for i := range entries {
		key := entryPathKey(&entries[i])
		if key == "" {
			continue
		}
		segs := strings.Split(key, "/")
		var nonE []string
		for _, s := range segs {
			if s != "" {
				nonE = append(nonE, s)
			}
		}
		if len(nonE) == 0 {
			continue
		}
		t.insertSegments(nonE, &entries[i])
	}
	return t
}

func (t *listingTrie) insertSegments(segs []string, e *SearchEntry) {
	cur := t.root
	for i := 0; i < len(segs); i++ {
		s := segs[i]
		if cur.children == nil {
			cur.children = make(map[string]*listingTrieNode)
		}
		next := cur.children[s]
		if next == nil {
			next = &listingTrieNode{}
			cur.children[s] = next
			cur.order = append(cur.order, s)
		}
		cur = next
		if i < len(segs)-1 {
			// Longer paths make this segment a directory; drop a file parked at an intermediate.
			cur.file = nil
		}
	}
	// File leaf only if nothing already lives beneath this path (matches linear scan first-wins).
	if len(cur.children) == 0 && cur.file == nil {
		cur.file = e
	}
}

func (t *listingTrie) listImmediateChildren(prefix string) []DirEntry {
	prefix = normalizeNeoFSPrefix(prefix)
	cur := t.root
	if prefix != "" {
		segs := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
		for _, s := range segs {
			if s == "" {
				continue
			}
			if cur.children == nil {
				return nil
			}
			next := cur.children[s]
			if next == nil {
				return nil
			}
			cur = next
		}
	}

	var out []DirEntry
	for _, name := range cur.order {
		ch := cur.children[name]
		if ch == nil {
			continue
		}
		if len(ch.children) > 0 {
			out = append(out, DirEntry{Name: name, IsDirectory: true})
			continue
		}
		if ch.file != nil {
			out = append(out, DirEntry{
				Name:     name,
				ObjectID: ch.file.ObjectID.EncodeToString(),
				Size:     ch.file.Size,
			})
			continue
		}
	}
	return out
}

// scanFlight lets parallel ListEntriesByHeadScan callers wait on one object listing + head pass.
type scanFlight struct {
	done chan struct{}

	mu  sync.Mutex
	err error // set when listing/heads fail (no cache entry)
}

func (f *scanFlight) waitErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *scanFlight) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

type sessionCacheKey struct {
	container string
	verb      session.ObjectVerb
}

type cachedSession struct {
	epochExp uint64
	token    session.Object
}

type Params struct {
	Logger    *slog.Logger
	Endpoint  string
	WalletKey string // either WIF string, or path to a file containing WIF
}

func New(ctx context.Context, p Params) (*Client, error) {
	if p.Endpoint == "" {
		return nil, errors.New("neofs: empty endpoint")
	}
	if p.WalletKey == "" {
		return nil, errors.New("neofs: empty wallet key (WIF or path)")
	}

	log := p.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	pk, err := parseWIFOrPath(p.WalletKey)
	if err != nil {
		return nil, err
	}

	signer := user.NewAutoIDSignerRFC6979(pk.PrivateKey)

	var prmInit client.PrmInit
	c, err := client.New(prmInit)
	if err != nil {
		return nil, fmt.Errorf("neofs: client init: %w", err)
	}

	var prmDial client.PrmDial
	prmDial.SetServerURI(p.Endpoint)
	prmDial.SetContext(ctx)
	prmDial.SetTimeout(15 * time.Second)
	// Use a very large stream timeout. Removing the call entirely reverts to the SDK's
	// ~60s default which silently kills large-file uploads mid-transfer.
	prmDial.SetStreamTimeout(72 * time.Hour)
	if err := c.Dial(prmDial); err != nil {
		return nil, fmt.Errorf("neofs: dial: %w", err)
	}

	// Write connection: dedicated to ObjectPut so large upload streams
	// don't block concurrent reads (LIST, HEAD, SEARCH) on the main connection.
	var prmInitW client.PrmInit
	cw, err := client.New(prmInitW)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("neofs: write client init: %w", err)
	}
	var prmDialW client.PrmDial
	prmDialW.SetServerURI(p.Endpoint)
	prmDialW.SetContext(ctx)
	prmDialW.SetTimeout(15 * time.Second)
	prmDialW.SetStreamTimeout(72 * time.Hour)
	if err := cw.Dial(prmDialW); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("neofs: write dial: %w", err)
	}

	return &Client{
		log:            log,
		c:              c,
		cw:             cw,
		signer:         signer,
		sessionCache:   make(map[sessionCacheKey]cachedSession),
		searchStrategy: make(map[string]int),
		scanCache:      make(map[string]cachedScanEntries),
		scanInflight:   make(map[string]*scanFlight),
	}, nil
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var errs []error
	if c.c != nil {
		errs = append(errs, c.c.Close())
	}
	if c.cw != nil {
		errs = append(errs, c.cw.Close())
	}
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// Balance retrieves the NeoFS account balance for the configured wallet.
func (c *Client) Balance(ctx context.Context) (int64, uint32, error) {
	var prm client.PrmBalanceGet
	prm.SetAccount(c.signer.UserID())
	dec, err := c.c.BalanceGet(ctx, prm)
	if err != nil {
		return 0, 0, err
	}
	return dec.Value(), dec.Precision(), nil
}

func (c *Client) Signer() user.Signer { return c.signer }

func (c *Client) ListContainers(ctx context.Context) ([]cid.ID, error) {
	return c.c.ContainerList(ctx, c.signer.UserID(), client.PrmContainerList{})
}

func (c *Client) ContainerGet(ctx context.Context, id cid.ID) (container.Container, error) {
	return c.c.ContainerGet(ctx, id, client.PrmContainerGet{})
}

type SearchEntry struct {
	ObjectID oid.ID
	FilePath string            // as stored in object.AttributeFilePath (often leading '/')
	FileName string            // as stored in object.AttributeFileName (may be empty)
	Name     string            // as stored in object.AttributeName (may be empty)
	Key      string            // S3-gateway "Key" attribute (may be empty)
	Size     int64             // object.FilterPayloadSize if requested
	Time     time.Time         // from Timestamp or LastModified
	Attrs    map[string]string // all object attributes
}

// headScanCacheTTL mirrors how S3 FUSE tools like goofys handle immutable stores:
// objects never change once written, so a long TTL only means slightly stale listings.
const headScanCacheTTL = 5 * time.Minute

const headScanWorkers = 32

// InvalidateContainerScan drops cached results from [Client.ListEntriesByHeadScan] for a container.
func (c *Client) InvalidateContainerScan(containerID cid.ID) {
	if c == nil {
		return
	}
	key := containerID.EncodeToString()
	c.scanMu.Lock()
	delete(c.scanCache, key)
	c.scanMu.Unlock()
}

// ListEntriesByHeadScan lists every object in the container (root objects first, then without ROOT filter),
// fetches each header, and derives [SearchEntry] rows from attributes. It is used when SearchObjects
// filters on FilePath/FileName/Name return nothing — for example when objects use only custom attributes
// or the index does not match our queries.
//
// The second return value is true only when this goroutine performed the listing/head RPCs (not a TTL
// cache hit and not a follower waiting on an in-flight scan). Callers use it to avoid duplicate logs.
func (c *Client) ListEntriesByHeadScan(ctx context.Context, containerID cid.ID) ([]SearchEntry, bool, error) {
	if c == nil || c.c == nil {
		return nil, false, errors.New("neofs: nil client")
	}
	key := containerID.EncodeToString()
	now := time.Now()

	c.scanMu.Lock()
	if ent, ok := c.scanCache[key]; ok && now.Sub(ent.at) < headScanCacheTTL {
		out := make([]SearchEntry, len(ent.entries))
		copy(out, ent.entries)
		c.scanMu.Unlock()
		return out, false, nil
	}
	if fl, ok := c.scanInflight[key]; ok {
		c.scanMu.Unlock()
		<-fl.done
		c.scanMu.Lock()
		ent, hit := c.scanCache[key]
		c.scanMu.Unlock()
		if hit {
			out := make([]SearchEntry, len(ent.entries))
			copy(out, ent.entries)
			return out, false, nil
		}
		return nil, false, fl.waitErr()
	}

	fl := &scanFlight{done: make(chan struct{})}
	c.scanInflight[key] = fl
	c.scanMu.Unlock()

	var entries []SearchEntry
	var err error
	func() {
		defer func() {
			c.scanMu.Lock()
			if err == nil {
				c.scanCache[key] = cachedScanEntries{
					at:      time.Now(),
					entries: entries,
					trie:    buildListingTrie(entries),
				}
			} else {
				fl.setErr(err)
			}
			delete(c.scanInflight, key)
			c.scanMu.Unlock()
			close(fl.done)
		}()

		var ids []oid.ID
		ids, err = c.searchAllObjectIDs(ctx, containerID)
		if err != nil {
			return
		}
		entries, err = c.entriesFromHeadsParallel(ctx, containerID, ids)
	}()

	if err != nil {
		return nil, true, err
	}
	out := make([]SearchEntry, len(entries))
	copy(out, entries)
	return out, true, nil
}

func searchEntryFromHead(id oid.ID, hdr *object.Object) SearchEntry {
	var e SearchEntry
	e.ObjectID = id
	if hdr == nil {
		return e
	}
	e.Size = int64(hdr.PayloadSize())
	e.Attrs = make(map[string]string, len(hdr.Attributes()))
	for _, a := range hdr.Attributes() {
		k := a.Key()
		v := a.Value()
		if k != "" {
			e.Attrs[k] = v
		}
		switch k {
		case object.AttributeFilePath:
			e.FilePath = v
		case object.AttributeFileName:
			e.FileName = v
		case object.AttributeName:
			e.Name = v
		case "Key":
			e.Key = v
		case object.AttributeTimestamp, "LastModified":
			// Timestamp is usually string UNIX epoch (e.g., "1672531200") or RFC3339 ("2026-03-24T20:43:24Z")
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				e.Time = t
			} else if t, err := time.Parse(time.RFC3339, v); err == nil {
				e.Time = t
			} else if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
				e.Time = time.Unix(sec, 0)
			}
		}
	}
	// If still zero, try payload creation epoch from object metadata if available
	if e.Time.IsZero() && hdr.CreationEpoch() > 0 {
		// CreationEpoch is a NeoFS epoch, not UNIX time, so it's not a direct mapping.
		// We'll stick to explicit Timestamp/LastModified attributes.
	}
	return e
}

func (c *Client) searchAllObjectIDs(ctx context.Context, cnr cid.ID) ([]oid.ID, error) {
	var opts client.SearchObjectsOptions
	opts.SetCount(client.MaxSearchObjectsCount)

	trySearchV2 := func(root bool) ([]oid.ID, error) {
		filters := object.NewSearchFilters()
		if root {
			filters.AddRootFilter()
		}
		var all []oid.ID
		cursor := ""
		for {
			items, next, err := c.c.SearchObjects(ctx, cnr, filters, nil, cursor, c.signer, opts)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				all = append(all, it.ID)
			}
			if next == "" {
				break
			}
			cursor = next
		}
		return all, nil
	}

	// Legacy ObjectSearch stream (some deployments differ from SearchV2 behavior).
	tryStream := func(root bool) ([]oid.ID, error) {
		var prm client.PrmObjectSearch
		filters := object.NewSearchFilters()
		if root {
			filters.AddRootFilter()
		}
		prm.SetFilters(filters)
		r, err := c.c.ObjectSearchInit(ctx, cnr, c.signer, prm)
		if err != nil {
			return nil, err
		}
		var all []oid.ID
		buf := make([]oid.ID, 256)
		for {
			n, err := r.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				_ = r.Close()
				return nil, err
			}
		}
		if err := r.Close(); err != nil {
			return nil, err
		}
		return all, nil
	}

	key := cnr.EncodeToString()

	// Fast path: use the strategy that worked last time.
	c.mu.Lock()
	strat := c.searchStrategy[key]
	c.mu.Unlock()

	try := func(s int) ([]oid.ID, error) {
		switch s {
		case searchStrategyV2Root:
			return trySearchV2(true)
		case searchStrategyV2All:
			return trySearchV2(false)
		case searchStrategyStreamRoot:
			return tryStream(true)
		case searchStrategyStreamAll:
			return tryStream(false)
		}
		return nil, nil
	}

	if strat != searchStrategyUnknown {
		ids, err := try(strat)
		if err == nil && len(ids) > 0 {
			return ids, nil
		}
		// Strategy no longer works (e.g. container emptied or node upgrade); fall through.
		c.mu.Lock()
		delete(c.searchStrategy, key)
		c.mu.Unlock()
	}

	// Discovery: try all strategies in order and remember the winner.
	ordered := []int{searchStrategyV2Root, searchStrategyV2All, searchStrategyStreamRoot, searchStrategyStreamAll}
	for _, s := range ordered {
		ids, err := try(s)
		if err != nil {
			continue // try next
		}
		if len(ids) > 0 {
			c.mu.Lock()
			c.searchStrategy[key] = s
			c.mu.Unlock()
			return ids, nil
		}
	}
	return nil, nil
}

func (c *Client) entriesFromHeadsParallel(ctx context.Context, cnr cid.ID, ids []oid.ID) ([]SearchEntry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	results := make([]SearchEntry, len(ids))

	type job struct {
		idx int
		id  oid.ID
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	workers := headScanWorkers
	if n := len(ids); n < workers {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				hdr, err := c.ObjectHead(ctx, cnr, j.id)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				results[j.idx] = searchEntryFromHead(j.id, hdr)
			}
		}()
	}

	for i, id := range ids {
		jobs <- job{idx: i, id: id}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return nil, fmt.Errorf("neofs: object head during scan: %w", firstErr)
	}
	return results, nil
}

func (c *Client) ObjectGet(ctx context.Context, containerID cid.ID, objectID oid.ID) (object.Object, io.ReadCloser, error) {
	var prm client.PrmObjectGet
	hdr, r, err := c.c.ObjectGetInit(ctx, containerID, objectID, c.signer, prm)
	if err != nil {
		return object.Object{}, nil, err
	}
	return hdr, r, nil
}

// DirEntry is a lightweight directory listing entry used by the Windows CfApi adapter.
type DirEntry struct {
	Name        string
	ObjectID    string
	Size        int64
	IsDirectory bool
}

// ListEntriesByPrefix returns the immediate children of the given prefix within
// a container. It is used by the Windows CfApi adapter to populate directory
// placeholders. An empty prefix lists the top-level entries.
func (c *Client) ListEntriesByPrefix(ctx context.Context, containerIDStr, prefix string) ([]DirEntry, error) {
	var containerID cid.ID
	if err := containerID.DecodeString(containerIDStr); err != nil {
		return nil, fmt.Errorf("neofs: invalid container ID %q: %w", containerIDStr, err)
	}
	prefix = normalizeNeoFSPrefix(prefix)

	cidKey := containerID.EncodeToString()
	now := time.Now()

	c.scanMu.Lock()
	if ent, ok := c.scanCache[cidKey]; ok && now.Sub(ent.at) < headScanCacheTTL && ent.trie != nil {
		out := ent.trie.listImmediateChildren(prefix)
		if len(out) == 0 && len(ent.entries) > 0 {
			if alt := listEntriesByPrefixLinear(ent.entries, prefix); len(alt) > 0 {
				out = alt
			}
		}
		c.scanMu.Unlock()
		return out, nil
	}
	c.scanMu.Unlock()

	if _, _, err := c.ListEntriesByHeadScan(ctx, containerID); err != nil {
		return nil, err
	}

	c.scanMu.Lock()
	ent, ok := c.scanCache[cidKey]
	tr := ent.trie
	c.scanMu.Unlock()
	if !ok || tr == nil {
		entries, _, err := c.ListEntriesByHeadScan(ctx, containerID)
		if err != nil {
			return nil, err
		}
		return listEntriesByPrefixLinear(entries, prefix), nil
	}
	out := tr.listImmediateChildren(prefix)
	if len(out) == 0 && len(ent.entries) > 0 {
		if alt := listEntriesByPrefixLinear(ent.entries, prefix); len(alt) > 0 {
			out = alt
		}
	}
	return out, nil
}

func listEntriesByPrefixLinear(entries []SearchEntry, prefix string) []DirEntry {
	seen := map[string]struct{}{}
	var result []DirEntry
	for i := range entries {
		e := &entries[i]
		key := entryPathKey(e)
		if key == "" {
			continue
		}
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		if rest == "" {
			continue
		}
		if idx := strings.Index(rest, "/"); idx != -1 {
			dirName := rest[:idx]
			if dirName == "" {
				continue
			}
			if _, ok := seen[dirName]; ok {
				continue
			}
			seen[dirName] = struct{}{}
			result = append(result, DirEntry{Name: dirName, IsDirectory: true})
		} else {
			name := rest
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			result = append(result, DirEntry{
				Name:     name,
				ObjectID: e.ObjectID.EncodeToString(),
				Size:     e.Size,
			})
		}
	}
	return result
}

// FindObjectIDsByExactPath returns object IDs whose FilePath or Key attribute
// matches relPath (slash-separated, no leading slash).
func (c *Client) FindObjectIDsByExactPath(ctx context.Context, cnr cid.ID, relPath string) ([]oid.ID, error) {
	if c == nil || c.c == nil {
		return nil, errors.New("neofs: nil client")
	}
	relPath = strings.TrimPrefix(relPath, "/")
	entries, _, err := c.ListEntriesByHeadScan(ctx, cnr)
	if err != nil {
		return nil, err
	}
	var out []oid.ID
	for _, e := range entries {
		fp := strings.TrimPrefix(e.FilePath, "/")
		ky := strings.TrimPrefix(e.Key, "/")
		if fp == relPath || ky == relPath {
			out = append(out, e.ObjectID)
		}
	}
	return out, nil
}

func (c *Client) readObjectRange(ctx context.Context, containerID cid.ID, objectID oid.ID, offset, length int64) (_ []byte, err error) {
	if c == nil || c.c == nil {
		return nil, errors.New("neofs: nil client")
	}
	if offset < 0 {
		return nil, fmt.Errorf("neofs: negative offset %d", offset)
	}
	if length < 0 {
		return nil, fmt.Errorf("neofs: negative length %d", length)
	}
	if length == 0 {
		return []byte{}, nil
	}

	var prm client.PrmObjectRange
	r, err := c.c.ObjectRangeInit(ctx, containerID, objectID, uint64(offset), uint64(length), c.signer, prm)
	if err != nil {
		return nil, fmt.Errorf("neofs: ObjectRange init: %w", err)
	}
	defer func() {
		closeErr := r.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("neofs: ObjectRange close: %w", closeErr)
		}
	}()

	buf := make([]byte, 0)
	chunk := make([]byte, 256*1024)
	for {
		n, readErr := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("neofs: ObjectRange read: %w", readErr)
		}
	}
	return buf, nil
}

// ReadObjectRange streams a byte range from a NeoFS object and returns it as a
// slice. Used by the Windows CfApi adapter to hydrate placeholder files.
func (c *Client) ReadObjectRange(ctx context.Context, containerIDStr, objectIDStr string, offset, length int64) ([]byte, error) {
	var containerID cid.ID
	if err := containerID.DecodeString(containerIDStr); err != nil {
		return nil, fmt.Errorf("neofs: invalid container ID %q: %w", containerIDStr, err)
	}
	var objectID oid.ID
	if err := objectID.DecodeString(objectIDStr); err != nil {
		return nil, fmt.Errorf("neofs: invalid object ID %q: %w", objectIDStr, err)
	}
	return c.readObjectRange(ctx, containerID, objectID, offset, length)
}

// ReadObjectRangeIDs streams a byte range from a NeoFS object identified by typed IDs.
func (c *Client) ReadObjectRangeIDs(ctx context.Context, containerID cid.ID, objectID oid.ID, offset, length int64) ([]byte, error) {
	return c.readObjectRange(ctx, containerID, objectID, offset, length)
}

func (c *Client) ObjectHead(ctx context.Context, containerID cid.ID, objectID oid.ID) (*object.Object, error) {
	var prm client.PrmObjectHead
	return c.c.ObjectHead(ctx, containerID, objectID, c.signer, prm)
}

func (c *Client) ObjectPut(ctx context.Context, containerID cid.ID, relPath string, payload io.Reader, contentType string) (oid.ID, error) {
	return c.ObjectPutContentType(ctx, containerID, relPath, payload, contentType, "")
}

func (c *Client) ObjectPutContentType(ctx context.Context, containerID cid.ID, relPath string, payload io.Reader, userContentType string, overrideContentType string) (oid.ID, error) {
	relPath = strings.TrimPrefix(relPath, "/")
	filePath := "/" + relPath

	obj := object.New(containerID, c.signer.UserID())

	attrs := []object.Attribute{
		object.NewAttribute(object.AttributeFilePath, filePath),
	}
	if base := baseName(relPath); base != "" {
		attrs = append(attrs, object.NewAttribute(object.AttributeFileName, base))
	}
	if overrideContentType != "" {
		attrs = append(attrs, object.NewAttribute(object.AttributeContentType, overrideContentType))
	} else if userContentType != "" {
		attrs = append(attrs, object.NewAttribute(object.AttributeContentType, userContentType))
	}
	obj.SetAttributes(attrs...)

	st, err := c.getOrCreateObjectSession(ctx, containerID, session.VerbObjectPut)
	if err != nil {
		return oid.ID{}, err
	}

	var prm client.PrmObjectPutInit
	prm.WithinSession(st)

	w, err := c.cw.ObjectPutInit(ctx, *obj, c.signer, prm)
	if err != nil {
		return oid.ID{}, err
	}

	if _, err := io.Copy(w, payload); err != nil {
		_ = w.Close()
		return oid.ID{}, err
	}

	if err := w.Close(); err != nil {
		return oid.ID{}, err
	}

	return w.GetResult().StoredObjectID(), nil
}

func (c *Client) ObjectDelete(ctx context.Context, containerID cid.ID, objectID oid.ID) error {
	st, err := c.getOrCreateObjectSession(ctx, containerID, session.VerbObjectDelete)
	if err != nil {
		return err
	}

	var prm client.PrmObjectDelete
	prm.WithinSession(st)
	_, err = c.c.ObjectDelete(ctx, containerID, objectID, c.signer, prm)
	return err
}

func (c *Client) getOrCreateObjectSession(ctx context.Context, containerID cid.ID, verb session.ObjectVerb) (session.Object, error) {
	key := sessionCacheKey{container: containerID.EncodeToString(), verb: verb}

	ni, err := c.c.NetworkInfo(ctx, client.PrmNetworkInfo{})
	if err != nil {
		return session.Object{}, fmt.Errorf("neofs: network info: %w", err)
	}

	curr := ni.CurrentEpoch()

	c.mu.Lock()
	if ent, ok := c.sessionCache[key]; ok {
		if curr+1 < ent.epochExp {
			tok := ent.token
			c.mu.Unlock()
			return tok, nil
		}
	}
	c.mu.Unlock()

	exp := curr + 20

	var prmSession client.PrmSessionCreate
	prmSession.SetExp(exp)
	res, err := c.c.SessionCreate(ctx, c.signer, prmSession)
	if err != nil {
		return session.Object{}, fmt.Errorf("neofs: session create: %w", err)
	}

	var pub neofsecdsa.PublicKey
	if err := pub.Decode(res.PublicKey()); err != nil {
		return session.Object{}, fmt.Errorf("neofs: decode session pubkey: %w", err)
	}

	var id uuid.UUID
	if err := id.UnmarshalBinary(res.ID()); err != nil {
		return session.Object{}, fmt.Errorf("neofs: decode session id: %w", err)
	}

	var tok session.Object
	tok.SetID(id)
	tok.SetNbf(curr)
	tok.SetIat(curr)
	tok.SetExp(exp)
	tok.SetAuthKey(&pub)
	tok.BindContainer(containerID)
	tok.ForVerb(verb)
	if err := tok.Sign(c.signer); err != nil {
		return session.Object{}, fmt.Errorf("neofs: sign session: %w", err)
	}

	c.mu.Lock()
	c.sessionCache[key] = cachedSession{epochExp: exp, token: tok}
	c.mu.Unlock()

	return tok, nil
}

func parseWIFOrPath(v string) (*keys.PrivateKey, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New("neofs: empty wallet key (WIF or path)")
	}

	// If it's an existing file path, treat file contents as WIF.
	if st, err := os.Stat(v); err == nil && !st.IsDir() {
		b, err := os.ReadFile(v)
		if err != nil {
			return nil, fmt.Errorf("neofs: read wallet key file: %w", err)
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return nil, errors.New("neofs: empty wallet key file")
		}
		pk, err := keys.NewPrivateKeyFromWIF(s)
		if err != nil {
			return nil, fmt.Errorf("neofs: wallet key file does not contain WIF: %w", err)
		}
		return pk, nil
	}

	// Otherwise treat it as raw WIF.
	pk, err := keys.NewPrivateKeyFromWIF(v)
	if err != nil {
		return nil, fmt.Errorf("neofs: wallet key is neither a readable file path nor a valid WIF: %w", err)
	}
	return pk, nil
}

func baseName(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return p
	}
	return p[i+1:]
}

// AddressFromWIF derives the Neo N3 address string from a WIF private key
// (or a file path containing a WIF). Returns an error if parsing fails.
func AddressFromWIF(wifOrPath string) (string, error) {
	pk, err := parseWIFOrPath(wifOrPath)
	if err != nil {
		return "", err
	}
	return pk.PublicKey().Address(), nil
}
