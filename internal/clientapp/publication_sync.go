package clientapp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"

	"github.com/mingming-cn/filecloud/internal/object"
)

var (
	_errNoProtectedDeletion = errors.New("--confirm-delete requires a protected pending deletion candidate for this worktree")
	_errWrongDeletePrefix   = errors.New("--confirm-delete must exactly match the 12-character prefix of this worktree's protected candidate")
)

// A prepared candidate carries policy decisions for just the session that observed them.
type _preparedSyncPublication struct {
	session                    *_lockedBoundWorktreeSession
	snapshot                   worktreeSnapshot
	candidate                  _publicationCandidate
	deleted, tracked           int64
	requiresDeleteConfirmation bool
}

type _syncPublicationRoundOptions struct {
	scanConfig       worktreeScanConfig
	confirmDelete    string
	confirmDeleteSet bool
	budget           *_replayBudget
	stdout           io.Writer // Used by the legacy checkout path until it has its own handoff.
}

type _syncPublicationRound struct {
	db      *sql.DB
	binding clientBinding
	options bindOptions
	config  libraryClientConfig
	budget  *_replayBudget
	stdout  io.Writer
}

func newSyncPublicationRound(session *_lockedBoundWorktreeSession, round _syncPublicationRoundOptions) _syncPublicationRound {
	options := session.bindOptions()
	options.confirmDelete, options.confirmDeleteSet = round.confirmDelete, round.confirmDeleteSet
	options.scanConfig = round.scanConfig
	options.scanConfig.trackedPaths = maps.Clone(round.scanConfig.trackedPaths)
	options.scanConfig.ignoredRootNames = maps.Clone(round.scanConfig.ignoredRootNames)
	options.scanConfig.ignoredPaths = maps.Clone(round.scanConfig.ignoredPaths)
	return _syncPublicationRound{db: session.db, binding: session.binding, options: options,
		config: session.config, budget: round.budget, stdout: round.stdout}
}

func (r _syncPublicationRound) start(ctx context.Context, prepared _preparedSyncPublication) (_publicationOutcome, error) {
	if r.options.confirmDeleteSet {
		return _publicationOutcome{}, _errNoProtectedDeletion
	}
	candidate := prepared.candidate
	if candidate.CapturedRoot != prepared.snapshot.root {
		return _publicationOutcome{}, _errPublicationRequestInvalid
	}
	candidate.CapturedData = bytes.Clone(candidate.CapturedData)
	candidate.CandidateData = bytes.Clone(candidate.CandidateData)
	pending := _syncPublicationState{_publicationCandidate: candidate,
		_syncPublicationFields: _syncPublicationFields{DeletionCount: prepared.deleted, TrackedCount: prepared.tracked,
			RequiresDeleteConfirmation: prepared.requiresDeleteConfirmation}}
	if err := saveSyncPublication(ctx, r.db, r.binding, &pending); err != nil {
		return _publicationOutcome{}, err
	}
	if pending.RequiresDeleteConfirmation {
		return _publicationOutcome{kind: _publicationOutcomeConfirmationRequired}, deletionConfirmationError(pending)
	}
	return r.upload(ctx, clonePublicationSnapshot(prepared.snapshot), pending)
}

func (r _syncPublicationRound) resumeCurrent(ctx context.Context, pending _syncPublicationState) (_publicationOutcome, error) {
	snapshot, err := scanWorktreeWithConfig(r.options.worktreeRoot, r.options.scanConfig)
	if err != nil {
		return _publicationOutcome{}, fmt.Errorf("scan worktree: %w", err)
	}
	head, err := getRemoteHead(ctx, r.options.base, r.binding.LibraryID, r.options.token)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if head.CommitID == nil {
		return _publicationOutcome{}, errors.New("bound library Head is empty")
	}
	return r.resume(ctx, snapshot, head, pending)
}

func clonePublicationSnapshot(snapshot worktreeSnapshot) worktreeSnapshot {
	snapshot.blocks = maps.Clone(snapshot.blocks)
	snapshot.paths = slices.Clone(snapshot.paths)
	snapshot.objects = slices.Clone(snapshot.objects)
	for index := range snapshot.objects {
		snapshot.objects[index].data = bytes.Clone(snapshot.objects[index].data)
	}
	return snapshot
}

