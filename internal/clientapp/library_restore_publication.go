package clientapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
)

func dispatchRestorePublication(ctx context.Context, db *sql.DB, options bindOptions, binding clientBinding,
	snapshot worktreeSnapshot, head remoteHead, pending _restorePublicationState, stdout io.Writer,
	config libraryClientConfig, budget *_replayBudget, mode _publicationDispatchMode) error {
	if !restorePublicationMatchesBinding(pending, binding) {
		return errors.New("verify restore publication: restore publication does not match the binding")
	}
	if !pending.RestoreConfirmed {
		prefix := pending.CandidateCommit[:deleteCandidatePrefixLen]
		return fmt.Errorf("restore publication requires exact confirmation; rerun restore --confirm %s", prefix)
	}
	switch mode {
	case _publicationDispatchStart, _publicationDispatchResume:
		return resumeRestorePublication(ctx, db, options, binding, snapshot, head, pending, stdout, config, budget)
	default:
		return errors.New("restore publication dispatcher has an invalid mode")
	}
}

func resumeRestorePublication(ctx context.Context, db *sql.DB, options bindOptions, binding clientBinding,
	snapshot worktreeSnapshot, head remoteHead, pending _restorePublicationState, stdout io.Writer,
	config libraryClientConfig, budget *_replayBudget) error {
	if head.CommitID == nil {
		return discardRestorePublication(ctx, db, binding.Worktree, pending, "library Head is empty during restore")
	}
	if *head.CommitID == pending.CandidateCommit {
		if snapshot.root != pending.CapturedRoot {
			return discardRestorePublication(ctx, db, binding.Worktree, pending,
				"restore candidate is published but the worktree changed; rerun sync")
		}
		return transitionPublishedRestore(ctx, db, options, binding, head, pending, stdout, config)
	}
	if *head.CommitID == pending.ExpectedHead {
		if head.ETag != pending.ExpectedETag || snapshot.root != pending.CapturedRoot {
			return discardRestorePublication(ctx, db, binding.Worktree, pending,
				"restore candidate became stale; rerun restore preview")
		}
		return uploadAndPublishRestore(ctx, db, options, binding, snapshot, pending, stdout, config, budget)
	}
	ancestor, err := _remoteCommitDescendsFrom(ctx, options, *head.CommitID, pending.CandidateCommit, binding.UserID, budget)
	if err != nil {
		return err
	}
	if ancestor {
		if snapshot.root != pending.CapturedRoot {
			return discardRestorePublication(ctx, db, binding.Worktree, pending,
				"restore candidate is published but the worktree changed; rerun sync")
		}
		return transitionPublishedRestore(ctx, db, options, binding, head, pending, stdout, config)
	}
	return discardRestorePublication(ctx, db, binding.Worktree, pending,
		"restore candidate became stale; rerun restore preview")
}

func discardRestorePublication(ctx context.Context, db *sql.DB, worktree string, pending _restorePublicationState, message string) error {
	if err := _deletePendingPublication(ctx, db, worktree, pending, "restore publication changed before discard"); err != nil {
		return err
	}
	return errors.New(message)
}

func uploadAndPublishRestore(ctx context.Context, db *sql.DB, options bindOptions, binding clientBinding,
	snapshot worktreeSnapshot, pending _restorePublicationState, stdout io.Writer, config libraryClientConfig,
	budget *_replayBudget) error {
	source, err := fetchRestoreSourceWithOptions(ctx, options, binding, pending.SourceCommit)
	if err != nil {
		return err
	}
	plan, err := planRestoreSnapshotWithBudget(ctx, options, snapshot, source, pending.SourcePath, config.restorePlanBudget)
	if err != nil {
		return err
	}
	if plan.resultRoot != pending.CandidateRoot || plan.createdCount != pending.CreatedCount || plan.updatedCount != pending.UpdatedCount ||
		plan.typeReplacementCount != pending.TypeReplacementCount || plan.removedDescendantCount != pending.RemovedDescendantCount ||
		plan.preservedCurrentOnlyCount != pending.PreservedCurrentOnlyCount || plan.changedPathCount != pending.ChangedPathCount ||
		plan.previewTruncated != pending.PreviewTruncated || !slices.Equal(plan.changedPaths, pending.ChangedPathPreview) {
		return discardRestorePublication(ctx, db, binding.Worktree, pending, "restore candidate became stale; rerun restore preview")
	}
	references := make([]clientObjectReference, 0, len(plan.directories)+1)
	for _, directory := range plan.directories {
		references = append(references, clientObjectReference{ObjectID: directory.id, ObjectType: "Directory"})
	}
	references = append(references, clientObjectReference{ObjectID: pending.CandidateCommit, ObjectType: "Commit"})
	missing, err := checkRemoteObjects(ctx, options, references)
	if err != nil {
		return err
	}
	for _, directory := range plan.directories {
		if !missing["directories\x00"+directory.id] {
			continue
		}
		if err := putMetadata(ctx, options.base, options.libraryID, options.token, "directories", directory.id, directory.data); err != nil {
			return err
		}
	}
	if missing["commits\x00"+pending.CandidateCommit] {
		if err := putMetadata(ctx, options.base, options.libraryID, options.token, "commits", pending.CandidateCommit, pending.CandidateData); err != nil {
			return err
		}
	}
	return publishRestorePending(ctx, db, options, binding, snapshot, pending, stdout, config, budget)
}

