package clientapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/mingming-cn/filecloud/internal/object"
)

var _errWrongRestorePrefix = errors.New("restore --confirm must exactly match this worktree's pending restore candidate")

type _previewRestorePublicationRequest struct {
	sourceCommit, sourcePath string
	warning                  io.Writer
}

func (_previewRestorePublicationRequest) publicationRequest() {}

type _confirmRestorePublicationRequest struct {
	prefix  string
	warning io.Writer
}

func (_confirmRestorePublicationRequest) publicationRequest() {}

type _resumeRestorePublicationRequest struct {
	scanConfig worktreeScanConfig
}

func (_resumeRestorePublicationRequest) publicationRequest() {}

type restorePreview struct {
	sourceCommit, sourcePath                     string
	expectedHead, candidateCommit                string
	createdCount, updatedCount                   int64
	typeReplacementCount, removedDescendantCount int64
	preservedCurrentOnlyCount, changedPathCount  int64
	changedPaths                                 []string
	truncated                                    bool
}

func (p restorePreview) confirmationPrefix() string {
	return p.candidateCommit[:deleteCandidatePrefixLen]
}

type _restorePublicationRound struct {
	*_lockedBoundWorktreeSession
	options bindOptions
	budget  *_replayBudget
}

func newRestorePublicationRound(s *_lockedBoundWorktreeSession) _restorePublicationRound {
	return _restorePublicationRound{_lockedBoundWorktreeSession: s, options: s.bindOptions(), budget: _newReplayBudget()}
}

func (r _restorePublicationRound) preview(ctx context.Context, pending _pendingPublicationState,
	request _previewRestorePublicationRequest) (_publicationOutcome, error) {
	switch value := pending.(type) {
	case _syncPublicationState:
		return _publicationOutcome{}, errors.New("restore cannot start while a sync publication is pending; run sync first")
	case _restorePublicationState:
		if !restorePublicationMatchesBinding(value, r.binding) {
			return _publicationOutcome{}, errors.New("verify pending restore preview: restore publication does not match the binding")
		}
		if value.RestoreConfirmed || value.SourceCommit != request.sourceCommit || value.SourcePath != request.sourcePath {
			return _publicationOutcome{kind: _publicationOutcomeConfirmationRequired}, fmt.Errorf("restore candidate is already pending; rerun restore --confirm %s", value.CandidateCommit[:deleteCandidatePrefixLen])
		}
		return previewReady(value), nil
	}

	if err := r.prepareScan(ctx, request.warning); err != nil {
		return _publicationOutcome{}, err
	}
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
	if snapshot.root != r.binding.SyncBaseRoot || *head.CommitID != r.binding.SyncBase {
		return _publicationOutcome{}, errors.New("worktree is not converged with Sync Base and Head; run sync first")
	}
	source, plan, err := r.plan(ctx, snapshot, request.sourceCommit, request.sourcePath)
	if errors.Is(err, _errRestoreSourcePathNotFound) {
		return _publicationOutcome{}, _errRestoreSourcePathNotFound
	}
	if err != nil {
		return _publicationOutcome{}, err
	}
	if plan.resultRoot == snapshot.root {
		return _publicationOutcome{kind: _publicationOutcomeRestoreNoOp,
			restorePreview: &restorePreview{sourceCommit: request.sourceCommit, sourcePath: request.sourcePath}}, nil
	}
	return r.persistPreview(ctx, snapshot, head, source, request.sourcePath, plan)
}

