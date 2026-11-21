package library

import (
	"errors"
	"fmt"
	"github.com/mingming-cn/filecloud/internal/storage"
	"strings"
	"testing"
)

func TestSnapshotPinnedPathsAndPaging(t *testing.T) {
	h, s, _ := newTestHandler(t)
	defer closeStore(t, s)
	c := h.(*Catalog)
	createHeadLibrary(t, h)
	empty := putMetadata(t, s, "directories", `{"Entries":[],"Type":"Directory","Version":1}`)
	file := putMetadata(t, s, "files", `{"Blocks":[],"Size":"0","Type":"File","Version":1}`)
	var entries []string
	for i := range 55 {
		entries = append(entries, fmt.Sprintf(`{"Id":%q,"ModifiedAt":"2026-08-09T00:00:00Z","Name":"观测%02d","Type":"File"}`, file, i))
	}
	root := putMetadata(t, s, "directories", `{"Entries":[`+strings.Join(entries, ",")+`],"Type":"Directory","Version":1}`)
	id := putCommit(t, s, _ownerID, nil, root)
	if _, e := c.Snapshot(t.Context(), _ownerID, _headLibraryID, id, ".", 0); !errors.Is(e, storage.ErrLibraryNotFound) {
		t.Fatal("unpublished snapshot visible")
	}
	publishHead(t, h, id, `"head-version-0"`, 200, 0)
	next := putCommit(t, s, _ownerID, []string{id}, empty)
	publishHead(t, h, next, `"head-version-1"`, 200, 0)
	v, e := c.Snapshot(t.Context(), _ownerID, _headLibraryID, id, ".", 0)
	if e != nil || v.Next != 50 || len(v.Entries) != 50 {
		t.Fatal("first directory page", e)
	}
	v, e = c.Snapshot(t.Context(), _ownerID, _headLibraryID, id, ".", 50)
	if e != nil || len(v.Entries) != 5 || v.Entries[0].Name != "观测50" {
		t.Fatal("second page", e)
	}
	v, e = c.Snapshot(t.Context(), _ownerID, _headLibraryID, id, "观测54", 0)
	if e != nil || v.ID != file || v.Kind != "File" || v.Blocks != 0 {
		t.Fatal("file metadata", e)
	}
	if _, e = c.Snapshot(t.Context(), _ownerID, _headLibraryID, next, "观测54", 0); !errors.Is(e, storage.ErrLibraryNotFound) {
		t.Fatal("missing path")
	}
	if _, e = c.Snapshot(t.Context(), "other", _headLibraryID, id, ".", 0); !errors.Is(e, storage.ErrLibraryNotFound) {
		t.Fatal("other owner read snapshot")
	}
}
