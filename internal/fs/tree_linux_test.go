//go:build linux

package fs

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

type rangeFetchCall struct {
	off    int64
	length int64
}

func TestRangeFileHandleReadReusesChunk(t *testing.T) {
	payload := patternedBytes(2 << 20)
	var calls []rangeFetchCall

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			calls = append(calls, rangeFetchCall{off: off, length: length})
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	got1 := readRangeHandle(t, h, 0, 64<<10)
	got2 := readRangeHandle(t, h, 128<<10, 64<<10)

	if !bytes.Equal(got1, payload[:64<<10]) {
		t.Fatalf("first read mismatch")
	}
	if !bytes.Equal(got2, payload[128<<10:192<<10]) {
		t.Fatalf("second read mismatch")
	}
	if len(calls) != 1 {
		t.Fatalf("fetch calls = %d, want 1", len(calls))
	}
	wantLen := rangeReadProbeChunkSize
	if wantLen > int64(len(payload)) {
		wantLen = int64(len(payload))
	}
	if calls[0].off != 0 || calls[0].length != wantLen {
		t.Fatalf("first fetch = %+v, want off=0 len=%d", calls[0], wantLen)
	}
}

func TestRangeFileHandleReadSupportsBackwardSeekAcrossChunks(t *testing.T) {
	payload := patternedBytes(int(rangeReadChunkSize + 2<<20))
	var calls []rangeFetchCall

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			calls = append(calls, rangeFetchCall{off: off, length: length})
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	off1 := int64(0)
	off2 := rangeReadChunkSize + 128<<10
	off3 := int64(1 << 20)

	got1 := readRangeHandle(t, h, off1, 32<<10)
	got2 := readRangeHandle(t, h, off2, 64<<10)
	got3 := readRangeHandle(t, h, off3, 32<<10)

	if !bytes.Equal(got1, payload[:32<<10]) {
		t.Fatalf("first read mismatch")
	}
	if !bytes.Equal(got2, payload[off2:off2+64<<10]) {
		t.Fatalf("second read mismatch")
	}
	if !bytes.Equal(got3, payload[off3:off3+32<<10]) {
		t.Fatalf("third read mismatch")
	}
	if len(calls) != 3 {
		t.Fatalf("fetch calls = %d, want 3", len(calls))
	}

	wantOff2 := off2 / rangeReadChunkSize * rangeReadChunkSize
	if calls[1].off != wantOff2 {
		t.Fatalf("second fetch off = %d, want %d", calls[1].off, wantOff2)
	}
	if calls[2].off != 0 || calls[2].length != rangeReadChunkSize {
		t.Fatalf("third fetch = %+v, want off=0 len=%d", calls[2], rangeReadChunkSize)
	}
}

func TestRangeFileHandleReadSpansChunks(t *testing.T) {
	payload := patternedBytes(int(rangeReadChunkSize + 2<<20))
	var calls []rangeFetchCall

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			calls = append(calls, rangeFetchCall{off: off, length: length})
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	off := rangeReadChunkSize - 64<<10
	got := readRangeHandle(t, h, off, 128<<10)
	if !bytes.Equal(got, payload[off:off+128<<10]) {
		t.Fatalf("spanning read mismatch")
	}
	if len(calls) != 2 {
		t.Fatalf("fetch calls = %d, want 2", len(calls))
	}
}

func TestRangeFileHandleKeepsHeadAndTailChunks(t *testing.T) {
	totalChunks := rangeReadMaxChunks + 4
	payload := patternedBytes(int(rangeReadChunkSize * int64(totalChunks)))
	var calls []rangeFetchCall

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			calls = append(calls, rangeFetchCall{off: off, length: length})
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	readRangeHandle(t, h, 0, 32<<10)
	tailOff := int64(len(payload)) - 32<<10
	readRangeHandle(t, h, tailOff, 32<<10)
	for i := 1; i <= rangeReadMaxChunks-1; i++ {
		readRangeHandle(t, h, int64(i)*rangeReadChunkSize, 32<<10)
	}

	fetchesBeforeReread := len(calls)
	readRangeHandle(t, h, 0, 32<<10)
	readRangeHandle(t, h, tailOff, 32<<10)
	if len(calls) != fetchesBeforeReread {
		t.Fatalf("head/tail reread triggered new fetches: before=%d after=%d", fetchesBeforeReread, len(calls))
	}
}

