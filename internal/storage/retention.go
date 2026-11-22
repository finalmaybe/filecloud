package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

// Zero inherits the server default; -1 keeps the pre-upgrade permanent history.
type Retention struct{ TrashDays, HistoryDays int }

var retentionMigration = []string{
	`CREATE TABLE retention_defaults (id INTEGER PRIMARY KEY CHECK(id=1), trash_days INTEGER NOT NULL, history_days INTEGER NOT NULL)`,
	`INSERT INTO retention_defaults VALUES(1,30,60)`,
	`ALTER TABLE libraries ADD COLUMN trash_days INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE libraries ADD COLUMN history_days INTEGER NOT NULL DEFAULT 0`,
	`UPDATE libraries SET history_days=-1`,
	`ALTER TABLE published_commits ADD COLUMN published_at INTEGER NOT NULL DEFAULT 0`,
	// Earlier releases did not record publication time. Start their retention clock at upgrade.
	`UPDATE published_commits SET published_at=unixepoch()`,
	`ALTER TABLE published_commits ADD COLUMN pruned INTEGER NOT NULL DEFAULT 0`,
	`CREATE TABLE trash_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT, owner TEXT NOT NULL, library TEXT NOT NULL,
		path TEXT NOT NULL, kind TEXT NOT NULL, object_id TEXT NOT NULL, modified TEXT NOT NULL,
		source TEXT NOT NULL, deleted_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, restored INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY(owner,library) REFERENCES libraries(owner_user_id,id) ON DELETE CASCADE
	)`,
	`CREATE TABLE trash_versions (
		trash_id INTEGER NOT NULL REFERENCES trash_entries(id) ON DELETE CASCADE,
		path TEXT NOT NULL, source TEXT NOT NULL, object_id TEXT NOT NULL,
		modified TEXT NOT NULL, change TEXT NOT NULL, PRIMARY KEY(trash_id,path,source)
	)`,
	`CREATE INDEX trash_versions_path ON trash_versions(path,source,trash_id)`,
	// restored is the server restore timestamp (zero while in the bin). An archive
	// placed back on the timeline gets a history window and keeps its original deadline.
	`CREATE VIEW trash_lifetimes AS
	 SELECT t.*, CASE WHEN restored=0 THEN expires_at WHEN l.history_days=-1 THEN 253402300799
	 ELSE MAX(expires_at, restored+86400*COALESCE(NULLIF(l.history_days,0),d.history_days)) END AS keep_until
	 FROM trash_entries t JOIN libraries l ON l.id=t.library AND l.owner_user_id=t.owner CROSS JOIN retention_defaults d`,
	`CREATE VIEW trash_content AS
	 SELECT owner,library,kind,object_id,keep_until AS expires_at,restored,expires_at AS bin_deadline FROM trash_lifetimes WHERE restored=0
	 UNION ALL SELECT t.owner,t.library,'File',v.object_id,t.keep_until,t.restored,t.expires_at FROM trash_versions v JOIN trash_lifetimes t ON t.id=v.trash_id`,
	`CREATE INDEX trash_library ON trash_entries(owner,library,restored,id)`,
	`CREATE INDEX trash_entries_source ON trash_entries(owner,library,path,source,kind)`,
}

func (s *Store) Defaults(ctx context.Context) (Retention, error) {
	var p Retention
	err := s.db.QueryRowContext(ctx, `SELECT trash_days,history_days FROM retention_defaults`).Scan(&p.TrashDays, &p.HistoryDays)
	return p, err
}

func (s *Store) SetDefaults(ctx context.Context, p Retention, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := sealExpired(ctx, tx, `l.history_days=0 AND (SELECT history_days FROM retention_defaults)<>?`, now, p.HistoryDays); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE retention_defaults SET trash_days=?,history_days=?`, p.TrashDays, p.HistoryDays); err != nil {
		return err
	}
	return tx.Commit()
}

// Freeze the old boundary when changing policies, even if offline GC has not run yet.
func sealExpired(ctx context.Context, tx *sql.Tx, scope string, now time.Time, scopeArgs ...any) error {
	args := append([]any{now.Unix()}, scopeArgs...)
	if _, err := tx.ExecContext(ctx, `UPDATE published_commits SET pruned=1 WHERE pruned=0 AND (owner_user_id,library_id,commit_id) IN (SELECT p.owner_user_id,p.library_id,p.commit_id `+expiryFrom+` WHERE `+expiredPredicate+` AND `+scope+`)`, args...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM trash_entries WHERE id IN (SELECT t.id FROM trash_lifetimes t JOIN libraries l ON t.owner=l.owner_user_id AND t.library=l.id WHERE t.restored<>0 AND t.keep_until<=? AND `+scope+`)`, args...)
	return err
}

const expiryFrom = ` FROM published_commits p JOIN libraries l ON l.owner_user_id=p.owner_user_id AND l.id=p.library_id CROSS JOIN retention_defaults d `
const expiredPredicate = `p.commit_id<>l.head_commit_id AND (p.pruned OR (l.history_days>=0 AND p.published_at+86400*COALESCE(NULLIF(l.history_days,0),d.history_days)<=?))`

