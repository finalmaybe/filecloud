package clientapp

import (
	"context"
	"errors"
	"fmt"
)

func (r _syncPublicationRound) settlePublished(ctx context.Context, snapshot worktreeSnapshot, head remoteHead, pending _syncPublicationState) (_publicationOutcome, error) {
	if snapshot.root != pending.CapturedRoot {
		if err := r.recoverCapturedBaseline(ctx, head.ETag, pending); err != nil {
			return _publicationOutcome{}, err
		}
		r.recordBaseline(pending.CapturedCommit, pending.CapturedRoot, head.ETag)
		return _publicationOutcome{kind: _publicationOutcomeRerunSync}, errors.New("published candidate recovered as Sync Base while preserving local changes; rerun sync")
	}
	if *head.CommitID == pending.CandidateCommit && pending.CandidateRoot == pending.CapturedRoot && !snapshot.needsCheckout {
		if err := r.finalize(ctx, snapshot, head.ETag, pending); err != nil {
			return _publicationOutcome{}, err
		}
		r.recordBaseline(pending.CandidateCommit, pending.CandidateRoot, head.ETag)
		return _publicationOutcome{kind: _publicationOutcomeFinalized}, nil
	}
	checkout := pendingCheckout{ServerURL: r.binding.ServerURL, LibraryID: r.binding.LibraryID, Worktree: r.binding.Worktree,
		UserID: r.binding.UserID, DeviceID: r.binding.DeviceID, TargetCommit: *head.CommitID, HeadETag: head.ETag, ApplyState: "pending"}
	if err := r.handoffToCheckout(ctx, snapshot, pending, &checkout); err != nil {
		return _publicationOutcome{}, err
	}
	r.recordBaseline(pending.CapturedCommit, pending.CapturedRoot, head.ETag)
	if r.config.afterSyncCheckoutTransition != nil {
		if err := r.config.afterSyncCheckoutTransition(); err != nil {
			return _publicationOutcome{}, fmt.Errorf("after sync checkout transition: %w", err)
		}
	}
	return _publicationOutcome{kind: _publicationOutcomeCheckoutPending, checkout: &checkout}, nil
}

func (s *_lockedBoundWorktreeSession) recordBaseline(commit, root, etag string) {
	s.binding.SyncBase, s.binding.SyncBaseRoot, s.binding.HeadETag = commit, root, etag
}

func (r _syncPublicationRound) recoverCapturedBaseline(ctx context.Context, etag string, pending _syncPublicationState) error {
	options, binding := r.options, r.binding
	cacheRoot, err := openVerifiedCacheRoot(options.clientDir)
	if err != nil {
		return err
	}
	defer cacheRoot.Close()
	options.cacheRoot = cacheRoot
	commit, err := downloadTargetCommit(ctx, options, pending.CapturedCommit, binding.UserID)
	if err != nil {
		return fmt.Errorf("recover captured publication metadata: %w", err)
	}
	if commit.Root != pending.CapturedRoot {
		return errors.New("captured publication commit has a different root")
	}
	paths, err := deriveRemotePaths(ctx, options, commit.Root, false)
	if err != nil {
		return fmt.Errorf("recover published candidate paths: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE bindings SET sync_base_commit = ?, sync_base_root = ?, head_etag = ?
		WHERE server_url = ? AND library_id = ? AND worktree = ? AND user_id = ? AND device_id = ?
		AND sync_base_commit = ? AND sync_base_root = ? AND head_etag = ?`, pending.CapturedCommit,
		pending.CapturedRoot, etag, binding.ServerURL, binding.LibraryID, binding.Worktree, binding.UserID,
		binding.DeviceID, binding.SyncBase, binding.SyncBaseRoot, binding.HeadETag)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.Join(errors.New("published candidate recovery did not advance the expected binding"), err, tx.Rollback())
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM path_index WHERE worktree = ?", binding.Worktree); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	priorOmissions := make(map[string]checkoutPath, len(options.scanConfig.ignoreIndex))
	for path, prior := range options.scanConfig.ignoreIndex {
		if prior.unmaterialized || ignoreSyncPath(options.scanConfig.ignore, path) {
			prior.unmaterialized = true
			priorOmissions[path] = prior
		}
	}
	for i, path := range paths {
		if prior, ok := priorOmissions[path.path]; ok {
			paths[i] = prior
			delete(priorOmissions, path.path)
		} else if ignoreSyncPath(options.scanConfig.ignore, path.path) {
			paths[i].unmaterialized = true
			paths[i].mtime = ""
		}
	}
	// The captured tree cannot tell us whether an omitted local copy of a
	// remotely deleted entry still exists. Keep its last observed local object.
	for _, prior := range priorOmissions {
		paths = append(paths, prior)
	}
	if err := insertPathIndex(ctx, tx, binding.Worktree, paths); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := _deletePendingPublication(ctx, tx, binding.Worktree, pending,
		"published candidate recovery did not clear the expected pending publication"); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("recover published candidate: %w", err)
	}
	return nil
}

func (r _syncPublicationRound) handoffToCheckout(ctx context.Context, snapshot worktreeSnapshot, pending _syncPublicationState, checkout *pendingCheckout) error {
	options, binding := r.options, r.binding
	var promotions []_conflictPromotion
	if pending.CandidateCommit != pending.CapturedCommit {
		rebuilt, err := _rebuildPendingMerge(ctx, options, binding, snapshot, pending, r.budget)
		if err != nil {
			return fmt.Errorf("rebuild conflict provenance before checkout transition: %w", err)
		}
		promotions = rebuilt.promotions
		if err := _validatePromotionTargets(promotions, rebuilt.paths); err != nil {
			return err
		}
	}
	encodedPromotions, err := _encodeConflictPromotions(promotions)
	if err != nil {
		return err
	}
	if len(promotions) != 0 {
		cacheRoot, err := openVerifiedCacheRoot(options.clientDir)
		if err != nil {
			return err
		}
		options.cacheRoot = cacheRoot
		capturedBinding := binding
		capturedBinding.SyncBase, capturedBinding.SyncBaseRoot = pending.CapturedCommit, pending.CapturedRoot
		authorityErr := _authoritativePromotionReplay(ctx, options, capturedBinding, checkout.TargetCommit, promotions, r.budget)
		closeErr := cacheRoot.Close()
		if authorityErr != nil || closeErr != nil {
			return errors.Join(authorityErr, closeErr)
		}
	}
	checkout.ConflictPromotions = encodedPromotions
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := updateBindingAndIndex(ctx, tx, binding, pending.CapturedCommit, pending.CapturedRoot, checkout.HeadETag, snapshot.indexPaths()); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := handoffPendingPublication(ctx, tx, *checkout, pending,
		"publication changed during checkout transition"); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("transition superseded publication: %w", err)
	}
	return nil
}
