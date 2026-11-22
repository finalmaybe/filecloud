package clientapp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopBindingsEnumerateRecoverableWork(t *testing.T) {
	dir := t.TempDir()
	bound, _ := testRestorePendingPublication(t)
	var out bytes.Buffer
	args := []string{"library", "bindings", "--client-dir", dir, "--server", bound.ServerURL, "--user-id", bound.UserID}
	query := func() []localBinding {
		t.Helper()
		out.Reset()
		if err := run(t.Context(), args, nil, &out, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		var rows []localBinding
		if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if got := query(); len(got) != 0 {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(dir, _clientDatabaseName)); !os.IsNotExist(err) {
		t.Fatal("read created a database")
	}
	db, err := initializeClientDB(t.Context(), dir, syncDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bound.Worktree = filepath.Join(dir, "no-longer-mounted")
	if err := finalizeBinding(t.Context(), db, bound, []byte("test-only"), nil); err != nil {
		t.Fatal(err)
	}
	c := pendingCheckout{ServerURL: bound.ServerURL, LibraryID: bound.LibraryID, Worktree: bound.Worktree, UserID: bound.UserID, DeviceID: bound.DeviceID, TargetCommit: bound.SyncBase, HeadETag: bound.HeadETag}
	if err := savePendingCheckout(t.Context(), db, c); err != nil {
		t.Fatal(err)
	}
	c.Worktree += "-new"
	c.LibraryID = "10000000-0000-4000-8000-000000000001"
	if err := savePendingCheckout(t.Context(), db, c); err != nil {
		t.Fatal(err)
	}
	rows := query()
	if len(rows) != 2 {
		t.Fatalf("got %d bindings", len(rows))
	}
	for _, r := range rows {
		if (r.Worktree == bound.Worktree) != (r.Phase == "bound") {
			t.Fatal("incorrect binding phase")
		}
	}
	args[len(args)-1] = "10000000-0000-4000-8000-000000000002"
	if len(query()) != 0 {
		t.Fatal("listed another owner")
	}
	args[len(args)-1] = bound.UserID
	args[5] = "https://different.invalid"
	if len(query()) != 0 {
		t.Fatal("listed another server")
	}
}
