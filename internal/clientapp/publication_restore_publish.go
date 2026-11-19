package clientapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

func (r _restorePublicationRound) resumeCurrent(ctx context.Context, pending _restorePublicationState) (_publicationOutcome, error) {
	snapshot, err := scanWorktreeWithConfig(r.root, r.options.scanConfig)
	if err != nil {
		return _publicationOutcome{}, fmt.Errorf("scan worktree: %w", err)
	}
	head, err := getRemoteHead(ctx, r.base, r.binding.LibraryID, r.token)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if head.CommitID == nil {
		return _publicationOutcome{}, errors.New("bound library Head is empty")
	}
	if !restorePublicationMatchesBinding(pending, r.binding) {
		return _publicationOutcome{}, errors.New("verify restore publication: restore publication does not match the binding")
	}
	if !pending.RestoreConfirmed {
		return restoreConfirmationRequired(pending)
	}
	if *head.CommitID != pending.ExpectedHead || head.ETag != pending.ExpectedETag {
		return r.reconcile(ctx, snapshot, head, pending)
	}
	if snapshot.root != pending.CapturedRoot {
		return r.discardChangedHead(ctx, pending)
	}
	source, plan, err := r.plan(ctx, snapshot, pending.SourceCommit, pending.SourcePath)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if !restorePlanMatchesPreview(pending, source, plan) {
		return r.discardChangedHead(ctx, pending)
	}
	return r.upload(ctx, snapshot, pending, plan.directories)
}

func (r _restorePublicationRound) upload(ctx context.Context, snapshot worktreeSnapshot, pending _restorePublicationState,
	directories []scannedObject) (_publicationOutcome, error) {
	references := make([]clientObjectReference, 0, len(directories)+1)
	for _, directory := range directories {
		references = append(references, clientObjectReference{ObjectID: directory.id, ObjectType: "Directory"})
	}
	references = append(references, clientObjectReference{ObjectID: pending.CandidateCommit, ObjectType: "Commit"})
	missing, err := checkRemoteObjects(ctx, r.options, references)
	if err != nil {
		return _publicationOutcome{}, err
	}
	for _, directory := range directories {
		if !missing["directories\x00"+directory.id] {
			continue
		}
		if err := putMetadata(ctx, r.base, r.binding.LibraryID, r.token, "directories", directory.id, directory.data); err != nil {
			return _publicationOutcome{}, err
		}
	}
	if missing["commits\x00"+pending.CandidateCommit] {
		if err := putMetadata(ctx, r.base, r.binding.LibraryID, r.token, "commits", pending.CandidateCommit, pending.CandidateData); err != nil {
			return _publicationOutcome{}, err
		}
	}
	return r.publish(ctx, snapshot, pending)
}

func (r _restorePublicationRound) publish(ctx context.Context, snapshot worktreeSnapshot, pending _restorePublicationState) (_publicationOutcome, error) {
	current, err := headBeforePublication(ctx, r.options, r.config, pending._publicationCandidate)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if *current.CommitID != pending.ExpectedHead || current.ETag != pending.ExpectedETag {
		return r.reconcile(ctx, snapshot, current, pending)
	}
	if err := _assertPendingPublication(ctx, r.db, r.binding.Worktree, pending); err != nil {
		return _publicationOutcome{}, err
	}
	conflictingHead, conflict, publishErr := updateRemoteHeadOnce(ctx, r.base, r.binding.LibraryID, r.token,
		pending.ExpectedETag, pending.CandidateCommit)
	if conflict {
		observed, scanErr := scanWorktreeWithConfig(r.root, r.options.scanConfig)
		if scanErr != nil {
			return _publicationOutcome{}, fmt.Errorf("rescan worktree after Head conflict: %w", scanErr)
		}
		return r.reconcile(ctx, observed, conflictingHead, pending)
	}
	published, observed, err := headAfterPublication(ctx, r.options, publishErr)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if published.CommitID == nil {
		return _publicationOutcome{}, errors.Join(publishErr, errors.New("library Head became empty during publication"))
	}
	if *published.CommitID == pending.CandidateCommit {
		return r.settlePublished(ctx, observed, published, pending)
	}
	if *published.CommitID == pending.ExpectedHead && published.ETag == pending.ExpectedETag {
		return _publicationOutcome{kind: _publicationOutcomeRerunSync}, errors.Join(publishErr, errors.New("library Head publication did not complete; rerun sync"))
	}
	outcome, err := r.reconcile(ctx, observed, published, pending)
	if err != nil {
		return outcome, errors.Join(publishErr, err)
	}
	return outcome, nil
}