func (r _restorePublicationRound) confirm(ctx context.Context, current _pendingPublicationState,
	request _confirmRestorePublicationRequest) (_publicationOutcome, error) {
	if current == nil {
		return _publicationOutcome{}, errors.New("restore confirmation requires a pending restore candidate")
	}
	pending, ok := current.(_restorePublicationState)
	if !ok {
		return _publicationOutcome{}, errors.New("restore cannot continue while a sync publication is pending; run sync first")
	}
	if request.prefix != pending.CandidateCommit[:deleteCandidatePrefixLen] {
		return _publicationOutcome{kind: _publicationOutcomeConfirmationRequired}, _errWrongRestorePrefix
	}
	if !restorePublicationMatchesBinding(pending, r.binding) {
		return r.discardStalePreview(ctx, pending)
	}
	head, err := getRemoteHead(ctx, r.base, r.binding.LibraryID, r.token)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if err := r.prepareScan(ctx, request.warning); err != nil {
		return _publicationOutcome{}, err
	}
	snapshot, err := scanWorktreeWithConfig(r.root, r.options.scanConfig)
	if err != nil {
		return _publicationOutcome{}, fmt.Errorf("scan worktree: %w", err)
	}
	if head.CommitID == nil {
		return r.discardStalePreview(ctx, pending)
	}
	if *head.CommitID != pending.ExpectedHead {
		published, err := r.isPublished(ctx, *head.CommitID, pending)
		if err != nil {
			return r.revalidationFailed(ctx, pending, err)
		}
		if !published {
			return r.discardStalePreview(ctx, pending)
		}
		if !pending.RestoreConfirmed {
			return restoreConfirmationRequired(pending)
		}
		return r.settlePublished(ctx, snapshot, head, pending)
	}
	if head.ETag != pending.ExpectedETag || snapshot.root != pending.CapturedRoot {
		return r.discardStalePreview(ctx, pending)
	}
	source, plan, err := r.plan(ctx, snapshot, pending.SourceCommit, pending.SourcePath)
	if err != nil {
		return r.revalidationFailed(ctx, pending, err)
	}
	capturedData, err := r.fetchCapturedCommit(ctx, pending.ExpectedHead, pending.CapturedRoot)
	if err != nil {
		return r.revalidationFailed(ctx, pending, err)
	}
	if !bytes.Equal(capturedData, pending.CapturedData) || !restorePlanMatchesPreview(pending, source, plan) {
		return r.discardStalePreview(ctx, pending)
	}
	if !pending.RestoreConfirmed {
		if err := confirmRestorePublication(ctx, r.db, r.binding.Worktree, &pending); err != nil {
			return _publicationOutcome{}, err
		}
	}
	return r.upload(ctx, snapshot, pending, plan.directories)
}

func (r *_restorePublicationRound) prepareScan(ctx context.Context, warning io.Writer) error {
	if err := validatePendingPromotionTargets(ctx, r.db, r.binding.Worktree); err != nil {
		return fmt.Errorf("validate checkout promotion targets: %w", err)
	}
	// Checkout recovery belongs to Sync. With no pending checkout, bind only the journal's Worktree identity.
	if err := bindFSJournalRoot(ctx, r.db, r.binding.Worktree, r.root); err != nil {
		return fmt.Errorf("recover checkout filesystem actions: %w", err)
	}
	var err error
	r.options.scanConfig, err = r.loadScanConfig(ctx, warning)
	return err
}

func (r _restorePublicationRound) plan(ctx context.Context, snapshot worktreeSnapshot, sourceID, sourcePath string) (historyInspectCommit, restorePlan, error) {
	source, err := fetchRestoreSource(ctx, r.options, r.binding, sourceID)
	if err != nil {
		return historyInspectCommit{}, restorePlan{}, err
	}
	plan, err := planRestoreSnapshot(ctx, r.options, snapshot, source, sourcePath, r.config.restorePlanBudget)
	return source, plan, err
}

func (r _restorePublicationRound) fetchCapturedCommit(ctx context.Context, commitID, rootID string) ([]byte, error) {
	data, err := fetchRestoreObject(ctx, r.options, "commits", commitID)
	if err != nil {
		return nil, err
	}
	commit, err := object.VerifyCommit(data, commitID)
	if err != nil || commit.AuthorUserID != r.binding.UserID || commit.Root != rootID {
		return nil, errors.New("restore Head commit is not canonical for the binding")
	}
	return data, nil
}

