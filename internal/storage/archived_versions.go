package storage

import (
	"context"
	"strings"
	"time"
)

type ArchivedVersion struct{ Path, Source, FileID, Modified, Change string }

// ArchivedAtPath also supplies records when a directory was restored under a new name.
func (s *Store) ArchivedAtPath(ctx context.Context, owner, lib, path string, ids []string, now time.Time) (map[string]ArchivedVersion, error) {
	out := map[string]ArchivedVersion{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{owner, lib, now.Unix(), path}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.source,v.object_id,v.modified,v.change FROM trash_versions v JOIN trash_lifetimes t ON t.id=v.trash_id WHERE t.owner=? AND t.library=? AND t.restored<>0 AND t.keep_until>? AND v.path=? AND v.source IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY t.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := ArchivedVersion{Path: path}
		if err := rows.Scan(&v.Source, &v.FileID, &v.Modified, &v.Change); err != nil {
			return nil, err
		}
		if _, ok := out[v.Source]; !ok {
			out[v.Source] = v
		}
	}
	return out, rows.Err()
}