func (r _syncPublicationRound) resume(ctx context.Context, snapshot worktreeSnapshot, head remoteHead,
	pending _syncPublicationState) (_publicationOutcome, error) {
	if r.options.confirmDeleteSet && !pending.RequiresDeleteConfirmation && !pending.LegacyRevalidationRequired {
		return _publicationOutcome{}, _errNoProtectedDeletion
	}
	if err := _verifyPendingPublicationChain(ctx, r.options, pending, r.binding, r.budget); err != nil {
		return _publicationOutcome{}, err
	}
	if *head.CommitID == pending.CandidateCommit {
		if snapshot.root != pending.CapturedRoot {
			return _publicationOutcome{kind: _publicationOutcomeRerunSync}, recoverPublishedCandidate(ctx, r.db, r.options, r.binding, head, pending)
		}
		if pending.CandidateRoot == pending.CapturedRoot {
			if err := finalizePublished(ctx, r.db, r.binding, snapshot, head, pending); err != nil {
				return _publicationOutcome{}, err
			}
			return _publicationOutcome{kind: _publicationOutcomeFinalized}, nil
		}
		return _publicationOutcome{kind: _publicationOutcomeCheckoutPending}, transitionPublishedSuccessor(ctx, r.db, r.options, r.binding, snapshot, head, pending, r.stdout, r.config, r.budget)
	}
	ancestor, err := _remoteCommitDescendsFrom(ctx, r.options, *head.CommitID, pending.CandidateCommit, r.binding.UserID, r.budget)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if ancestor {
		if snapshot.root == pending.CapturedRoot {
			return _publicationOutcome{kind: _publicationOutcomeCheckoutPending}, transitionPublishedSuccessor(ctx, r.db, r.options, r.binding, snapshot, head, pending, r.stdout, r.config, r.budget)
		}
		return _publicationOutcome{kind: _publicationOutcomeRerunSync}, recoverPublishedCandidate(ctx, r.db, r.options, r.binding, head, pending)
	}
	if *head.CommitID != pending.ExpectedHead {
		return r.replacePendingForTrivialMerge(ctx, snapshot, head, pending)
	}
	if head.ETag != pending.ExpectedETag {
		if err := refreshPendingETag(ctx, r.db, r.binding.Worktree, &pending, head.ETag); err != nil {
			return _publicationOutcome{}, err
		}
	}
	if snapshot.root != pending.CapturedRoot {
		if err := discardPendingPublication(ctx, r.db, r.binding.Worktree, pending); err != nil {
			return _publicationOutcome{}, err
		}
		message := "worktree changed after pending publication was created; stale candidate discarded, rerun sync"
		if pending.LegacyRevalidationRequired {
			message = "worktree changed after legacy pending publication was created; stale candidate discarded, rerun sync"
		}
		return _publicationOutcome{kind: _publicationOutcomeRerunSync}, errors.New(message)
	}
	if pending.LegacyRevalidationRequired {
		deleted, tracked := deletionStats(r.options.scanConfig.trackedPaths, snapshot.paths)
		if err := revalidateLegacySyncPublication(ctx, r.db, r.binding.Worktree, &pending, deleted, tracked); err != nil {
			return _publicationOutcome{}, err
		}
	}
	if pending.CandidateCommit != pending.CapturedCommit {
		if _, err := _rebuildPendingMerge(ctx, r.options, r.binding, snapshot, pending, r.budget); err != nil {
			return _publicationOutcome{}, fmt.Errorf("revalidate pending recursive merge: %w", err)
		}
	}
	if r.options.confirmDeleteSet && r.options.confirmDelete != pending.CandidateCommit[:deleteCandidatePrefixLen] {
		return _publicationOutcome{}, _errWrongDeletePrefix
	}
	if pending.RequiresDeleteConfirmation && !pending.DeleteConfirmed {
		if !r.options.confirmDeleteSet {
			return _publicationOutcome{kind: _publicationOutcomeConfirmationRequired}, deletionConfirmationError(pending)
		}
		if err := confirmSyncPublication(ctx, r.db, r.binding.Worktree, &pending); err != nil {
			return _publicationOutcome{}, err
		}
	}
	return r.upload(ctx, snapshot, pending)
}

func (r _syncPublicationRound) upload(ctx context.Context, snapshot worktreeSnapshot, pending _syncPublicationState) (_publicationOutcome, error) {
	if pending.LegacyRevalidationRequired {
		return _publicationOutcome{}, errors.New("legacy pending publication requires deletion revalidation before publication")
	}
	var directories []scannedObject
	if pending.CandidateCommit != pending.CapturedCommit {
		rebuilt, err := _rebuildPendingMerge(ctx, r.options, r.binding, snapshot, pending, r.budget)
		if err != nil {
			return _publicationOutcome{}, fmt.Errorf("rebuild pending recursive merge for upload: %w", err)
		}
		directories = rebuilt.directories
	}
	return r.uploadPrepared(ctx, snapshot, pending, directories)
}

