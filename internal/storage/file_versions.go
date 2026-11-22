package storage

import (
	"context"
	"strings"
	"time"
)

// DeletedFileVersions returns the removed timeline entries among the supplied commits.
func (s *Store) DeletedFileVersions(ctx context.Context, owner, lib, path string, commits []string) (map[string]bool, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	args := []any{owner, lib, path}
	for _, id := range commits {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT commit_id FROM deleted_file_versions
		WHERE owner_user_id=? AND library_id=? AND path=? AND commit_id IN (?`+strings.Repeat(",?", len(commits)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deleted := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		deleted[id] = true
	}
	return deleted, rows.Err()
}

// DeleteFileVersion removes a timeline entry without changing immutable snapshots.
func (s *Store) DeleteFileVersion(ctx context.Context, owner, lib string, t TrashEntry, head string, now time.Time) error {
	done := s.UseLibrary(owner, lib)
	defer done()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO deleted_file_versions(owner_user_id,library_id,path,commit_id)
		SELECT owner_user_id,id,?,? FROM libraries WHERE owner_user_id=? AND id=? AND head_commit_id=?
		ON CONFLICT DO NOTHING`, t.Path, t.Source, owner, lib, head)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrHeadConflict
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM trash_entries WHERE owner=? AND library=? AND path=? AND source=? AND kind='Version' AND restored<>0`, owner, lib, t.Path, t.Source); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM trash_versions WHERE path=? AND source=? AND trash_id IN (SELECT id FROM trash_entries WHERE owner=? AND library=? AND restored<>0)`, t.Path, t.Source, owner, lib); err != nil {
		return err
	}
	if err = insertTrash(ctx, tx, owner, lib, t, now); err != nil {
		return err
	}
	return tx.Commit()
}
