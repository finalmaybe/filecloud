package storage

import (
	"path/filepath"
	"testing"
)

func TestHistoryConcurrencyMigrationAndPersistence(t *testing.T) {
	ctx := t.Context()
	databasePath := filepath.Join(t.TempDir(), _databaseName)
	db, err := openDB(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db)
	store := &Store{db: db}
	migrations := metadataMigrations()
	if err := migrate(ctx, db, migrations[:10]); err != nil {
		t.Fatalf("prepare version 10 schema: %v", err)
	}
	if err := store.SetWebDAVEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations); err != nil {
		t.Fatalf("migrate history concurrency: %v", err)
	}
	if got, err := store.HistoryConcurrency(ctx); err != nil || got != (HistoryConcurrency{Global: 50, User: 20}) {
		t.Fatalf("upgraded history concurrency = %+v, %v; want 50/20", got, err)
	}
	if enabled, err := store.WebDAVEnabled(ctx); err != nil || !enabled {
		t.Fatalf("upgrade changed WebDAV setting: %v, %v", enabled, err)
	}
	for _, column := range []string{"history_global_capacity", "history_user_capacity"} {
		for _, invalid := range []any{0, MaxSavedHistoryConcurrency + 1, 1.5} {
			if _, err := db.ExecContext(ctx, `UPDATE server_settings SET `+column+`=? WHERE id=1`, invalid); err == nil {
				t.Errorf("migration allowed %s = %v", column, invalid)
			}
		}
	}
	want := HistoryConcurrency{Global: 1, User: MaxSavedHistoryConcurrency}
	if err := store.SetHistoryConcurrency(ctx, want); err != nil {
		t.Fatalf("save independent lower and upper bounds: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openDB(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, reopened)
	if err := migrate(ctx, reopened, migrations); err != nil {
		t.Fatalf("reopen current schema: %v", err)
	}
	if got, err := (&Store{db: reopened}).HistoryConcurrency(ctx); err != nil || got != want {
		t.Fatalf("reopened history concurrency = %+v, %v; want %+v", got, err, want)
	}
}