func (r _syncPublicationRound) uploadPrepared(ctx context.Context, snapshot worktreeSnapshot, pending _syncPublicationState,
	directories []scannedObject) (_publicationOutcome, error) {
	if err := uploadSnapshot(ctx, r.options, snapshot); err != nil {
		return _publicationOutcome{}, err
	}
	if err := uploadMissingSyncCommit(ctx, r.options, pending.CapturedCommit, pending.CapturedData); err != nil {
		return _publicationOutcome{}, err
	}
	if err := uploadMergedDirectories(ctx, r.options, directories); err != nil {
		return _publicationOutcome{}, err
	}
	if err := uploadCandidateHistory(ctx, r.options, pending.CandidateHistory); err != nil {
		return _publicationOutcome{}, err
	}
	if pending.CandidateCommit != pending.CapturedCommit {
		if err := uploadMissingSyncCommit(ctx, r.options, pending.CandidateCommit, pending.CandidateData); err != nil {
			return _publicationOutcome{}, err
		}
	}
	return r.publish(ctx, snapshot, pending)
}

func (r _syncPublicationRound) publish(ctx context.Context, snapshot worktreeSnapshot, pending _syncPublicationState) (_publicationOutcome, error) {
	current, err := headBeforePublication(ctx, r.options, r.config, pending._publicationCandidate)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if *current.CommitID != pending.ExpectedHead {
		return r.resume(ctx, snapshot, current, pending)
	}
	if current.ETag != pending.ExpectedETag {
		if err := refreshPendingETag(ctx, r.db, r.binding.Worktree, &pending, current.ETag); err != nil {
			return _publicationOutcome{}, err
		}
	}
	if err := _assertPendingPublication(ctx, r.db, r.binding.Worktree, pending); err != nil {
		return _publicationOutcome{}, err
	}
	_, _, publishErr := updateRemoteHead(ctx, r.options.base, r.binding.LibraryID, r.options.token, pending.ExpectedETag, pending.CandidateCommit)
	published, observed, err := headAfterPublication(ctx, r.options, publishErr)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if published.CommitID == nil {
		return _publicationOutcome{}, errors.Join(publishErr, errors.New("library Head became empty during publication"))
	}
	switch *published.CommitID {
	case pending.CandidateCommit:
		return r.resume(ctx, observed, published, pending)
	case pending.ExpectedHead:
		if published.ETag != pending.ExpectedETag {
			if err := refreshPendingETag(ctx, r.db, r.binding.Worktree, &pending, published.ETag); err != nil {
				return _publicationOutcome{}, errors.Join(publishErr, err)
			}
		}
		return _publicationOutcome{kind: _publicationOutcomeRerunSync}, errors.Join(publishErr, errors.New("library Head publication did not complete; rerun sync"))
	default:
		outcome, err := r.resume(ctx, observed, published, pending)
		if err != nil {
			return outcome, errors.Join(publishErr, err)
		}
		return outcome, nil
	}
}

func uploadMissingSyncCommit(ctx context.Context, options bindOptions, id string, data []byte) error {
	missing, err := checkRemoteObjects(ctx, options, []clientObjectReference{{ObjectID: id, ObjectType: "Commit"}})
	if err != nil || !missing["commits\x00"+id] {
		return err
	}
	return putMetadata(ctx, options.base, options.libraryID, options.token, "commits", id, data)
}

func uploadMergedDirectories(ctx context.Context, options bindOptions, values []scannedObject) error {
	byID := make(map[string]scannedObject, len(values))
	for _, value := range values {
		byID[value.id] = value
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	references := make([]clientObjectReference, 0, len(ids))
	for _, id := range ids {
		references = append(references, clientObjectReference{ObjectID: id, ObjectType: "Directory"})
	}
	missing, err := checkRemoteObjects(ctx, options, references)
	if err != nil {
		return err
	}
	for _, id := range ids {
		value := byID[id]
		if missing["directories\x00"+id] {
			if err := putMetadata(ctx, options.base, options.libraryID, options.token, "directories", id, value.data); err != nil {
				return err
			}
		}
	}
	return nil
}

func uploadCandidateHistory(ctx context.Context, options bindOptions, history [][]byte) error {
	if len(history) == 0 {
		return nil
	}
	references := make([]clientObjectReference, 0, len(history))
	for _, data := range history {
		references = append(references, clientObjectReference{ObjectID: object.ID(data), ObjectType: "Commit"})
	}
	missing, err := checkRemoteObjects(ctx, options, references)
	if err != nil {
		return err
	}
	for _, data := range history {
		id := object.ID(data)
		if missing["commits\x00"+id] {
			if err := putMetadata(ctx, options.base, options.libraryID, options.token, "commits", id, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func finalizePublished(ctx context.Context, db *sql.DB, binding clientBinding, snapshot worktreeSnapshot, head remoteHead,
	pending _syncPublicationState) error {
	if head.CommitID == nil || *head.CommitID != pending.CandidateCommit {
		return errors.New("published Head does not match pending publication")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := updateBindingAndIndex(ctx, tx, binding, pending.CandidateCommit, pending.CandidateRoot, head.ETag, snapshot.paths); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := _deletePendingPublication(ctx, tx, binding.Worktree, pending,
		"publication changed during finalization"); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finalize publication: %w", err)
	}
	return nil
}
