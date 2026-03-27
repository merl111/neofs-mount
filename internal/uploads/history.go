package uploads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultHistoryMax = 400

// HistoryItem is one completed or failed upload persisted for the tray UI.
type HistoryItem struct {
	FinishedAt time.Time `json:"finished_at"`
	StartedAt  time.Time `json:"started_at"`
	NeoKey     string    `json:"neo_key"`
	DiskPath   string    `json:"disk_path,omitempty"`
	Bytes      int64     `json:"bytes"`
	Status     string    `json:"status"` // "ok" or "failed"
	Detail     string    `json:"detail,omitempty"`
}

// History persists upload outcomes as JSON (newest entries first).
type History struct {
	path string
	max  int

	mu    sync.RWMutex
	items []HistoryItem
}

// NewHistory loads existing items from path (if present). max caps stored rows; zero uses default.
func NewHistory(path string, max int) *History {
	if max <= 0 {
		max = defaultHistoryMax
	}
	h := &History{path: path, max: max}
	_ = h.load()
	return h
}

// Append prepends an item and writes the file. No-op if h is nil.
func (h *History) Append(it HistoryItem) {
	if h == nil || h.path == "" {
		return
	}
	if it.FinishedAt.IsZero() {
		it.FinishedAt = time.Now()
	}

	h.mu.Lock()
	out := make([]HistoryItem, 0, len(h.items)+1)
	out = append(out, it)
	out = append(out, h.items...)
	if len(out) > h.max {
		out = out[:h.max]
	}
	h.items = out
	path := h.path
	h.mu.Unlock()

	_ = saveHistoryFile(path, out)
}

// List returns a copy of items, newest first.
func (h *History) List() []HistoryItem {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	cp := make([]HistoryItem, len(h.items))
	copy(cp, h.items)
	return cp
}

func (h *History) load() error {
	if h.path == "" {
		return nil
	}
	b, err := os.ReadFile(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items []HistoryItem
	if err := json.Unmarshal(b, &items); err != nil {
		return err
	}
	if len(items) > h.max {
		items = items[:h.max]
	}
	h.mu.Lock()
	h.items = items
	h.mu.Unlock()
	return nil
}

func saveHistoryFile(path string, items []HistoryItem) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
