package clientapp

import (
	"context"
)

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
	current, err := headBeforePublication(ctx, r.options, r.config, pending._publicationCandidate, r.binding.SyncBaseRoot)
	if err != nil {
		return _publicationOutcome{}, err
	}
	relation, err := r.classifyHead(ctx, current, pending)
	if err != nil {
		return _publicationOutcome{}, err
	}
	if relation != _restoreHeadExpected {
		return r.settleObservedHead(ctx, snapshot, current, pending, relation)
	}
	if err := _assertPendingPublication(ctx, r.db, r.binding.Worktree, pending); err != nil {
		return _publicationOutcome{}, err
	}
	conflictingHead, conflict, publishErr := updateRemoteHeadOnce(ctx, r.base, r.binding.LibraryID, r.token,
		pending.ExpectedETag, pending.CandidateCommit)
	return r.resolvePublicationResult(ctx, pending, conflictingHead, conflict, publishErr)
}
