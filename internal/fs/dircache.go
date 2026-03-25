package fs

import (
	"sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

type dirCacheKey struct {
	container string
	prefix    string
}

type dirCacheEntry struct {
	at      time.Time
	entries []fuse.DirEntry
}

type dirCache struct {
	ttl time.Duration

	mu sync.Mutex
	m  map[dirCacheKey]dirCacheEntry
}

func newDirCache(ttl time.Duration) *dirCache {
	return &dirCache{
		ttl: ttl,
		m:   make(map[dirCacheKey]dirCacheEntry),
	}
}

func (c *dirCache) Get(container, prefix string) ([]fuse.DirEntry, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	k := dirCacheKey{container: container, prefix: prefix}
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.m[k]
	if !ok {
		return nil, false
	}
	if now.Sub(e.at) > c.ttl {
		delete(c.m, k)
		return nil, false
	}
	out := make([]fuse.DirEntry, len(e.entries))
	copy(out, e.entries)
	return out, true
}

func (c *dirCache) Put(container, prefix string, entries []fuse.DirEntry) {
	if c == nil || c.ttl <= 0 {
		return
	}
	k := dirCacheKey{container: container, prefix: prefix}

	cp := make([]fuse.DirEntry, len(entries))
	copy(cp, entries)

	c.mu.Lock()
	c.m[k] = dirCacheEntry{at: time.Now(), entries: cp}
	c.mu.Unlock()
}

func (c *dirCache) InvalidateContainer(container string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.container == container {
			delete(c.m, k)
		}
	}
}

