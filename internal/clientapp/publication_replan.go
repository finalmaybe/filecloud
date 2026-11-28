package clientapp

import (
	"context"
	"errors"
	"fmt"
)

// A challenge fixes the publication row and remote Head used by both merge planning and replacement CAS.
type _syncReplanChallenge struct {
	round        _syncPublicationRound
	binding      clientBinding
	observed     _syncPublicationState
	headID, etag string
	replacement  *_syncReplacementPlan
}

type _syncReplacementPlan struct {
	baseRoot, root, commit string
	data                   []byte
	history                [][]byte
	directories            []scannedObject
	deleted, tracked       int64
	protected              bool
}

type _replaceSyncPublicationRequest struct{ challenge *_syncReplanChallenge }

func (_replaceSyncPublicationRequest) publicationRequest() {}

func (r _syncPublicationRound) requestReplan(old _syncPublicationState, head remoteHead) _publicationOutcome {
	c := &_syncReplanChallenge{round: r, binding: r.binding, observed: old, headID: *head.CommitID, etag: head.ETag}
	r.challenge = c
	return _publicationOutcome{kind: _publicationOutcomeSyncReplan, replan: c}
}

func (c *_syncReplanChallenge) prepare(ctx context.Context) (_replaceSyncPublicationRequest, error) {
	r := c.round
	release, err := r.acquire(ctx)
	if err != nil {
		return _replaceSyncPublicationRequest{}, err
	}
	defer release()
	if r.challenge != c || r.binding != c.binding {
		return _replaceSyncPublicationRequest{}, _errPublicationRequestInvalid
	}
	if c.replacement == nil {
		verified, err := scanSyncSnapshot(ctx, r.options, r.binding.SyncBaseRoot)
		if err != nil || verified.root != c.observed.CapturedRoot {
			return _replaceSyncPublicationRequest{}, errors.New("worktree changed before pending merge replacement")
		}
		plan, err := prepareSyncReplacementMerge(ctx, r, c.observed, verified, c.headID)
		if err != nil {
			return _replaceSyncPublicationRequest{}, err
		}
		c.replacement = &plan
	}
	return _replaceSyncPublicationRequest{challenge: c}, nil
}

func (c *_syncReplanChallenge) replace(ctx context.Context) (_publicationOutcome, error) {
	r, old, plan := c.round, c.observed, c.replacement
	snapshot, err := scanSyncSnapshot(ctx, r.options, r.binding.SyncBaseRoot)
	if err != nil || snapshot.root != old.CapturedRoot {
		return _publicationOutcome{}, errors.New("worktree changed while preparing pending merge replacement")
	}
	head, err := getRemoteHead(ctx, r.base, r.binding.LibraryID, r.token)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if head.CommitID == nil || *head.CommitID != c.headID || head.ETag != c.etag {
		return _publicationOutcome{kind: _publicationOutcomeRerunSync}, errors.New("remote Head changed while preparing pending merge replacement; rerun sync")
	}
	next := _syncPublicationState{
		_publicationCandidate: _publicationCandidate{BaseCommit: old.ExpectedHead, BaseRoot: plan.baseRoot, ExpectedHead: c.headID,
			ExpectedETag: c.etag, CandidateCommit: plan.commit, CandidateRoot: plan.root, CandidateData: plan.data,
			CapturedCommit: old.CapturedCommit, CapturedRoot: old.CapturedRoot, CapturedData: old.CapturedData},
		_syncPublicationFields: _syncPublicationFields{DeletionCount: plan.deleted, TrackedCount: plan.tracked,
			RequiresDeleteConfirmation: plan.protected},
		CandidateHistory: plan.history,
	}
	if err := replaceSyncPublication(ctx, r.db, r.binding, old, &next); err != nil {
		return _publicationOutcome{}, err
	}
	if r.config.afterPendingReplacement != nil {
		if err := r.config.afterPendingReplacement(); err != nil {
			return _publicationOutcome{}, fmt.Errorf("after pending publication replacement: %w", err)
		}
	}
	if next.RequiresDeleteConfirmation {
		return _publicationOutcome{kind: _publicationOutcomeConfirmationRequired}, deletionConfirmationError(next)
	}
	r.options.confirmDelete, r.options.confirmDeleteSet = "", false
	return r.uploadPrepared(ctx, snapshot, next, plan.directories)
}
