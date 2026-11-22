package clientapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLibraryStatusReportsLocalOperations(t *testing.T) {
	for _, pending := range []string{"none", "sync", "delete-confirmation", "confirmed-delete", "restore-confirmation", "restore", "checkout"} {
		t.Run(pending, func(t *testing.T) {
			dir := t.TempDir()
			path, err := canonicalExistingPath(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			binding, restore := testRestorePendingPublication(t)
			binding.Worktree = path
			db, err := initializeClientDB(t.Context(), dir, syncDirectory)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			const secret = "synthetic-status-token"
			if err := finalizeBinding(t.Context(), db, binding, []byte(secret), nil); err != nil {
				t.Fatal(err)
			}
			wantPending := pending
			command := "sync"
			var confirm []string
			switch pending {
			case "sync", "delete-confirmation", "confirmed-delete":
				p := testSyncPublicationState(t, binding)
				if pending != "sync" {
					p.RequiresDeleteConfirmation, p.DeletionCount, p.TrackedCount = true, 1, 2
					p.DeleteConfirmed = pending == "confirmed-delete"
					if p.DeleteConfirmed {
						wantPending = "sync"
					} else {
						confirm = []string{"--confirm-delete", p.CandidateCommit[:12]}
					}
				}
				err = saveSyncPublication(t.Context(), db, binding, &p)
			case "restore", "restore-confirmation":
				restore.RestoreConfirmed = pending == "restore"
				if !restore.RestoreConfirmed {
					command, confirm = "restore", []string{"--confirm", restore.CandidateCommit[:12]}
				}
				err = saveRestorePublication(t.Context(), db, binding, restore)
			case "checkout":
				err = savePendingCheckout(t.Context(), db, pendingCheckout{ServerURL: binding.ServerURL,
					LibraryID: binding.LibraryID, Worktree: path, UserID: binding.UserID, DeviceID: binding.DeviceID,
					TargetCommit: restore.CandidateCommit, HeadETag: binding.HeadETag})
			}
			if err != nil {
				t.Fatal(err)
			}
			// A scan would reject reserved paths. Status must report only the durable snapshot.
			if err := os.WriteFile(filepath.Join(path, syncRecoveryPrefix+"not-in-journal"), []byte("local edits"), 0o600); err != nil {
				t.Fatal(err)
			}
			if pending == "none" {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			databasePath := filepath.Join(dir, _clientDatabaseName)
			mainBefore, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			walBefore := statusTestWAL(t, databasePath)
			before := captureHistoryInspectWorktree(t, path)
			var out, stderr bytes.Buffer
			if err := run(t.Context(), []string{"library", "status", "--client-dir", dir, "--worktree", path}, nil, &out, &stderr); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String()+stderr.String(), secret) || strings.Contains(out.String(), "not-in-journal") {
				t.Fatal("status exposed credentials or a worktree entry")
			}
			var got libraryStatus
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Worktree != path || got.LibraryID != binding.LibraryID || got.SyncBase != binding.SyncBase || got.Pending != wantPending {
				t.Fatalf("unexpected status: %+v", got)
			}
			var wantNext []string
			if pending != "none" {
				canonicalDir, err := canonicalExistingPath(dir)
				if err != nil {
					t.Fatal(err)
				}
				wantNext = append([]string{"filecloud-client", "library", command, "--client-dir", canonicalDir, "--worktree", path}, confirm...)
			}
			if !slices.Equal(got.NextCommand, wantNext) {
				t.Fatalf("next command = %q, want %q", got.NextCommand, wantNext)
			}
			if pending == "delete-confirmation" && (got.Deletion == nil || got.Deletion.Deleted != 1 || got.Deletion.Tracked != 2) {
				t.Fatal("missing deletion statistics")
			}
			mainAfter, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(mainBefore, mainAfter) || !bytes.Equal(walBefore, statusTestWAL(t, databasePath)) {
				t.Fatal("status changed the client database or its WAL contents")
			}
			if !slices.Equal(before, captureHistoryInspectWorktree(t, path)) {
				t.Fatal("status modified the worktree")
			}
		})
	}
}

func statusTestWAL(t *testing.T, databasePath string) []byte {
	t.Helper()
	data, err := os.ReadFile(databasePath + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return data
}

func TestLibraryStatusDoesNotCreateState(t *testing.T) {
	path := t.TempDir()
	dir := filepath.Join(t.TempDir(), "no-client")
	var out bytes.Buffer
	err := run(t.Context(), []string{"library", "status", "--client-dir", dir, "--worktree", path}, nil, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not bound") || out.Len() != 0 {
		t.Fatalf("missing binding: err=%v, stdout=%q", err, out.String())
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing client directory: stat=%v", err)
	}

	dir = t.TempDir()
	db, err := initializeClientDB(t.Context(), dir, syncDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = run(t.Context(), []string{"library", "status", "--client-dir", dir, "--worktree", path}, nil, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not bound") || out.Len() != 0 {
		t.Fatalf("unbound worktree: err=%v, stdout=%q", err, out.String())
	}
}
