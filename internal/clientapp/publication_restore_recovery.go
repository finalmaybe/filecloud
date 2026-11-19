package clientapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type _restoreHeadRelation uint8

const (
	_restoreHeadEmpty _restoreHeadRelation = iota + 1
	_restoreHeadExpected
	_restoreHeadCandidate
	_restoreHeadSuccessor
	_restoreHeadUnrelated
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
	relation, err := r.classifyHead(ctx, head, pending)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if relation != _restoreHeadExpected {
		return r.settleObservedHead(ctx, snapshot, head, pending, relation)
	}
	if snapshot.root != pending.CapturedRoot {
		return r.invalidate(ctx, pending)
	}
	source, plan, err := r.plan(ctx, snapshot, pending.SourceCommit, pending.SourcePath)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if !restorePlanMatchesPreview(pending, source, plan) {
		return r.invalidate(ctx, pending)
	}
	return r.upload(ctx, snapshot, pending, plan.directories)
}

func (r _restorePublicationRound) classifyHead(ctx context.Context, head remoteHead, pending _restorePublicationState) (_restoreHeadRelation, error) {
	if head.CommitID == nil {
		return _restoreHeadEmpty, nil
	}
	switch *head.CommitID {
	case pending.ExpectedHead:
		if head.ETag == pending.ExpectedETag {
			return _restoreHeadExpected, nil
		}
		return _restoreHeadUnrelated, nil
	case pending.CandidateCommit:
		return _restoreHeadCandidate, nil
	}
	published, err := _remoteCommitDescendsFrom(ctx, r.options, *head.CommitID, pending.CandidateCommit, r.binding.UserID, r.budget)
	if err != nil {
		return 0, err
	}
	if published {
		return _restoreHeadSuccessor, nil
	}
	return _restoreHeadUnrelated, nil
}

func (r _restorePublicationRound) resolvePublicationResult(ctx context.Context, pending _restorePublicationState,
	head remoteHead, conflict bool, publishErr error) (_publicationOutcome, error) {
	var observed worktreeSnapshot
	var err error
	if conflict && head.ETag != "" {
		observed, err = scanWorktreeWithConfig(r.root, r.options.scanConfig)
		if err != nil {
			return _publicationOutcome{}, fmt.Errorf("rescan worktree after Head conflict: %w", err)
		}
	} else {
		head, observed, err = headAfterPublication(ctx, r.options, publishErr)
		if err != nil {
			return _publicationOutcome{}, err
		}
	}
	relation, err := r.classifyHead(ctx, head, pending)
	if err != nil {
		return _publicationOutcome{}, errors.Join(publishErr, err)
	}
	if !conflict {
		switch relation {
		case _restoreHeadEmpty:
			return _publicationOutcome{}, errors.Join(publishErr, errors.New("library Head became empty during publication"))
		case _restoreHeadExpected:
			return _publicationOutcome{kind: _publicationOutcomeRerunSync}, errors.Join(publishErr, errors.New("library Head publication did not complete; rerun sync"))
		}
	}
	outcome, err := r.settleObservedHead(ctx, observed, head, pending, relation)
	if err != nil && publishErr != nil && relation != _restoreHeadCandidate {
		return outcome, errors.Join(publishErr, err)
	}
	return outcome, err
}

func (r _restorePublicationRound) settleObservedHead(ctx context.Context, snapshot worktreeSnapshot, head remoteHead,
	pending _restorePublicationState, relation _restoreHeadRelation) (_publicationOutcome, error) {
	switch relation {
	case _restoreHeadCandidate, _restoreHeadSuccessor:
		return r.settlePublished(ctx, snapshot, head, pending)
	case _restoreHeadEmpty:
		return r.discard(ctx, pending, _publicationOutcomeRerunRestore, "library Head is empty during restore")
	default:
		return r.invalidate(ctx, pending)
	}
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

func (r _restorePublicationRound) invalidate(ctx context.Context, pending _restorePublicationState) (_publicationOutcome, error) {
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
