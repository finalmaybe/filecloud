package library

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestOnlineGCCannotSweepAfterValidationBeforeHeadCAS(t *testing.T) {
	dir, _, candidate := newHeadCrashFixture(t)
	ctx := t.Context()
	s, err := storage.OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	old := time.Now().Add(-48 * time.Hour)
	p := filepath.Join(s.ObjectsDir(), _ownerID, _headLibraryID, "commits", candidate[:2], candidate[2:])
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	round, err := s.NewOnlineGCRound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	h := newHeadTestHandler(t, s, time.Now(), Config{BeforeHeadUpdate: func() error {
		report, err := round.Run(ctx)
		if err != nil {
			return err
		}
		if report.Deferred != 1 || report.Objects != 0 {
			return errors.New("GC entered an in-flight publication")
		}
		checked = true
		return nil
	}})
	publishHead(t, h, candidate, `"head-version-1"`, http.StatusOK, 0)
	if !checked {
		t.Fatal("missed publication boundary")
	}
	report, err := round.Run(ctx)
	if err != nil || report.Completed != 1 {
		t.Fatal(report, err)
	}
	assertHeadGraphReadable(t, s, candidate)
}

func TestOnlineGCRespectsTrashPinThenRestores(t *testing.T) {
	dir, old, _ := newHeadCrashFixture(t)
	ctx := t.Context()
	s, err := storage.OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	l, err := s.GetLibrary(ctx, _ownerID, _headLibraryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRetention(ctx, l, storage.Retention{HistoryDays: 1, TrashDays: 30}, now); err != nil {
		t.Fatal(err)
	}
	h := newHeadTestHandler(t, s, now, Config{}).(*Catalog)
	if err := h.MoveToTrash(ctx, _ownerID, VersionTarget{LibraryID: _headLibraryID, Path: "old.txt", CommitID: old}); err != nil {
		t.Fatal(err)
	}
	// Age the publication clock and the bytes independently; the bin deadline stays in the future.
	age := now.Add(-48 * time.Hour)
	if _, err = s.DB().ExecContext(ctx, `UPDATE published_commits SET published_at=? WHERE commit_id=?`, age.Unix(), old); err != nil {
		t.Fatal(err)
	}
	err = filepath.Walk(s.ObjectsDir(), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		return os.Chtimes(p, age, age)
	})
	if err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	s, err = storage.OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	round, err := s.NewOnlineGCRound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report, err := round.Run(ctx)
	if err != nil || report.Completed != 1 || report.Objects == 0 {
		t.Fatal(report, err)
	}
	h = newHeadTestHandler(t, s, now, Config{}).(*Catalog)
	if _, err := h.Snapshot(ctx, _ownerID, _headLibraryID, old, ".", 0); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("old snapshot not expired", err)
	}
	items, err := h.ListTrash(ctx, _ownerID, _headLibraryID, 0)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if err := h.RestoreTrash(ctx, _ownerID, _headLibraryID, items[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	l, err = s.GetLibrary(ctx, _ownerID, _headLibraryID)
	if err != nil {
		t.Fatal(err)
	}
	assertHeadGraphReadable(t, s, *l.HeadCommitID)
}
