package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

func TestRetentionPreviewAndSweepExpiredPublishedBlocks(t *testing.T) {
	ctx := t.Context()
	dir, _, preserved := newGarbageCollectionFixture(t)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	for path := range preserved {
		if err := os.Chtimes(path, now.AddDate(0, 0, -2), now.AddDate(0, 0, -2)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.GetLibrary(ctx, _gcTestOwnerA, _gcTestLibrary)
	if err != nil {
		t.Fatal(err)
	}
	next := Retention{HistoryDays: 10}
	preview, err := s.PreviewRetention(ctx, l, next, now)
	if err != nil || preview.Count != 2 || preview.Bytes != int64(len("basebranch")) || preview.Token == "" || preview.First != "2026-08-18" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if err := s.SetRetention(ctx, l, next, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := OpenGarbageCollector(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		if _, err := g.Collect(ctx, GarbageCollectionOptions{Now: now, GracePeriod: 24 * time.Hour, DryRun: dry}); err != nil {
			t.Fatal(err)
		}
		for _, data := range []string{"base", "branch", "current"} {
			path := gcTestObjectPath(dir, _gcTestOwnerA, _gcTestLibrary, "blocks", object.ID([]byte(data)))
			_, err := os.Stat(path)
			if dry || data == "current" {
				if err != nil {
					t.Fatal(data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("old published block survived", data, err)
			}
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	check, err := OpenIntegrityChecker(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	report, err := check.Check(ctx)
	if err != nil || len(report.Issues) != 0 {
		t.Fatal(report, err)
	}
	if err := check.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err = s.GetLibrary(ctx, _gcTestOwnerA, _gcTestLibrary)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRetention(ctx, l, Retention{HistoryDays: -1}, now); err != nil {
		t.Fatal(err)
	}
	_, commit := integrityFixtureCommit(t, preserved, "2026-08-18T12:00:02Z")
	expired, err := s.SnapshotExpired(ctx, l.OwnerUserID, l.ID, commit.Parents[0], now)
	if err != nil || !expired {
		t.Fatal("swept snapshot revived", err)
	}
	if ok, err := s.HasObject(ctx, l.OwnerUserID, l.ID, "commits", commit.Parents[0]); err != nil || !ok {
		t.Fatal("baseline metadata lost", err)
	}
}

func TestMigrationKeepsExistingLibraryHistoryPermanent(t *testing.T) {
	ctx := t.Context()
	db, err := openDB(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := metadataMigrations()
	if err := migrate(ctx, db, m[:7]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users VALUES('u','u','u','hash',0,'old','old');INSERT INTO libraries(id,owner_user_id,name,name_key,created_at,updated_at) VALUES('old','u','old','old','old','old')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, m); err != nil {
		t.Fatal(err)
	}
	var days int
	if err := db.QueryRow(`SELECT history_days FROM libraries`).Scan(&days); err != nil || days != -1 {
		t.Fatal(days, err)
	}
	s := Store{db: db}
	d, err := s.Defaults(ctx)
	if err != nil || d != (Retention{TrashDays: 30, HistoryDays: 60}) {
		t.Fatal(d, err)
	}
	lib, _, err := s.CreateLibrary(ctx, Library{OwnerUserID: "u", ID: "new", Name: "new"}, time.Now())
	if err != nil || lib.Retention != (Retention{}) {
		t.Fatal(lib.Retention, err)
	}
}
