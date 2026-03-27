// Package audit provides an append-only JSON Lines log for storage operations.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Log is a thread-safe append-only audit file (one JSON object per line).
type Log struct {
	mu   sync.Mutex
	file *os.File
}

// Open creates or appends to path. The parent directory is created if needed.
// An empty path returns (nil, nil) with no error.
func Open(path string) (*Log, error) {
	if path == "" {
		return nil, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	return &Log{file: f}, nil
}

// Close releases the log file.
func (l *Log) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.file.Close()
	l.file = nil
	return err
}

// Record appends one event. Nil receiver or empty op is a no-op.
func (l *Log) Record(op string, data map[string]any) {
	if l == nil || l.file == nil || op == "" {
		return
	}
	ev := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339Nano),
		"op": op,
	}
	for k, v := range data {
		ev[k] = v
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	_, _ = l.file.Write(append(b, '\n'))
}
