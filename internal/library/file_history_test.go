package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestFileHistoryFollowsPathAcrossMainlinePages(t *testing.T) {
	h, store, _ := newTestHandler(t)
	defer closeStore(t, store)
	createHeadLibrary(t, h)
	c := h.(*Catalog)
	ctx := t.Context()
	empty := putMetadata(t, store, "directories", `{"Entries":[],"Type":"Directory","Version":1}`)
	file := putMetadata(t, store, "files", `{"Blocks":[],"Size":"0","Type":"File","Version":1}`)
	block := object.ID([]byte("x"))
	if _, err := store.PutObject(ctx, _ownerID, _headLibraryID, "blocks", block, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	changed := putMetadata(t, store, "files", fmt.Sprintf(`{"Blocks":[%q],"Size":"1","Type":"File","Version":1}`, block))
	dir := func(name, kind, id string, second int) string {
		return putMetadata(t, store, "directories", fmt.Sprintf(`{"Entries":[{"Id":%q,"ModifiedAt":"2026-08-09T00:00:%02dZ","Name":%q,"Type":%q}],"Type":"Directory","Version":1}`, id, second, name, kind))
	}
	version := 0
	var parents []string
	publish := func(root string) string {
		id := putCommit(t, store, _ownerID, parents, root)
		publishHead(t, h, id, fmt.Sprintf(`"head-version-%d"`, version), 200, 0)
		version++
		parents = []string{id}
		return id
	}
	initial := publish(dir("notes", "Directory", dir("a.txt", "File", file, 0), 0))
	updated := publish(dir("notes", "Directory", dir("a.txt", "File", changed, 0), 0))
	subtree := dir("a.txt", "File", changed, 1)
	touched := publish(dir("notes", "Directory", subtree, 0))
	publish(empty)
	restored := publish(dir("notes", "Directory", subtree, 1))
	root := dir("notes", "Directory", subtree, 2) // Only the parent directory changed.
	var head string
	for range 50 {
		head = publish(root)
	}
	const path = "notes/a.txt"
	const endpoint = "/v1/libraries/" + _headLibraryID + "/file-history?"
	query := url.Values{"CommitId": {head}, "Path": {path}}
	readPage := func() FileHistory {
		t.Helper()
		res := serve(h, "GET", endpoint+query.Encode(), "", _ownerToken)
		assertStatusCode(t, res, 200, 0)
		var result struct{ FileHistory FileHistory }
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.FileHistory
	}
	p := readPage()
	if p.Versions == nil || len(p.Versions) != 0 || p.Next == "" || p.Current == nil || p.Current.FileID != changed || p.Current.ModifiedAt != "2026-08-09T00:00:01Z" {
		t.Fatalf("unchanged page: %d versions, continuation=%t", len(p.Versions), p.Next != "")
	}
	publish(empty) // Advancing Head does not change the file or commits selected by the cursor.
	query.Set("PageToken", p.Next)
	p = readPage()
	if p.Next != "" || p.Anchor != head {
		t.Fatalf("older page: continuation=%t", p.Next != "")
	}
	var got, changes []string
	for _, v := range p.Versions {
		got = append(got, v.CommitID)
		changes = append(changes, v.Change)
	}
	if p.Current != nil || !slices.Equal(changes, []string{"added", "modified-time", "content", "added"}) {
		t.Fatalf("unexpected continuation metadata: %v, %v", p.Current, changes)
	}
	if !slices.Equal(got, []string{restored, touched, updated, initial}) {
		t.Fatalf("unexpected versions: %v", got)
	}
	query.Del("PageToken")
	params := query.Encode()
	assertStatusCode(t, serve(h, "GET", endpoint+params, "", _otherToken), 404, 2000)
	assertStatusCode(t, serve(h, "GET", endpoint+"bad", "", ""), 401, 1001)
	for _, invalid := range []string{"Path=a", "CommitId=" + head, params + "&Path=other", params + "&PageSize=1", params + "&PageToken=", params + "&PageToken=invalid"} {
		assertStatusCode(t, serve(h, "GET", endpoint+invalid, "", _ownerToken), 400, 1000)
	}
	for _, tc := range []struct {
		owner, anchor, path, cursor string
		want                        error
	}{
		{_ownerID, head, "notes", "", storage.ErrLibraryNotFound},
		{_ownerID, head, "absent.txt", "", storage.ErrLibraryNotFound},
		{_ownerID, head, "../a.txt", "", ErrInvalidPath},
		{_ownerID, putCommit(t, store, _ownerID, parents, root), path, "", storage.ErrLibraryNotFound},
		{"other", head, path, "", storage.ErrLibraryNotFound},
		{_ownerID, head, path, "invalid", ErrInvalidPage},
	} {
		if _, err := c.FileHistory(ctx, tc.owner, _headLibraryID, tc.anchor, tc.path, tc.cursor); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.path, err, tc.want)
		}
	}
}