func (s *Store) SnapshotExpired(ctx context.Context, owner, lib, id string, now time.Time) (bool, error) {
	var expired bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 `+expiryFrom+` WHERE p.owner_user_id=? AND p.library_id=? AND p.commit_id=? AND `+expiredPredicate+`)`, owner, lib, id, now.Unix()).Scan(&expired)
	return expired, err
}

func expiredRoots(ctx context.Context, db *sql.DB, query string, args ...any) (map[gcRoot]bool, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[gcRoot]bool{}
	for rows.Next() {
		var key gcRoot
		if err := rows.Scan(&key.owner, &key.library, &key.id); err != nil {
			return nil, err
		}
		m[key] = true
	}
	return m, rows.Err()
}
func trashRoots(ctx context.Context, db *sql.DB, now time.Time) ([]gcObjectKey, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT owner,library,kind,object_id FROM trash_content WHERE expires_at>?`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gcObjectKey
	for rows.Next() {
		var key gcObjectKey
		if err := rows.Scan(&key.owner, &key.library, &key.kind, &key.id); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

type TrashEntry struct {
	ID                   int64
	Path, Kind, Source   string
	Entry                object.DirectoryEntry
	DeletedAt, ExpiresAt time.Time
	Versions             []ArchivedVersion
}
type HeadChange struct {
	Removed     []TrashEntry
	RestoreID   int64
	Destination string
}

func insertTrash(ctx context.Context, tx *sql.Tx, owner, lib string, t TrashEntry, now time.Time) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO trash_entries(owner,library,path,kind,source,object_id,modified,deleted_at,expires_at)
		SELECT ?,?,?,?,?,?,?,?,?+86400*COALESCE(NULLIF(l.trash_days,0),d.trash_days)
		FROM libraries l CROSS JOIN retention_defaults d WHERE l.owner_user_id=? AND l.id=?`, owner, lib, t.Path, t.Kind, t.Source, t.Entry.ID, t.Entry.ModifiedAt, now.Unix(), now.Unix(), owner, lib)
	if err != nil || len(t.Versions) == 0 {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO trash_versions(trash_id,path,source,object_id,modified,change)
		SELECT ?,?,?,?,?,? WHERE ?='Version' OR NOT EXISTS (
			SELECT 1 FROM deleted_file_versions WHERE owner_user_id=? AND library_id=? AND path=? AND commit_id=?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range t.Versions {
		if _, err := stmt.ExecContext(ctx, id, v.Path, v.Source, v.FileID, v.Modified, v.Change, t.Kind, owner, lib, v.Path, v.Source); err != nil {
			return err
		}
	}
	if t.Kind != "Version" {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO trash_versions(trash_id,path,source,object_id,modified,change)
			SELECT ?,v.path,v.source,v.object_id,v.modified,v.change
			FROM trash_versions v JOIN trash_lifetimes t ON t.id=v.trash_id
			WHERE t.owner=? AND t.library=? AND t.restored<>0 AND t.keep_until>?
			AND EXISTS (SELECT 1 FROM trash_versions f WHERE f.trash_id=? AND f.path=v.path)
			AND NOT EXISTS (SELECT 1 FROM deleted_file_versions x WHERE x.owner_user_id=t.owner
				AND x.library_id=t.library AND x.path=v.path AND x.commit_id=v.source)`, id, owner, lib, now.Unix(), id)
	}
	return err
}

const trashSelect = `SELECT id,path,kind,source,object_id,modified,deleted_at,expires_at FROM trash_entries WHERE owner=? AND library=? AND restored=0 AND expires_at>? `

func scanTrash(row libraryScanner) (TrashEntry, error) {
	var t TrashEntry
	var deleted, expires int64
	err := row.Scan(&t.ID, &t.Path, &t.Kind, &t.Source, &t.Entry.ID, &t.Entry.ModifiedAt, &deleted, &expires)
	t.Entry.Type = t.Kind
	if t.Kind == "Version" {
		t.Entry.Type = "File"
	}
	t.DeletedAt = time.Unix(deleted, 0).UTC()
	t.ExpiresAt = time.Unix(expires, 0).UTC()
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrLibraryNotFound
	}
	return t, err
}
func (s *Store) Trash(ctx context.Context, owner, lib string, now time.Time, before int64) ([]TrashEntry, error) {
	rows, err := s.db.QueryContext(ctx, trashSelect+`AND (?=0 OR id<?) ORDER BY id DESC LIMIT 101`, owner, lib, now.Unix(), before, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []TrashEntry
	for rows.Next() {
		t, err := scanTrash(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, t)
	}
	return entries, rows.Err()
}
func (s *Store) TrashItem(ctx context.Context, owner, lib string, id int64, now time.Time) (TrashEntry, error) {
	return scanTrash(s.db.QueryRowContext(ctx, trashSelect+`AND id=?`, owner, lib, now.Unix(), id))
}

const restoreEntrySQL = `UPDATE trash_entries SET restored=? WHERE owner=? AND library=? AND id=? AND restored=0 AND expires_at>?`

func consumeTrash(ctx context.Context, tx *sql.Tx, owner, lib string, change HeadChange, now time.Time) error {
	res, err := tx.ExecContext(ctx, restoreEntrySQL, now.Unix(), owner, lib, change.RestoreID, now.Unix())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if n != 1 && err == nil {
		return ErrLibraryNotFound
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE trash_versions SET path=? || substr(path,1+(SELECT length(path) FROM trash_entries WHERE id=?)) WHERE trash_id=?`, change.Destination, change.RestoreID, change.RestoreID)
	}
	return err
}

func (s *Store) RestoreVersionRecord(ctx context.Context, owner, lib string, t TrashEntry, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, restoreEntrySQL, now.Unix(), owner, lib, t.ID, now.Unix())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLibraryNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deleted_file_versions WHERE owner_user_id=? AND library_id=? AND path=? AND commit_id=?`, owner, lib, t.Path, t.Source); err != nil {
		return err
	}
	return tx.Commit()
}
