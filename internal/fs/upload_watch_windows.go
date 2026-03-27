//go:build windows

package fs

import (
	"context"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/mathias/neofs-mount/internal/cfapi"
	"golang.org/x/sys/windows"
)

type pathDebouncer struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newPathDebouncer() *pathDebouncer {
	return &pathDebouncer{timers: make(map[string]*time.Timer)}
}

func (d *pathDebouncer) schedule(key string, delay time.Duration, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.timers[key]; ok {
		t.Stop()
	}
	d.timers[key] = time.AfterFunc(delay, func() {
		d.mu.Lock()
		delete(d.timers, key)
		d.mu.Unlock()
		fn()
	})
}

// cancel drops a pending debounced callback (e.g. file removed or renamed away).
func (d *pathDebouncer) cancel(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.timers[key]; ok {
		t.Stop()
		delete(d.timers, key)
	}
}

func (p *neofsProvider) runUploadWatcher(stop <-chan struct{}) {
	root := filepath.Clean(p.root)
	rootW, err := windows.UTF16PtrFromString(root)
	if err != nil {
		p.log.Debug("upload watch: UTF16 mount root", "err", err)
		return
	}
	h, err := windows.CreateFile(rootW,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		p.log.Debug("upload watch: open sync root", "path", root, "err", err)
		return
	}

	go func() {
		<-stop
		_ = windows.CloseHandle(h)
	}()

	const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME |
		windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE

	buf := make([]byte, 64*1024)
	deb := newPathDebouncer()
	const debounceDelay = 500 * time.Millisecond

	for {
		var ret uint32
		err := windows.ReadDirectoryChanges(h, &buf[0], uint32(len(buf)), true, mask, &ret, nil, 0)
		if err != nil {
			if err == windows.ERROR_INVALID_HANDLE {
				return
			}
			p.log.Debug("upload watch: ReadDirectoryChanges", "err", err)
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if ret == 0 {
			continue
		}
		p.dispatchNotifyEvents(buf[:ret], deb, debounceDelay)
	}
}

func isRecycleBinPath(p string) bool {
	return strings.Contains(strings.ToLower(p), `\$recycle.bin\`)
}

func (p *neofsProvider) startUploadWatcher() {
	if p.ro || p.root == "" {
		return
	}
	ch := make(chan struct{})
	p.uploadWatchStop = ch
	p.uploadWatchWG.Add(1)
	go func() {
		defer p.uploadWatchWG.Done()
		p.runUploadWatcher(ch)
	}()
}

func (p *neofsProvider) stopUploadWatcher() {
	if p.uploadWatchStop == nil {
		return
	}
	close(p.uploadWatchStop)
	p.uploadWatchWG.Wait()
	p.uploadWatchStop = nil
}

func (p *neofsProvider) dispatchNotifyEvents(buf []byte, deb *pathDebouncer, delay time.Duration) {
	offset := 0
	for offset+12 <= len(buf) {
		next := int(binary.LittleEndian.Uint32(buf[offset:]))
		action := binary.LittleEndian.Uint32(buf[offset+4:])
		nameLen := int(binary.LittleEndian.Uint32(buf[offset+8:]))
		nameOff := offset + 12
		if nameOff+nameLen > len(buf) || nameLen < 0 || nameLen%2 != 0 {
			break
		}
		u16s := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameOff])), nameLen/2)
		rel := windows.UTF16ToString(u16s)
		full := filepath.Join(p.root, rel)
		key := filepath.Clean(full)
		switch action {
		case windows.FILE_ACTION_MODIFIED:
			if isRecycleBinPath(key) {
				break
			}
			// Directory "modified" is extremely noisy (Explorer metadata, deleting a child, etc.).
			// Scheduling maybeUploadPathFromWatcher on a directory walks the whole subtree and would
			// re-upload every file after unrelated events (e.g. context-menu delete of one file).
			if fi, statErr := os.Stat(full); statErr == nil && fi.IsDir() {
				break
			}
			fallthrough
		case windows.FILE_ACTION_ADDED, windows.FILE_ACTION_RENAMED_NEW_NAME:
			if isRecycleBinPath(key) {
				break
			}
			deb.schedule(key, delay, func() {
				p.maybeUploadPathFromWatcher(key)
			})
		case windows.FILE_ACTION_REMOVED, windows.FILE_ACTION_RENAMED_OLD_NAME:
			deb.cancel(key)
			cause := "removed"
			if action == windows.FILE_ACTION_RENAMED_OLD_NAME {
				cause = "renamed_old_name"
			}
			if p.audit != nil {
				p.audit.Record("fs_notify_pending_upload_cancelled", map[string]any{"path": key, "cause": cause})
			}
			// If NeoFS still has this object, recreate a cloud placeholder so Explorer keeps the path
			// (e.g. user moved the hydrated file out of the sync root).
			pathCopy := key
			go func() {
				time.Sleep(150 * time.Millisecond)
				p.tryRestorePlaceholderAfterRemove(pathCopy)
			}()
		}
		if next == 0 {
			break
		}
		offset += next
		if offset >= len(buf) {
			break
		}
	}
}

// maybeUploadPathFromWatcher runs after a debounced filesystem notification. For a file it uploads
// that object; for a directory (e.g. moved folder) it walks the tree and uploads each regular file.
func (p *neofsProvider) maybeUploadPathFromWatcher(path string) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("cfapi: upload watch callback panic", "recover", r)
		}
	}()
	if isRecycleBinPath(path) {
		return
	}
	if isEphemeralEditorHiddenName(filepath.Base(path)) {
		return
	}
	// Same as uploadClosedFile: re-creates after delete must upload; wasRecentDelete is for restore suppression only.
	path = filepath.Clean(path)
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if st.IsDir() {
		_ = filepath.WalkDir(path, func(subPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if isRecycleBinPath(subPath) {
				return nil
			}
			if isEphemeralEditorHiddenName(filepath.Base(subPath)) {
				return nil
			}
			containerIDStr, neoRel, ok := p.mountDiskToNeo(subPath)
			if !ok {
				return nil
			}
			if d.IsDir() {
				dirIdent := cfapi.IdentityFromString(containerIDStr + ":dir:" + filepath.ToSlash(neoRel))
				if err := cfapi.ConvertToPlaceholder(subPath, dirIdent, true); err != nil {
					p.log.Debug("cfapi: convert dir to placeholder", "path", subPath, "err", err)
				}
				return nil
			}
			if _, err := d.Info(); err != nil {
				return nil
			}
			go func(sp string) {
				_ = p.uploadClosedFile(context.Background(), sp)
			}(subPath)
			return nil
		})
		return
	}
	if _, _, ok := p.mountDiskToNeo(path); !ok {
		return
	}
	go func() { _ = p.uploadClosedFile(context.Background(), path) }()
}
