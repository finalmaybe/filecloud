package library

import (
	"errors"
	"fmt"
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
	p, err := c.FileHistory(ctx, _ownerID, _headLibraryID, head, path, "")
	if err != nil || len(p.Versions) != 0 || p.Next == "" {
		t.Fatalf("unchanged page: %d versions, continuation=%t, err=%v", len(p.Versions), p.Next != "", err)
	}
	publish(empty) // Advancing Head does not change the file or commits selected by the cursor.
	p, err = c.FileHistory(ctx, _ownerID, _headLibraryID, head, path, p.Next)
	if err != nil || p.Next != "" || p.Anchor != head {
		t.Fatalf("older page: continuation=%t, err=%v", p.Next != "", err)
	}
	var got []string
	for _, v := range p.Versions {
		got = append(got, v.CommitID)
	}
	if !slices.Equal(got, []string{restored, touched, updated, initial}) {
		t.Fatalf("unexpected versions: %v", got)
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
