package neofs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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

	c      *client.Client
	signer user.Signer

	mu           sync.Mutex
	sessionCache map[sessionCacheKey]cachedSession

	scanMu       sync.Mutex
	scanCache    map[string]cachedScanEntries // container ID string -> head-scan listing
	scanInflight map[string]*scanFlight      // coalesce concurrent head-scans per container
}

type cachedScanEntries struct {
	at      time.Time
	entries []SearchEntry
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
	prmDial.SetStreamTimeout(30 * time.Second)
	if err := c.Dial(prmDial); err != nil {
		return nil, fmt.Errorf("neofs: dial: %w", err)
	}

	return &Client{
		log:          log,
		c:            c,
		signer:       signer,
		sessionCache: make(map[sessionCacheKey]cachedSession),
		scanCache:    make(map[string]cachedScanEntries),
		scanInflight: make(map[string]*scanFlight),
	}, nil
}

func (c *Client) Close() error {
	if c == nil || c.c == nil {
		return nil
	}
	return c.c.Close()
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
	FilePath string // as stored in object.AttributeFilePath (often leading '/')
	FileName string // as stored in object.AttributeFileName (may be empty)
	Name     string // as stored in object.AttributeName (may be empty)
	Key      string // S3-gateway "Key" attribute (may be empty)
	Size     int64  // object.FilterPayloadSize if requested
	Time     time.Time // from Timestamp or LastModified
	Attrs    map[string]string // all object attributes
}



const headScanCacheTTL = 30 * time.Second

const headScanWorkers = 12

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
				c.scanCache[key] = cachedScanEntries{at: time.Now(), entries: entries}
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

	ids, err := trySearchV2(true)
	if err != nil {
		return nil, fmt.Errorf("neofs: list object ids (root): %w", err)
	}
	if len(ids) > 0 {
		return ids, nil
	}
	ids, err = trySearchV2(false)
	if err != nil {
		return nil, fmt.Errorf("neofs: list object ids (no root filter): %w", err)
	}
	if len(ids) > 0 {
		return ids, nil
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

	ids, err = tryStream(true)
	if err != nil {
		return nil, fmt.Errorf("neofs: object search stream (root): %w", err)
	}
	if len(ids) > 0 {
		return ids, nil
	}
	ids, err = tryStream(false)
	if err != nil {
		return nil, fmt.Errorf("neofs: object search stream (no root filter): %w", err)
	}
	return ids, nil
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

	w, err := c.c.ObjectPutInit(ctx, *obj, c.signer, prm)
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

