package uploads

import (
	"sync"
	"sync/atomic"
	"time"
)

// Entry represents a single in-flight upload.
type Entry struct {
	Path      string
	TotalBytes int64
	sent      atomic.Int64
	Started   time.Time
}

// Sent returns how many bytes have been transferred so far.
func (e *Entry) Sent() int64 { return e.sent.Load() }

// AddSent increments the transferred byte counter.
func (e *Entry) AddSent(n int64) { e.sent.Add(n) }

// Tracker tracks in-flight uploads. Safe for concurrent use.
type Tracker struct {
	mu      sync.RWMutex
	entries map[string]*Entry // keyed by path
}

func New() *Tracker {
	return &Tracker{entries: make(map[string]*Entry)}
}

// Register adds a new upload and returns the Entry to update progress on.
func (t *Tracker) Register(path string, totalBytes int64) *Entry {
	e := &Entry{Path: path, TotalBytes: totalBytes, Started: time.Now()}
	t.mu.Lock()
	t.entries[path] = e
	t.mu.Unlock()
	return e
}

// Finish removes an upload from the active list.
func (t *Tracker) Finish(path string) {
	t.mu.Lock()
	delete(t.entries, path)
	t.mu.Unlock()
}

// List returns a snapshot of in-flight uploads.
func (t *Tracker) List() []*Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Entry, 0, len(t.entries))
	for _, e := range t.entries {
		out = append(out, e)
	}
	return out
}