func publishRestorePending(ctx context.Context, db *sql.DB, options bindOptions, binding clientBinding, snapshot worktreeSnapshot,
	pending _restorePublicationState, stdout io.Writer, config libraryClientConfig, budget *_replayBudget) error {
	current, err := headBeforePublication(ctx, options, config, pending._publicationCandidate)
	if err != nil {
		return err
	}
	if *current.CommitID != pending.ExpectedHead || current.ETag != pending.ExpectedETag {
		return dispatchRestorePublication(ctx, db, options, binding, snapshot, current, pending, stdout, config, budget, _publicationDispatchResume)
	}
	if err := _assertPendingPublication(ctx, db, binding.Worktree, pending); err != nil {
		return err
	}
	conflictingHead, conflict, publishErr := updateRemoteHeadOnce(ctx, options.base, options.libraryID, options.token,
		pending.ExpectedETag, pending.CandidateCommit)
	if conflict {
		observed, scanErr := scanWorktreeWithConfig(options.worktreeRoot, options.scanConfig)
		if scanErr != nil {
			return fmt.Errorf("rescan worktree after Head conflict: %w", scanErr)
		}
		return dispatchRestorePublication(ctx, db, options, binding, observed, conflictingHead, pending, stdout, config, budget,
			_publicationDispatchResume)
	}
	published, observed, err := headAfterPublication(ctx, options, publishErr)
	if err != nil {
		return err
	}
	if published.CommitID == nil {
		return errors.Join(publishErr, errors.New("library Head became empty during publication"))
	}
	switch *published.CommitID {
	case pending.CandidateCommit:
		return dispatchRestorePublication(ctx, db, options, binding, observed, published, pending, stdout, config, budget, _publicationDispatchResume)
	case pending.ExpectedHead:
		if published.ETag == pending.ExpectedETag {
			return errors.Join(publishErr, errors.New("library Head publication did not complete; rerun sync"))
		}
		return errors.Join(publishErr, dispatchRestorePublication(ctx, db, options, binding, observed, published, pending, stdout, config, budget,
			_publicationDispatchResume))
	default:
		if err := dispatchRestorePublication(ctx, db, options, binding, observed, published, pending, stdout, config, budget,
			_publicationDispatchResume); err != nil {
			return errors.Join(publishErr, err)
		}
		return nil
	}
}

func transitionPublishedRestore(ctx context.Context, db *sql.DB, options bindOptions, binding clientBinding,
	head remoteHead, pending _restorePublicationState, stdout io.Writer, config libraryClientConfig) error {
	if head.CommitID == nil {
		return errors.New("published restore Head is empty")
	}
	checkout := pendingCheckout{ServerURL: binding.ServerURL, LibraryID: binding.LibraryID, Worktree: binding.Worktree,
		UserID: binding.UserID, DeviceID: binding.DeviceID, TargetCommit: *head.CommitID, HeadETag: head.ETag,
		ApplyState: "pending", ConflictPromotions: _emptyConflictPromotions}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pending_checkouts(server_url, library_id, worktree, user_id, device_id,
		target_commit, target_root, head_etag, apply_state, conflict_promotions)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, 'pending', ?)`, checkout.ServerURL, checkout.LibraryID, checkout.Worktree,
		checkout.UserID, checkout.DeviceID, checkout.TargetCommit, checkout.HeadETag, checkout.ConflictPromotions); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := _deletePendingPublication(ctx, tx, binding.Worktree, pending,
		"restore publication changed during checkout transition"); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("transition restore publication to checkout: %w", err)
	}
	if config.afterSyncCheckoutTransition != nil {
		if err := config.afterSyncCheckoutTransition(); err != nil {
			return fmt.Errorf("after restore checkout transition: %w", err)
		}
	}
	return continueSyncCheckout(ctx, db, options, binding, checkout, stdout, config)
}
