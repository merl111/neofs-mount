//go:build linux

package fs

import (
	"bytes"
	"context"
	"syscall"
	"testing"

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
	if calls[0].off != 0 || calls[0].length != int64(len(payload)) {
		t.Fatalf("first fetch = %+v, want off=0 len=%d", calls[0], len(payload))
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
	if len(calls) != 2 {
		t.Fatalf("fetch calls = %d, want 2", len(calls))
	}

	wantOff2 := off2 / rangeReadChunkSize * rangeReadChunkSize
	if calls[1].off != wantOff2 {
		t.Fatalf("second fetch off = %d, want %d", calls[1].off, wantOff2)
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
