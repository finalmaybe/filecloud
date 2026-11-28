package library

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestDeleteTrashAPIOnlyRemovesBinRecords(t *testing.T) {
	ctx := t.Context()
	h, s, now := newTestHandler(t)
	defer closeStore(t, s)
	c := h.(*Catalog)
	const owner, lib = _ownerID, _headLibraryID
	const base = "/v1/libraries/" + lib
	assertStatusCode(t, serve(h, "PUT", base, `{"Name":"bin"}`, _ownerToken), 201, 0)

	file := func(data string) string {
		t.Helper()
		id := object.ID([]byte(data))
		if _, err := s.PutObject(ctx, owner, lib, "blocks", id, strings.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		return putMetadata(t, s, "files", fmt.Sprintf(`{"Blocks":[%q],"Size":"%d","Type":"File","Version":1}`, id, len(data)))
	}
	oldFile, currentFile, removedFile := file("v1"), file("v2"), file("deleted")
	entry := func(name, kind, id string) object.DirectoryEntry {
		return object.DirectoryEntry{Name: name, Type: kind, ID: id, ModifiedAt: now.UTC().Truncate(time.Second).Format(time.RFC3339)}
	}
	dir := func(entries ...object.DirectoryEntry) string {
		return putMetadata(t, s, "directories", string(object.Directory{Entries: entries}.Bytes()))
	}
	folder := entry("folder", "Directory", dir(entry("child", "File", removedFile)))
	removed := entry("removed.txt", "File", removedFile)
	first := putCommit(t, s, owner, nil, dir(folder, entry("keep.txt", "File", oldFile), removed))
	publishHead(t, h, first, headETag(0), 200, 0)
	last := putCommit(t, s, owner, []string{first}, dir(folder, entry("keep.txt", "File", currentFile), removed))
	publishHead(t, h, last, headETag(1), 200, 0)
	hidden := VersionTarget{LibraryID: lib, CommitID: first, Path: "keep.txt"}
	if err := c.DeleteFileVersion(ctx, owner, hidden, last); err != nil {
		t.Fatal(err)
	}
	head := putCommit(t, s, owner, []string{last}, dir(entry("keep.txt", "File", currentFile)))
	publishHead(t, h, head, headETag(2), 200, 0)

	list := func() map[string]int64 {
		t.Helper()
		res := serve(h, "GET", base+"/trash", "", _ownerToken)
		assertStatusCode(t, res, 200, 0)
		var page struct {
			Entries []struct {
				ID   int64
				Kind string
			}
		}
		decode(t, res, &page)
		ids := map[string]int64{}
		for _, e := range page.Entries {
			ids[e.Kind] = e.ID
		}
		return ids
	}
	ids := list()
	if len(ids) != 3 || ids["File"] < 1 || ids["Directory"] < 1 || ids["Version"] < 1 {
		t.Fatalf("trash IDs: %v", ids)
	}
	request := func(token, body string, status, code int) {
		t.Helper()
		assertStatusCode(t, serve(h, "POST", base+"/trash/delete", body, token), status, code)
	}
	single := fmt.Sprintf(`{"IDs":[%d]}`, ids["File"])
	request("", single, 401, 1001)
	request(_otherToken, single, 404, 2000)
	for _, invalid := range []string{
		`{}`, `{"IDs":[]}`, `{"IDs":null}`, `{"IDs":[null]}`, `{"IDs":[0]}`, `{"IDs":[-1]}`, `{"IDs":[1.0]}`, `{"IDs":["1"]}`,
		fmt.Sprintf(`{"IDs":[%d,%d]}`, ids["File"], ids["File"]),
		`{"IDs":[` + strings.Repeat("1,", storage.TrashPageSize) + `2]}`,
		single + `{}`, strings.TrimSuffix(single, "}") + `,"IDs":[1]}`,
	} {
		request(_ownerToken, invalid, 400, 1000)
	}
	request(_ownerToken, fmt.Sprintf(`{"IDs":[%d,123456]}`, ids["File"]), 404, 2000)
	if got := list(); len(got) != 3 {
		t.Fatalf("failed request changed the bin: %v", got)
	}
	request(_ownerToken, single, 204, 0)
	if got := list(); len(got) != 2 || got["Directory"] != ids["Directory"] || got["Version"] != ids["Version"] {
		t.Fatalf("single delete changed other records: %v", got)
	}
	request(_ownerToken, fmt.Sprintf(`{"IDs":[%d,%d]}`, ids["Directory"], ids["Version"]), 204, 0)
	if got := list(); len(got) != 0 {
		t.Fatalf("batch delete left bin records: %v", got)
	}
	for _, id := range ids {
		if err := c.RestoreTrash(ctx, owner, lib, id, ""); !errors.Is(err, storage.ErrLibraryNotFound) {
			t.Fatalf("deleted record %d remained restorable: %v", id, err)
		}
	}
	if _, err := c.OpenFileVersion(ctx, owner, hidden); !errors.Is(err, storage.ErrLibraryNotFound) {
		t.Fatal("permanently deleted version became visible again:", err)
	}
	assertCurrentHead(t, h, head, headETag(3))
	for _, f := range []struct{ commit, path, content string }{
		{head, "keep.txt", "v2"}, {first, "keep.txt", "v1"}, {last, "folder/child", "deleted"},
	} {
		item, err := c.OpenSnapshotFile(ctx, owner, VersionTarget{LibraryID: lib, CommitID: f.commit, Path: f.path})
		if err != nil {
			t.Fatal(f, err)
		}
		data, err := io.ReadAll(item.Content(ctx))
		if err != nil || string(data) != f.content {
			t.Fatal(f, string(data), err)
		}
	}
}