func (r _restorePublicationRound) persistPreview(ctx context.Context, snapshot worktreeSnapshot, head remoteHead,
	source historyInspectCommit, sourcePath string, plan restorePlan) (_publicationOutcome, error) {
	capturedData, err := r.fetchCapturedCommit(ctx, *head.CommitID, snapshot.root)
	if err != nil {
		return _publicationOutcome{}, err
	}
	data, id, err := canonicalRestoreCommit(r.binding.UserID, r.binding.DeviceID, plan.resultRoot,
		source.CommitID, sourcePath, []string{*head.CommitID}, r.config.now())
	if err != nil {
		return _publicationOutcome{}, err
	}
	candidate := _restorePublicationState{
		_publicationCandidate: _publicationCandidate{BaseCommit: r.binding.SyncBase, BaseRoot: r.binding.SyncBaseRoot,
			ExpectedHead: *head.CommitID, ExpectedETag: head.ETag, CandidateCommit: id, CandidateRoot: plan.resultRoot,
			CandidateData: data, CapturedCommit: *head.CommitID, CapturedRoot: snapshot.root, CapturedData: capturedData},
		_restorePublicationFields: restorePreviewFields(source, sourcePath, plan),
		ChangedPathPreview:        plan.changedPaths,
	}
	if !restorePublicationMatchesBinding(candidate, r.binding) {
		return _publicationOutcome{}, errors.New("verify restore candidate: restore publication does not match the binding")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if err := saveRestorePublication(ctx, tx, r.binding, candidate); err != nil {
		return _publicationOutcome{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return _publicationOutcome{}, fmt.Errorf("save restore candidate: %w", err)
	}
	return previewReady(candidate), nil
}

func restorePreviewFields(source historyInspectCommit, sourcePath string, plan restorePlan) _restorePublicationFields {
	return _restorePublicationFields{SourceCommit: source.CommitID, SourcePath: sourcePath, SourceRoot: source.Root,
		CreatedCount: plan.createdCount, UpdatedCount: plan.updatedCount, TypeReplacementCount: plan.typeReplacementCount,
		RemovedDescendantCount: plan.removedDescendantCount, PreservedCurrentOnlyCount: plan.preservedCurrentOnlyCount,
		ChangedPathCount: plan.changedPathCount, PreviewTruncated: plan.previewTruncated}
}

func restorePlanMatchesPreview(pending _restorePublicationState, source historyInspectCommit, plan restorePlan) bool {
	fields := restorePreviewFields(source, pending.SourcePath, plan)
	fields.RestoreConfirmed = pending.RestoreConfirmed
	// Loading verified the candidate's exact canonical bytes, including its parent and restore message.
	// The root is the candidate field derived from the newly computed overlay.
	return plan.resultRoot == pending.CandidateRoot && fields == pending._restorePublicationFields &&
		slices.Equal(plan.changedPaths, pending.ChangedPathPreview)
}

func previewReady(pending _restorePublicationState) _publicationOutcome {
	preview := restorePreview{sourceCommit: pending.SourceCommit, sourcePath: pending.SourcePath,
		expectedHead: pending.ExpectedHead, candidateCommit: pending.CandidateCommit, createdCount: pending.CreatedCount,
		updatedCount: pending.UpdatedCount, typeReplacementCount: pending.TypeReplacementCount,
		removedDescendantCount: pending.RemovedDescendantCount, preservedCurrentOnlyCount: pending.PreservedCurrentOnlyCount,
		changedPathCount: pending.ChangedPathCount, changedPaths: slices.Clone(pending.ChangedPathPreview), truncated: pending.PreviewTruncated}
	return _publicationOutcome{kind: _publicationOutcomePreviewReady, restorePreview: &preview}
}

func (r _restorePublicationRound) discardStalePreview(ctx context.Context, pending _restorePublicationState) (_publicationOutcome, error) {
	if err := discardPendingPublication(ctx, r.db, r.binding.Worktree, pending); err != nil {
		return _publicationOutcome{}, err
	}
	return _publicationOutcome{kind: _publicationOutcomeRerunRestore}, errors.New("stale restore candidate discarded; rerun restore")
}

func (r _restorePublicationRound) revalidationFailed(ctx context.Context, pending _restorePublicationState, err error) (_publicationOutcome, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return _publicationOutcome{}, err
	}
	if _, transient := errors.AsType[*transientRemoteError](err); transient {
		return _publicationOutcome{}, err
	}
	if statusErr, remote := errors.AsType[*remoteStatusError](err); remote &&
		statusErr.status != http.StatusNotFound && statusErr.status != http.StatusGone {
		return _publicationOutcome{}, err
	}
	return r.discardStalePreview(ctx, pending)
}
