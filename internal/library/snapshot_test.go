package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestSnapshotPinnedPathsAndPaging(t *testing.T) {
	h, s, _ := newTestHandler(t)
	defer closeStore(t, s)
	c := h.(*Catalog)
	createHeadLibrary(t, h)
	const endpoint = "/v1/libraries/" + _headLibraryID + "/files"
	readDirectory := func(query string) directoryListing {
		t.Helper()
		res := serve(h, "GET", endpoint+query, "", _ownerToken)
		assertStatusCode(t, res, 200, 0)
		var result struct{ Directory directoryListing }
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Directory
	}
	if d := readDirectory(""); d.CommitID != nil || d.Entries == nil || len(d.Entries) != 0 || d.Path != "." {
		t.Fatal("expected unpublished root")
	}
	assertStatusCode(t, serve(h, "GET", endpoint, "", _otherToken), 404, 2000)
	assertStatusCode(t, serve(h, "GET", endpoint+"?Offset=bad", "", ""), 401, 1001)
	empty := putMetadata(t, s, "directories", `{"Entries":[],"Type":"Directory","Version":1}`)
	block := object.ID([]byte("a"))
	if _, err := s.PutObject(t.Context(), _ownerID, _headLibraryID, "blocks", block, strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	file := putMetadata(t, s, "files", fmt.Sprintf(`{"Blocks":[%q],"Size":"1","Type":"File","Version":1}`, block))
	var entries []string
	for i := range 55 {
		entries = append(entries, fmt.Sprintf(`{"Id":%q,"ModifiedAt":"2026-08-09T00:00:00Z","Name":"观测%02d","Type":"File"}`, file, i))
	}
	root := putMetadata(t, s, "directories", `{"Entries":[`+strings.Join(entries, ",")+`],"Type":"Directory","Version":1}`)
	id := putCommit(t, s, _ownerID, nil, root)
	if _, e := c.Snapshot(t.Context(), _ownerID, _headLibraryID, id, ".", 0); !errors.Is(e, storage.ErrLibraryNotFound) {
		t.Fatal("unpublished snapshot visible")
	}
	assertStatusCode(t, serve(h, "GET", endpoint+"?CommitId="+id, "", _ownerToken), 404, 2000)
	publishHead(t, h, id, `"head-version-0"`, 200, 0)
	first := readDirectory("")
	if first.CommitID == nil || *first.CommitID != id || first.NextOffset != 50 || len(first.Entries) != 50 {
		t.Fatal("wrong directory first page")
	}
	next := putCommit(t, s, _ownerID, []string{id}, empty)
	publishHead(t, h, next, `"head-version-1"`, 200, 0)
	const sizeURL = "/v1/libraries/" + _headLibraryID + "/size?CommitId="
	for commit, want := range map[string]string{id: "55", next: "0"} {
		res := serve(h, "GET", sizeURL+commit, "", _ownerToken)
		assertStatusCode(t, res, 200, 0)
		var result struct{ Size string }
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil || result.Size != want {
			t.Fatalf("logical size: got %q, want %q (%v)", result.Size, want, err)
		}
	}
	assertStatusCode(t, serve(h, "GET", sizeURL+id, "", _otherToken), 404, 2000)
	second := readDirectory(fmt.Sprintf("?CommitId=%s&Offset=%d", *first.CommitID, first.NextOffset))
	if len(second.Entries) != 5 || second.Entries[0].Name != "观测50" || second.NextOffset != 0 {
		t.Fatal("directory pagination followed the new Head")
	}
	if d := readDirectory(""); d.CommitID == nil || *d.CommitID != next || d.Entries == nil || len(d.Entries) != 0 {
		t.Fatal("expected published empty directory")
	}
	for _, query := range []string{"?Offset=-1", "?Offset=z", "?Offset=30", "?Offset=1&Offset=2", "?Path=..", "?CommitId=bad", "?other=1", "?CommitId=" + id + "&Path=%E8%A7%82%E6%B5%8B54"} {
		assertStatusCode(t, serve(h, "GET", endpoint+query, "", _ownerToken), 400, 1000)
	}
	assertStatusCode(t, serve(h, "GET", endpoint+"?CommitId="+id, "", _otherToken), 404, 2000)
	assertStatusCode(t, serve(h, "GET", endpoint+"?Path=missing", "", _ownerToken), 404, 2000)
	v, e := c.Snapshot(t.Context(), _ownerID, _headLibraryID, id, "观测54", 0)
	if e != nil || v.ID != file || v.Kind != "File" || v.Blocks != 1 {
		t.Fatal("file metadata", e)
	}
	if _, e = c.Snapshot(t.Context(), _ownerID, _headLibraryID, next, "观测54", 0); !errors.Is(e, storage.ErrLibraryNotFound) {
		t.Fatal("missing path")
	}
	if _, e = c.Snapshot(t.Context(), "other", _headLibraryID, id, ".", 0); !errors.Is(e, storage.ErrLibraryNotFound) {
		t.Fatal("other owner read snapshot")
	}
	f, _, err := s.GetObject(t.Context(), _ownerID, _headLibraryID, "files", file)
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	f.Close()
	if err = os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Snapshot(t.Context(), _ownerID, _headLibraryID, id, "观测54", 0); !errors.Is(err, object.ErrInvalidPersistedObject) {
		t.Fatal("corrupt file accepted", err)
	}
}
