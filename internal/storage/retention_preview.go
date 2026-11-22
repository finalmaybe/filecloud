package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

type RetentionPreview struct {
	Count, RestoredRecords int
	First, Last            string
	Bytes                  int64
	Token                  string
}

// PreviewRetention compares content blocks, not file sizes: shared blocks cannot be freed twice.
func (s *Store) PreviewRetention(ctx context.Context, l Library, next Retention, now time.Time) (RetentionPreview, error) {
	var out RetentionPreview
	defaults, err := s.Defaults(ctx)
	if err != nil {
		return out, err
	}
	days := next.HistoryDays
	if days == 0 {
		days = defaults.HistoryDays
	}
	previous := l.HistoryDays
	if previous == 0 {
		previous = defaults.HistoryDays
	}
	if days < 0 || (previous >= 0 && days >= previous) {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.commit_id,p.published_at,p.commit_id<>l.head_commit_id AND p.published_at+86400*?<=? `+expiryFrom+` WHERE p.owner_user_id=? AND p.library_id=? AND NOT (`+expiredPredicate+`) ORDER BY p.published_at,p.commit_id`, days, now.Unix(), l.OwnerUserID, l.ID, now.Unix())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type snapshot struct {
		id      string
		expires bool
	}
	var snapshots []snapshot
	for rows.Next() {
		var r snapshot
		var at int64
		if err := rows.Scan(&r.id, &at, &r.expires); err != nil {
			return out, err
		}
		snapshots = append(snapshots, r)
		if r.expires {
			day := time.Unix(at, 0).UTC().Format(time.DateOnly)
			if out.Count == 0 {
				out.First = day
			}
			out.Count++
			out.Last = day
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trash_content WHERE owner=? AND library=? AND expires_at>? AND restored<>0 AND max(bin_deadline,restored+86400*?)<=?`, l.OwnerUserID, l.ID, now.Unix(), days, now.Unix()).Scan(&out.RestoredRecords)
	if err != nil {
		return out, err
	}
	if out.Count == 0 && out.RestoredRecords == 0 {
		out.Token = out.confirmation(l, next, defaults)
		return out, nil
	}
	g := GarbageCollector{objectsDir: s.objectsDir}
	live := map[gcObjectKey]struct{}{}
	old := map[gcObjectKey]struct{}{}
	for _, r := range snapshots {
		data, err := g.readPublishedObject(gcObjectKey{l.OwnerUserID, l.ID, "commits", r.id})
		if err != nil {
			return out, err
		}
		commit, err := object.VerifyCommit(data, r.id)
		if err != nil {
			return out, err
		}
		target := live
		if r.expires {
			target = old
		}
		if err := g.markSnapshot(ctx, target, l.OwnerUserID, l.ID, commit.Root, true); err != nil {
			return out, err
		}
	}
	pins, err := s.db.QueryContext(ctx, `SELECT DISTINCT kind,object_id,restored<>0 AND max(bin_deadline,restored+86400*?)<=? FROM trash_content WHERE owner=? AND library=? AND expires_at>?`, days, now.Unix(), l.OwnerUserID, l.ID, now.Unix())
	if err != nil {
		return out, err
	}
	defer pins.Close()
	for pins.Next() {
		var kind, id string
		var expires bool
		if err := pins.Scan(&kind, &id, &expires); err != nil {
			return out, err
		}
		target := live
		if expires {
			target = old
		}
		if kind == "Directory" {
			err = g.markSnapshot(ctx, target, l.OwnerUserID, l.ID, id, true)
		} else {
			err = g.markFile(ctx, target, l.OwnerUserID, l.ID, id, true)
		}
		if err != nil {
			return out, err
		}
	}
	if err := pins.Err(); err != nil {
		return out, err
	}
	for k := range old {
		if k.kind != "blocks" {
			continue
		}
		if _, kept := live[k]; kept {
			continue
		}
		info, err := os.Stat(filepath.Join(s.objectsDir, k.owner, k.library, k.kind, k.id[:2], k.id[2:]))
		if err != nil {
			return out, err
		}
		out.Bytes += info.Size()
	}
	out.Token = out.confirmation(l, next, defaults)
	return out, nil
}

func (p RetentionPreview) confirmation(l Library, next, defaults Retention) string {
	return object.ID([]byte(fmt.Sprintf("%s:%s:%d:%v:%v:%v:%s:%s:%d:%d:%d", l.OwnerUserID, l.ID, l.HeadVersion, l.Retention, next, defaults, p.First, p.Last, p.Count, p.Bytes, p.RestoredRecords)))
}

// PreviewDefaultHistory summarizes only libraries which inherit the global setting.
func (s *Store) PreviewDefaultHistory(ctx context.Context, days int, now time.Time) (RetentionPreview, error) {
	var total RetentionPreview
	rows, err := s.db.QueryContext(ctx, `SELECT owner_user_id,id FROM libraries WHERE history_days=0 ORDER BY owner_user_id,id`)
	if err != nil {
		return total, err
	}
	defer rows.Close()
	for rows.Next() {
		var owner, id string
		if err := rows.Scan(&owner, &id); err != nil {
			return total, err
		}
		l, err := s.GetLibrary(ctx, owner, id)
		if err != nil {
			return total, err
		}
		next := l.Retention
		next.HistoryDays = days
		p, err := s.PreviewRetention(ctx, l, next, now)
		if err != nil {
			return total, err
		}
		total.Count += p.Count
		total.Bytes += p.Bytes
		total.RestoredRecords += p.RestoredRecords
		if p.First != "" && (total.First == "" || p.First < total.First) {
			total.First = p.First
		}
		if p.Last > total.Last {
			total.Last = p.Last
		}
		total.Token = object.ID([]byte(total.Token + p.Token))
	}
	return total, rows.Err()
}

func (s *Store) SetRetention(ctx context.Context, l Library, next Retention, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if l.HistoryDays != next.HistoryDays {
		if err := sealExpired(ctx, tx, `l.owner_user_id=? AND l.id=?`, now, l.OwnerUserID, l.ID); err != nil {
			return err
		}
	}
	r, err := tx.ExecContext(ctx, `UPDATE libraries SET history_days=?,trash_days=? WHERE owner_user_id=? AND id=? AND head_version=? AND trash_days=? AND history_days=?`, next.HistoryDays, next.TrashDays, l.OwnerUserID, l.ID, l.HeadVersion, l.TrashDays, l.HistoryDays)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrHeadConflict
	}
	return tx.Commit()
}
