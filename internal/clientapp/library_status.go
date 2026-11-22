package clientapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type statusDeletion struct{ Deleted, Tracked int64 }

type statusConfirmation struct{ Kind, CandidatePrefix string }

type libraryStatus struct {
	Confirmation *statusConfirmation `json:",omitempty"`
	LibraryID    string              `json:"LibraryId"`
	Worktree     string
	SyncBase     string
	Pending      string
	Deletion     *statusDeletion `json:",omitempty"`
	NextCommand  []string        `json:",omitempty"`
}

func runLibraryStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("library status", stderr)
	clientDir := flags.String("client-dir", "", "Filecloud client state directory")
	worktree := flags.String("worktree", "", "Bound worktree directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *clientDir == "" || *worktree == "" || flags.NArg() != 0 {
		return errors.New("usage: filecloud-client library status --client-dir path --worktree path")
	}
	dir, err := canonicalStateDir(*clientDir)
	if err != nil {
		return err
	}
	path, err := canonicalExistingPath(*worktree)
	if err != nil {
		return err
	}
	status, err := readLibraryStatus(ctx, dir, path)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(status)
}

func readLibraryStatus(ctx context.Context, dir, path string) (_ libraryStatus, retErr error) {
	databasePath := filepath.Join(dir, _clientDatabaseName)
	if _, err := os.Stat(databasePath); errors.Is(err, os.ErrNotExist) {
		return libraryStatus{}, errors.New("worktree is not bound: client database does not exist")
	} else if err != nil {
		return libraryStatus{}, err
	}
	db, err := openClientDB(databasePath, true)
	if err != nil {
		return libraryStatus{}, err
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return libraryStatus{}, err
	}
	defer tx.Rollback()
	binding := clientBinding{Worktree: path}
	var checkout bool
	err = tx.QueryRowContext(ctx, `SELECT library_id, user_id, device_id, sync_base_commit, sync_base_root, head_etag,
		EXISTS(SELECT 1 FROM pending_checkouts WHERE worktree = bindings.worktree)
		FROM bindings WHERE worktree = ?`, path).Scan(&binding.LibraryID, &binding.UserID, &binding.DeviceID,
		&binding.SyncBase, &binding.SyncBaseRoot, &binding.HeadETag, &checkout)
	if errors.Is(err, sql.ErrNoRows) {
		return libraryStatus{}, errors.New("worktree is not bound")
	}
	if err != nil {
		return libraryStatus{}, fmt.Errorf("read client binding: %w", err)
	}
	status := libraryStatus{LibraryID: binding.LibraryID, Worktree: path, SyncBase: binding.SyncBase}
	next := []string{"filecloud-client", "library", "sync", "--client-dir", dir, "--worktree", path}
	if checkout {
		status.Pending, status.NextCommand = "checkout", next
		return status, nil
	}
	pending, err := loadPendingPublication(ctx, tx, binding)
	if err != nil {
		return libraryStatus{}, err
	}
	switch p := pending.(type) {
	case nil:
		status.Pending = "none"
	case _syncPublicationState:
		status.Pending, status.NextCommand = "sync", next
		if p.RequiresDeleteConfirmation && !p.DeleteConfirmed {
			status.Pending = "delete-confirmation"
			status.Confirmation = &statusConfirmation{"delete", p.CandidateCommit[:deleteCandidatePrefixLen]}
			status.Deletion = &statusDeletion{Deleted: p.DeletionCount, Tracked: p.TrackedCount}
			status.NextCommand = append(next, "--confirm-delete", p.CandidateCommit[:deleteCandidatePrefixLen])
		}
	case _restorePublicationState:
		status.Pending, status.NextCommand = "restore", next
		if !p.RestoreConfirmed {
			status.Pending = "restore-confirmation"
			status.Confirmation = &statusConfirmation{"restore", p.CandidateCommit[:deleteCandidatePrefixLen]}
			next[2] = "restore"
			status.NextCommand = append(next, "--confirm", p.CandidateCommit[:deleteCandidatePrefixLen])
		}
	default:
		return libraryStatus{}, errors.New("unsupported pending publication")
	}
	return status, nil
}
