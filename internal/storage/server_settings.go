package storage

import (
	"context"
	"database/sql"
	"errors"
)

const MaxSavedHistoryConcurrency = 1000

var ErrInvalidHistoryConcurrency = errors.New("history concurrency is out of range")

// HistoryConcurrency holds the global and per-account limits on shared reads.
type HistoryConcurrency struct{ Global, User int }

func (s *Store) HistoryConcurrency(ctx context.Context) (HistoryConcurrency, error) {
	var limits HistoryConcurrency
	err := s.db.QueryRowContext(ctx, `SELECT history_global_capacity, history_user_capacity FROM server_settings WHERE id=1`).Scan(&limits.Global, &limits.User)
	return limits, err
}

func (s *Store) SetHistoryConcurrency(ctx context.Context, limits HistoryConcurrency) error {
	if limits.Global < 1 || limits.Global > MaxSavedHistoryConcurrency || limits.User < 1 || limits.User > MaxSavedHistoryConcurrency {
		return ErrInvalidHistoryConcurrency
	}
	result, err := s.db.ExecContext(ctx, `UPDATE server_settings SET history_global_capacity=?, history_user_capacity=? WHERE id=1`, limits.Global, limits.User)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
