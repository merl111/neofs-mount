//go:build linux

package fs

import (
	"bytes"
	"context"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

type rangeFetchCall struct {
	off    int64
	length int64
}

func TestRangeFileHandleReadReusesWindow(t *testing.T) {
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

func TestRangeFileHandleReadSupportsBackwardSeekAcrossWindows(t *testing.T) {
	payload := patternedBytes(int(rangeReadChunkMax + 2<<20))
	var calls []rangeFetchCall

	h := &rangeFileHandle{
		size: int64(len(payload)),
		fetch: func(ctx context.Context, off, length int64) ([]byte, error) {
			calls = append(calls, rangeFetchCall{off: off, length: length})
			return append([]byte(nil), payload[off:off+length]...), nil
		},
	}

	off1 := int64(0)
	off2 := rangeReadChunkMax + 128<<10
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

	wantOff2 := off2 / rangeReadAlign * rangeReadAlign
	wantOff3 := off3 / rangeReadAlign * rangeReadAlign
	if calls[1].off != wantOff2 {
		t.Fatalf("second fetch off = %d, want %d", calls[1].off, wantOff2)
	}
	if calls[2].off != wantOff3 {
		t.Fatalf("third fetch off = %d, want %d", calls[2].off, wantOff3)
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
