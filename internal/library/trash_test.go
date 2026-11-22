package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestTrashDirectoryVersionRestoreAndOfflineCollection(t *testing.T) {
	ctx := t.Context()
	h, s, start := newTestHandler(t)
	now := start
	c := h.(*Catalog)
	c.core.now = func() time.Time { return now }
	const lib = _headLibraryID
	const owner = _ownerID
	assertStatusCode(t, serve(h, "PUT", "/v1/libraries/"+lib, `{"Name":"bin","TrashDays":30,"HistoryDays":1}`, _ownerToken), 201, 0)
	dir := func(entries ...object.DirectoryEntry) string {
		return putMetadata(t, s, "directories", string(object.Directory{Entries: entries}.Bytes()))
	}
	entry := func(name, kind, id string) object.DirectoryEntry {
		return object.DirectoryEntry{Name: name, Type: kind, ID: id, ModifiedAt: start.UTC().Truncate(time.Second).Format(time.RFC3339)}
	}
	files := map[string]string{}
	for _, content := range []string{"hidden", "first", "last", "unreferenced"} {
		id := object.ID([]byte(content))
		if _, err := s.PutObject(ctx, owner, lib, "blocks", id, strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(s.ObjectsDir(), owner, lib, "blocks", id[:2], id[2:])
		if err := os.Chtimes(file, start, start); err != nil {
			t.Fatal(err)
		}
		files[content] = putMetadata(t, s, "files", fmt.Sprintf(`{"Blocks":[%q],"Size":"%d","Type":"File","Version":1}`, id, len(content)))
	}
	version := 0
	var parents []string
	publish := func(root string) string {
		id := putCommit(t, s, owner, parents, root)
		publishHead(t, h, id, headETag(int64(version)), 200, 0)
		version++
		parents = []string{id}
		return id
	}
	root := func(content string) string {
		return dir(entry("folder", "Directory", dir(entry("child", "File", files[content]), entry("empty", "Directory", dir()))))
	}
	hidden := publish(root("hidden"))
	first := publish(root("first"))
	last := publish(root("last"))
	target := VersionTarget{LibraryID: lib, Path: "folder/child", CommitID: hidden}
	if err := c.DeleteFileVersion(ctx, owner, target, last); err != nil {
		t.Fatal(err)
	}
	head := publish(dir(entry("folder", "File", files["last"])))
	items, err := c.ListTrash(ctx, owner, lib, 0)
	if err != nil || len(items) != 2 || items[0].Kind != "Directory" || items[0].Path != "folder" || items[1].Kind != "Version" || !items[0].ExpiresAt.Equal(start.Truncate(time.Second).AddDate(0, 0, 30)) {
		t.Fatalf("trash %+v %v", items, err)
	}
	// Reset the configured bin period after deletion. Its existing deadlines must not move.
	l, err := s.GetLibrary(ctx, owner, lib)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRetention(ctx, l, storage.Retention{TrashDays: 1, HistoryDays: 1}, now); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Dir(s.ObjectsDir())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	now = start.AddDate(0, 0, 3)
	gc, err := storage.OpenGarbageCollector(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		report, err := gc.Collect(ctx, storage.GarbageCollectionOptions{Now: now, DryRun: dry, GracePeriod: 24 * time.Hour})
		if err != nil || report.Objects[0].Count != 1 {
			t.Fatalf("dry=%v report=%+v err=%v", dry, report, err)
		}
	}
	if err := gc.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = storage.OpenForServe(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	c, err = NewHandler(s, nil, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Snapshot(ctx, owner, lib, first, ".", 0); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("expired snapshot:", err)
	}
	if err := c.RestoreTrash(ctx, _otherID, lib, items[0].ID, ""); !errors.Is(err, storage.ErrLibraryNotFound) {
		t.Fatal("other owner:", err)
	}
	if err := c.RestoreTrash(ctx, owner, lib, items[0].ID, ""); !errors.Is(err, ErrRestoreOccupied) {
		t.Fatal("collision:", err)
	}
	if err := c.RestoreTrash(ctx, owner, lib, items[0].ID, "copy"); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreTrash(ctx, owner, lib, items[0].ID, "again"); !errors.Is(err, storage.ErrLibraryNotFound) {
		t.Fatal("double restore:", err)
	}
	current, err := s.GetLibrary(ctx, owner, lib)
	if err != nil || *current.HeadCommitID == head || current.HeadVersion != 5 {
		t.Fatal(current, err)
	}
	snap, err := c.Snapshot(ctx, owner, lib, *current.HeadCommitID, "copy/empty", 0)
	if err != nil || snap.Kind != "Directory" {
		t.Fatal(snap, err)
	}
	p, err := c.FileHistory(ctx, owner, lib, *current.HeadCommitID, "copy/child", "")
	if err != nil || len(p.Versions) != 3 {
		t.Fatalf("restored history %+v %v", p, err)
	}
	readVersion := func(v VersionTarget, want string) {
		f, err := c.OpenFileVersion(ctx, owner, v)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(f.Content(ctx))
		if err != nil || string(data) != want {
			t.Fatal(string(data), err)
		}
	}
	readVersion(VersionTarget{LibraryID: lib, Path: "copy/child", CommitID: first}, "first")
	if _, err := c.OpenFileVersion(ctx, owner, VersionTarget{LibraryID: lib, Path: "copy/child", CommitID: hidden}); !errors.Is(err, storage.ErrLibraryNotFound) {
		t.Fatal("separately deleted version came back", err)
	}
	if err := c.RestoreTrash(ctx, owner, lib, items[1].ID, ""); err != nil {
		t.Fatal(err)
	}
	readVersion(target, "hidden")
	after, err := s.GetLibrary(ctx, owner, lib)
	if err != nil || after.HeadVersion != current.HeadVersion {
		t.Fatal("putting back a version published a head", err)
	}
	move := VersionTarget{LibraryID: lib, Path: "copy", CommitID: head}
	if err := c.MoveToTrash(ctx, owner, move); !errors.Is(err, storage.ErrHeadConflict) {
		t.Fatal("stale delete accepted", err)
	}
	move.CommitID = *after.HeadCommitID
	if err := c.MoveToTrash(ctx, owner, move); err != nil {
		t.Fatal(err)
	}
	items, err = c.ListTrash(ctx, owner, lib, 0)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	now = items[0].ExpiresAt
	if err := c.RestoreTrash(ctx, owner, lib, items[0].ID, ""); !errors.Is(err, storage.ErrLibraryNotFound) {
		t.Fatal("restored expired trash", err)
	}
	if items, err := c.ListTrash(ctx, owner, lib, 0); err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}
