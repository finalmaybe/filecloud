package storage

import (
	"errors"
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

func TestDeleteTrashAtomicallyScopesRecordsAndCascadesArchives(t *testing.T) {
	s := openAccountStore(t)
	defer closeAccountStore(t, s)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	for _, user := range []User{{ID: "mine", Username: "alice", PasswordHash: "hash"}, {ID: "theirs", Username: "bob", PasswordHash: "hash"}} {
		if err := s.CreateUser(ctx, user, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []Library{{OwnerUserID: "mine", ID: "bin", Name: "bin"}, {OwnerUserID: "mine", ID: "other", Name: "other"}, {OwnerUserID: "theirs", ID: "bin", Name: "bin"}} {
		if _, _, err := s.CreateLibrary(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	add := func(owner, lib, kind string, restored int64, expires time.Time) int64 {
		t.Helper()
		var id int64
		if err := s.db.QueryRowContext(ctx, `INSERT INTO trash_entries(owner,library,path,kind,object_id,modified,source,deleted_at,expires_at,restored)
			VALUES(?,?,'same-path',?,'object','modified','commit',?,?,?) RETURNING id`, owner, lib, kind, now.Add(-time.Hour).Unix(), expires.Unix(), restored).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO trash_versions VALUES(?,'same-path','commit','object','modified','content')`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	future := now.Add(time.Hour)
	file := add("mine", "bin", "File", 0, future)
	dir := add("mine", "bin", "Directory", 0, future)
	version := add("mine", "bin", "Version", 0, future)
	unselected := add("mine", "bin", "File", 0, future)
	restored := add("mine", "bin", "Version", now.Unix(), future)
	expired := add("mine", "bin", "File", 0, now)
	otherLibrary := add("mine", "other", "File", 0, future)
	otherOwner := add("theirs", "bin", "File", 0, future)

	assertCounts := func(id int64, want int) {
		t.Helper()
		var records, archives int
		if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM trash_entries WHERE id=?),
			(SELECT COUNT(*) FROM trash_versions WHERE trash_id=?)`, id, id).Scan(&records, &archives); err != nil || records != want || archives != want {
			t.Fatalf("id %d: records=%d archives=%d, want %d each, err=%v", id, records, archives, want, err)
		}
	}
	for _, invalid := range []int64{otherOwner, otherLibrary, restored, expired, otherOwner + 1} {
		if err := s.DeleteTrash(ctx, "mine", "bin", []int64{file, invalid, dir}, now); !errors.Is(err, ErrLibraryNotFound) {
			t.Fatalf("invalid id %d: %v", invalid, err)
		}
		assertCounts(file, 1)
		assertCounts(dir, 1)
	}
	if err := s.DeleteTrash(ctx, "mine", "bin", []int64{file, dir, version}, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{file, dir, version} {
		assertCounts(id, 0)
	}
	for _, id := range []int64{unselected, restored, expired, otherLibrary, otherOwner} {
		assertCounts(id, 1)
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
