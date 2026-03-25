package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Cache struct {
	dir      string
	maxBytes int64

	mu       sync.Mutex
	curBytes int64
	lru      []string // most-recent first
	entries  map[string]*entry
}

func (c *Cache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

type entry struct {
	path  string
	size  int64
	atime time.Time

	fetching bool
	waiters  []chan struct{}
	lastErr  error
}

func New(dir string, maxBytes int64) (*Cache, error) {
	if dir == "" {
		return nil, errors.New("cache: empty dir")
	}
	if maxBytes <= 0 {
		return nil, errors.New("cache: non-positive maxBytes")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	return &Cache{
		dir:      dir,
		maxBytes: maxBytes,
		entries:  make(map[string]*entry),
	}, nil
}

func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = io.WriteString(h, p)
		_, _ = io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// GetOrFetch ensures key is present in cache and returns the cached file path.
// fetch must write the object payload to w; GetOrFetch handles temp file + rename.
func (c *Cache) GetOrFetch(ctx context.Context, key string, fetch func(ctx context.Context, w io.Writer) error) (string, int64, error) {
	if c == nil {
		return "", 0, errors.New("cache: nil")
	}
	if key == "" {
		return "", 0, errors.New("cache: empty key")
	}

	// Fast path: hit.
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && !e.fetching && e.lastErr == nil {
		path := e.path
		size := e.size
		e.atime = time.Now()
		c.bumpLRU(key)
		c.mu.Unlock()
		if st, err := os.Stat(path); err == nil && st.Size() == size {
			return path, size, nil
		}
		// File missing or changed: treat as miss.
		delete(c.entries, key)
		c.removeFromLRU(key)
		c.curBytes -= size
	}

	// In-flight fetch: wait.
	if e, ok := c.entries[key]; ok && e.fetching {
		ch := make(chan struct{})
		e.waiters = append(e.waiters, ch)
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-ch:
			// re-check
			return c.GetOrFetch(ctx, key, fetch)
		}
	}

	// Start fetch.
	e := &entry{fetching: true, atime: time.Now()}
	c.entries[key] = e
	c.bumpLRU(key)
	c.mu.Unlock()

	finalPath := filepath.Join(c.dir, key+".blob")
	tmpPath := finalPath + ".tmp"

	_ = os.Remove(tmpPath)
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		c.finishFetch(key, "", 0, err)
		return "", 0, err
	}

	err = fetch(ctx, f)
	closeErr := f.Close()
	if err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		c.finishFetch(key, "", 0, err)
		return "", 0, err
	}

	st, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		c.finishFetch(key, "", 0, err)
		return "", 0, err
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		c.finishFetch(key, "", 0, err)
		return "", 0, err
	}

	c.finishFetch(key, finalPath, st.Size(), nil)
	c.evictIfNeeded()

	return finalPath, st.Size(), nil
}

func (c *Cache) finishFetch(key, path string, size int64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok {
		return
	}

	e.fetching = false
	e.lastErr = err
	if err == nil {
		e.path = path
		e.size = size
		c.curBytes += size
	}

	for _, ch := range e.waiters {
		close(ch)
	}
	e.waiters = nil
}

func (c *Cache) evictIfNeeded() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for c.curBytes > c.maxBytes && len(c.lru) > 0 {
		// Evict least-recent: end of slice.
		key := c.lru[len(c.lru)-1]
		c.lru = c.lru[:len(c.lru)-1]
		e := c.entries[key]
		delete(c.entries, key)
		if e != nil && e.path != "" {
			_ = os.Remove(e.path)
			c.curBytes -= e.size
		}
	}
}

func (c *Cache) bumpLRU(key string) {
	// Remove then prepend.
	c.removeFromLRU(key)
	c.lru = append([]string{key}, c.lru...)
}

func (c *Cache) removeFromLRU(key string) {
	for i := range c.lru {
		if c.lru[i] == key {
			copy(c.lru[i:], c.lru[i+1:])
			c.lru = c.lru[:len(c.lru)-1]
			return
		}
	}
}

func (c *Cache) String() string {
	return fmt.Sprintf("Cache{dir=%s,maxBytes=%d}", c.dir, c.maxBytes)
}

// StoreFromPath moves an existing local file into the cache under key.
// The source file must be on the same filesystem as the cache dir for atomic rename.
func (c *Cache) StoreFromPath(key string, srcPath string) (string, int64, error) {
	if c == nil {
		return "", 0, errors.New("cache: nil")
	}
	if key == "" {
		return "", 0, errors.New("cache: empty key")
	}
	if srcPath == "" {
		return "", 0, errors.New("cache: empty srcPath")
	}

	st, err := os.Stat(srcPath)
	if err != nil {
		return "", 0, err
	}

	finalPath := filepath.Join(c.dir, key+".blob")
	if err := os.Rename(srcPath, finalPath); err != nil {
		return "", 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Replace any existing entry accounting.
	if old, ok := c.entries[key]; ok && old.lastErr == nil && old.path != "" {
		c.curBytes -= old.size
		_ = os.Remove(old.path)
	}

	c.entries[key] = &entry{
		path:    finalPath,
		size:    st.Size(),
		atime:   time.Now(),
		lastErr: nil,
	}
	c.bumpLRU(key)
	c.curBytes += st.Size()
	go c.evictIfNeeded()

	return finalPath, st.Size(), nil
}