func (r _restorePublicationRound) reconcile(ctx context.Context, snapshot worktreeSnapshot, head remoteHead,
	pending _restorePublicationState) (_publicationOutcome, error) {
	if head.CommitID == nil {
		return r.discard(ctx, pending, _publicationOutcomeRerunRestore, "library Head is empty during restore")
	}
	if *head.CommitID != pending.ExpectedHead {
		published, err := r.isPublished(ctx, *head.CommitID, pending)
		if err != nil {
			return _publicationOutcome{}, err
		}
		if published {
			return r.settlePublished(ctx, snapshot, head, pending)
		}
	}
	return r.discardChangedHead(ctx, pending)
}

func (r _restorePublicationRound) isPublished(ctx context.Context, head string, pending _restorePublicationState) (bool, error) {
	if head == pending.CandidateCommit {
		return true, nil
	}
	return _remoteCommitDescendsFrom(ctx, r.options, head, pending.CandidateCommit, r.binding.UserID, r.budget)
}

func (r _restorePublicationRound) settlePublished(ctx context.Context, snapshot worktreeSnapshot, head remoteHead,
	pending _restorePublicationState) (_publicationOutcome, error) {
	if snapshot.root != pending.CapturedRoot {
		return r.discard(ctx, pending, _publicationOutcomeRerunSync,
			"restore candidate is published but the worktree changed; rerun sync")
	}
	binding := r.binding
	checkout := pendingCheckout{ServerURL: binding.ServerURL, LibraryID: binding.LibraryID, Worktree: binding.Worktree,
		UserID: binding.UserID, DeviceID: binding.DeviceID, TargetCommit: *head.CommitID, HeadETag: head.ETag,
		ApplyState: "pending", ConflictPromotions: slices.Clone(_emptyConflictPromotions)}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pending_checkouts(server_url, library_id, worktree, user_id, device_id,
		target_commit, target_root, head_etag, apply_state, conflict_promotions)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, 'pending', ?)`, checkout.ServerURL, checkout.LibraryID, checkout.Worktree,
		checkout.UserID, checkout.DeviceID, checkout.TargetCommit, checkout.HeadETag, checkout.ConflictPromotions); err != nil {
		return _publicationOutcome{}, errors.Join(err, tx.Rollback())
	}
	if err := _deletePendingPublication(ctx, tx, binding.Worktree, pending,
		"restore publication changed during checkout transition"); err != nil {
		return _publicationOutcome{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return _publicationOutcome{}, fmt.Errorf("transition restore publication to checkout: %w", err)
	}
	if r.config.afterSyncCheckoutTransition != nil {
		if err := r.config.afterSyncCheckoutTransition(); err != nil {
			return _publicationOutcome{}, fmt.Errorf("after restore checkout transition: %w", err)
		}
	}
	return _publicationOutcome{kind: _publicationOutcomeCheckoutPending, checkout: &checkout}, nil
}

func (r _restorePublicationRound) discardChangedHead(ctx context.Context, pending _restorePublicationState) (_publicationOutcome, error) {
	return r.discard(ctx, pending, _publicationOutcomeRerunRestore, "restore candidate became stale; rerun restore preview")
}

func (r _restorePublicationRound) discard(ctx context.Context, pending _restorePublicationState,
	kind _publicationOutcomeKind, message string) (_publicationOutcome, error) {
	if err := _deletePendingPublication(ctx, r.db, r.binding.Worktree, pending, "restore publication changed before discard"); err != nil {
		return _publicationOutcome{}, err
	}
	return _publicationOutcome{kind: kind}, errors.New(message)
}

func restoreConfirmationRequired(pending _restorePublicationState) (_publicationOutcome, error) {
	prefix := pending.CandidateCommit[:deleteCandidatePrefixLen]
	return _publicationOutcome{kind: _publicationOutcomeConfirmationRequired}, fmt.Errorf("restore publication requires exact confirmation; rerun restore --confirm %s", prefix)
}
