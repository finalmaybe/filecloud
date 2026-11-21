package storage

import (
	"context"
	"strings"
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
func (s *Store) DeleteFileVersion(ctx context.Context, owner, lib, path, commit, head string) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO deleted_file_versions(owner_user_id,library_id,path,commit_id)
		SELECT owner_user_id,id,?,? FROM libraries WHERE owner_user_id=? AND id=? AND head_commit_id=?
		ON CONFLICT DO NOTHING`, path, commit, owner, lib, head)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrHeadConflict
	}
	return err
}
