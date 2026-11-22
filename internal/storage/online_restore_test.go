package storage

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

func TestRestoreVersionRecordDuringOnlineSweep(t *testing.T) {
	ctx := t.Context()
	dir, _, _ := newGarbageCollectionFixture(t)
	s, err := OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := gcScope{_gcTestOwnerA, _gcTestLibrary}
	now := time.Now().UTC()
	l, err := s.GetLibrary(ctx, scope.owner, scope.library)
	if err != nil {
		t.Fatal(err)
	}
	// Delete the old version just before its snapshot expires. By the time GC runs,
	// the bin record alone protects its content.
	at := now.AddDate(0, 0, -61)
	src := putGCTestGraph(t, s, scope.owner, scope.library, []byte("restored during GC"), []string{*l.HeadCommitID}, at, false)
	l, err = s.UpdateLibraryHead(ctx, scope.owner, scope.library, l.HeadCommitID, l.HeadVersion, src.commitID, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	current := putGCTestGraph(t, s, scope.owner, scope.library, []byte("current bytes"), []string{src.commitID}, at.Add(time.Hour), false)
	l, err = s.UpdateLibraryHead(ctx, scope.owner, scope.library, l.HeadCommitID, l.HeadVersion, current.commitID, nil, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	v := ArchivedVersion{Path: "file", Source: src.commitID, FileID: object.ID(src.fileData), Modified: at.Format(time.RFC3339), Change: "content"}
	item := TrashEntry{Path: v.Path, Kind: "Version", Source: v.Source, Entry: object.DirectoryEntry{Type: "File", Name: v.Path, ID: v.FileID, ModifiedAt: v.Modified}, Versions: []ArchivedVersion{v}}
	if err := s.DeleteFileVersion(ctx, scope.owner, scope.library, item, *l.HeadCommitID, now.AddDate(0, 0, -2)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(s.objectPath(scope.owner, scope.library, "blocks", object.ID(src.blockData)), at, at); err != nil {
		t.Fatal(err)
	}
	items, err := s.Trash(ctx, scope.owner, scope.library, now, 0)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	closeObjectStore(t, s)
	s, err = OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeObjectStore(t, s)
	// The fixture's orphan must be eligible independent of the test machine's date.
	orphan := s.objectPath(scope.owner, scope.library, "blocks", object.ID([]byte("expired orphan")))
	if err := os.Chtimes(orphan, at, at); err != nil {
		t.Fatal(err)
	}
	restored := false
	s.syncObjectDirectory = func(path string) error {
		if !restored {
			restored = true
			if err := s.RestoreVersionRecord(ctx, scope.owner, scope.library, items[0], now); err != nil {
				return err
			}
		}
		return syncDirectory(path)
	}
	if _, _, err := s.collectIdleLibrary(ctx, scope, now); !errors.Is(err, errGCYield) || !restored {
		t.Fatal("restore did not invalidate the running sweep", restored, err)
	}
	if _, _, err := s.collectIdleLibrary(ctx, scope, now); err != nil {
		t.Fatal(err)
	}
	expired, err := s.SnapshotExpired(ctx, scope.owner, scope.library, src.commitID, now)
	if err != nil || !expired {
		t.Fatal("source snapshot still available", err)
	}
	archive, err := s.ArchivedAtPath(ctx, scope.owner, scope.library, "file", []string{src.commitID}, now)
	if err != nil || archive[src.commitID].FileID != v.FileID {
		t.Fatal("restored revision lost", err)
	}
	if ok, err := s.HasObject(ctx, scope.owner, scope.library, "blocks", object.ID(src.blockData)); err != nil || !ok {
		t.Fatal("restored bytes lost", err)
	}
	after, err := s.GetLibrary(ctx, scope.owner, scope.library)
	if err != nil || after.HeadVersion != l.HeadVersion {
		t.Fatal("restoring a version changed Head", err)
	}
}
