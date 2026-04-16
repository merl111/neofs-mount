package neofs

import (
	"reflect"
	"testing"

	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
)

func oidByte(b byte) oid.ID {
	var id oid.ID
	for i := range id {
		id[i] = b
	}
	return id
}

func TestListingTrieMatchesLinear(t *testing.T) {
	mk := func(path string, b byte, size int64) SearchEntry {
		return SearchEntry{ObjectID: oidByte(b), FilePath: path, Size: size}
	}
	mkFileName := func(fileName string, b byte, size int64) SearchEntry {
		return SearchEntry{ObjectID: oidByte(b), FileName: fileName, Size: size}
	}
	entries := []SearchEntry{
		mk("/docs/readme.txt", 0x11, 10),
		mk("/docs/sub/hook.txt", 0x22, 20),
		mk("/other.bin", 0x33, 3),
	}
	tr := buildListingTrie(entries)
	prefixes := []string{"", "docs", "docs/", "docs/sub", "docs/sub/"}
	for _, p := range prefixes {
		norm := normalizeNeoFSPrefix(p)
		got := tr.listImmediateChildren(norm)
		want := listEntriesByPrefixLinear(entries, norm)
		if len(got) != len(want) {
			t.Fatalf("prefix %q: len got %d want %d\ngot=%+v\nwant=%+v", p, len(got), len(want), got, want)
		}
		// Linear order must match trie insertion order (same as entries order).
		for i := range got {
			if got[i].Name != want[i].Name || got[i].IsDirectory != want[i].IsDirectory {
				t.Fatalf("prefix %q idx %d: got %+v want %+v", p, i, got[i], want[i])
			}
			if !want[i].IsDirectory {
				if got[i].ObjectID != want[i].ObjectID || got[i].Size != want[i].Size {
					t.Fatalf("prefix %q idx %d file meta: got %+v want %+v", p, i, got[i], want[i])
				}
			}
		}
	}

	// Objects that only set AttributeFileName (no FilePath / Key / Name) must still list at root.
	onlyName := []SearchEntry{
		mkFileName("root-only.bin", 0x41, 5),
		mkFileName("notes.txt", 0x42, 7),
	}
	tr2 := buildListingTrie(onlyName)
	gotRoot := tr2.listImmediateChildren("")
	wantRoot := listEntriesByPrefixLinear(onlyName, "")
	if len(gotRoot) != len(wantRoot) {
		t.Fatalf("FileName-only entries: len got %d want %d\ngot=%+v\nwant=%+v", len(gotRoot), len(wantRoot), gotRoot, wantRoot)
	}
}

func TestSplitObjectRange(t *testing.T) {
	got := splitObjectRange(1024, objectRangeMultipartPartSize*2+123)
	want := []objectRangePart{
		{index: 0, offset: 1024, length: objectRangeMultipartPartSize},
		{index: 1, offset: 1024 + objectRangeMultipartPartSize, length: objectRangeMultipartPartSize},
		{index: 2, offset: 1024 + objectRangeMultipartPartSize*2, length: 123},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitObjectRange mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}