func TestRangeFileHandleReadReturnsEintrOnCanceledFetch(t *testing.T) {
	h := &rangeFileHandle{
		size: 8 << 20,
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			return nil, context.Canceled
		},
	}

	res, errno := h.Read(context.Background(), make([]byte, 32<<10), 0)
	if res != nil {
		t.Fatalf("read result = %#v, want nil", res)
	}
	if errno != syscall.EINTR {
		t.Fatalf("errno = %v, want %v", errno, syscall.EINTR)
	}
}

func TestRangeFileHandleReadCollapsesConcurrentSameChunkFetch(t *testing.T) {
	payload := patternedBytes(int(rangeReadChunkSize))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			calls.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	errCh := make(chan error, 2)
	go func() { errCh <- verifyRangeRead(h, 0, 32<<10) }()
	go func() { errCh <- verifyRangeRead(h, 128<<10, 32<<10) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for chunk fetch to start")
	}
	close(release)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

func TestRangeFileHandleReadFetchesDifferentChunksConcurrently(t *testing.T) {
	payload := patternedBytes(int(2 * rangeReadChunkSize))
	started := make(chan int64, 2)
	release := make(chan struct{})

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			started <- off
			<-release
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	errCh := make(chan error, 2)
	go func() { errCh <- verifyRangeRead(h, 0, 32<<10) }()
	go func() { errCh <- verifyRangeRead(h, rangeReadChunkSize, 32<<10) }()

	seen := make(map[int64]struct{}, 2)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(seen) < 2 {
		select {
		case off := <-started:
			seen[off] = struct{}{}
		case <-deadline.C:
			t.Fatalf("concurrent reads did not start two fetches")
		}
	}

	close(release)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRangeFileHandleReadExpandsProbeChunk(t *testing.T) {
	payload := patternedBytes(int(2 * rangeReadChunkSize))
	var calls []rangeFetchCall

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			calls = append(calls, rangeFetchCall{off: off, length: length})
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	readRangeHandle(t, h, 0, 32<<10)
	readRangeHandle(t, h, rangeReadProbeChunkSize+32<<10, 32<<10)

	if len(calls) != 2 {
		t.Fatalf("fetch calls = %d, want 2", len(calls))
	}
	if calls[0].off != 0 || calls[0].length != rangeReadProbeChunkSize {
		t.Fatalf("first fetch = %+v, want off=0 len=%d", calls[0], rangeReadProbeChunkSize)
	}
	if calls[1].off != 0 || calls[1].length != rangeReadChunkSize {
		t.Fatalf("second fetch = %+v, want off=0 len=%d", calls[1], rangeReadChunkSize)
	}
}

func patternedBytes(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	return buf
}

func readRangeHandle(t *testing.T, h *rangeFileHandle, off int64, size int) []byte {
	t.Helper()

	buf := make([]byte, size)
	res, errno := h.Read(context.Background(), buf, off)
	if errno != 0 {
		t.Fatalf("read errno = %v", errno)
	}

	got, status := res.Bytes(nil)
	res.Done()
	if status != fuse.OK {
		t.Fatalf("read status = %v", status)
	}

	return append([]byte(nil), got...)
}

func verifyRangeRead(h *rangeFileHandle, off int64, size int) error {
	buf := make([]byte, size)
	res, errno := h.Read(context.Background(), buf, off)
	if errno != 0 {
		return fmt.Errorf("read errno = %v", errno)
	}

	got, status := res.Bytes(nil)
	res.Done()
	if status != fuse.OK {
		return fmt.Errorf("read status = %v", status)
	}
	if len(got) != size {
		return fmt.Errorf("read len = %d, want %d", len(got), size)
	}
	return nil
}
