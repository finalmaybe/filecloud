package clientapp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mingming-cn/filecloud/internal/object"
)

// PublicationKind identifies the workflow that owns a pending publication.
type PublicationKind string

const (
	PublicationKindSync    PublicationKind = "sync"
	PublicationKindRestore PublicationKind = "restore"

	_maxCandidateCommitBytes  = 64 << 10
	_maxCandidateHistoryBytes = 8 + maxSyncParentWalk*(_maxCandidateCommitBytes+4)
	_candidateHistoryMagic    = "FCH1"
	_emptyCandidateHistory    = _candidateHistoryMagic + "\x00\x00\x00\x00"
	_restorePreviewHeader     = 8
	_restorePreviewMaxBytes   = _restorePreviewHeader + _restorePreviewPathLimit*(4+_mergeMaxPath)
	_emptyRestorePreview      = "FRP1\x00\x00\x00\x00"
)

type _pendingPublicationState interface {
	pendingPublication()
}

type _publicationCandidate struct {
	BaseCommit, BaseRoot, ExpectedHead, ExpectedETag string
	CandidateCommit, CandidateRoot                   string
	CandidateData                                    []byte
	CapturedCommit, CapturedRoot                     string
	CapturedData                                     []byte
}

type _syncPublicationFields struct {
	DeletionCount, TrackedCount int64
	RequiresDeleteConfirmation  bool
	DeleteConfirmed             bool
	LegacyRevalidationRequired  bool
}

type _syncPublicationState struct {
	_publicationCandidate
	_syncPublicationFields
	CandidateHistory [][]byte

	captured, candidate object.Commit
	mergeSequence       []_pendingMergeCommit
}

func (_syncPublicationState) pendingPublication() {}

type _restorePublicationFields struct {
	SourceCommit, SourcePath, SourceRoot         string
	CreatedCount, UpdatedCount                   int64
	TypeReplacementCount, RemovedDescendantCount int64
	PreservedCurrentOnlyCount, ChangedPathCount  int64
	PreviewTruncated, RestoreConfirmed           bool
}

type _restorePublicationState struct {
	_publicationCandidate
	_restorePublicationFields
	ChangedPathPreview []string
}

func (_restorePublicationState) pendingPublication() {}

// _pendingPublicationRow is the SQLite representation, not a state that callers can act on.
type _pendingPublicationRow struct {
	_publicationCandidate
	_syncPublicationFields
	_restorePublicationFields
	kind                          PublicationKind
	candidateHistory, pathPreview []byte
}

const _pendingPublicationColumns = `worktree, publication_kind, base_commit, base_root, expected_head,
	expected_etag, candidate_commit, candidate_root, candidate_data, captured_commit, captured_root, captured_data,
	candidate_history, deletion_count, tracked_count, requires_delete_confirmation, delete_confirmed,
	legacy_revalidation_required, source_commit, source_path, source_root, created_count, updated_count,
	type_replacement_count, removed_descendant_count, preserved_current_only_count, changed_path_preview,
	changed_path_count, preview_truncated, restore_confirmed`

var _pendingPublicationWhere = strings.Join(strings.FieldsFunc(_pendingPublicationColumns, func(r rune) bool {
	return r == ',' || r == ' ' || r == '\t' || r == '\n'
}), " = ? AND ") + " = ?"

type _pendingPublicationExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func loadPendingPublication(ctx context.Context, db clientSchemaQuerier, binding clientBinding) (_pendingPublicationState, error) {
	var row _pendingPublicationRow
	var candidateLength, capturedLength, historyLength, previewLength int
	var blobs bool
	err := db.QueryRowContext(ctx, `SELECT publication_kind, base_commit, base_root, expected_head, expected_etag, candidate_commit, candidate_root,
		length(candidate_data), CASE WHEN length(candidate_data) BETWEEN 1 AND `+strconv.Itoa(_maxCandidateCommitBytes)+` THEN candidate_data END,
		captured_commit, captured_root, length(captured_data),
		CASE WHEN length(captured_data) BETWEEN 1 AND `+strconv.Itoa(_maxCandidateCommitBytes)+` THEN captured_data END,
		length(candidate_history), CASE WHEN length(candidate_history) BETWEEN 8 AND `+strconv.Itoa(_maxCandidateHistoryBytes)+` THEN candidate_history END,
		deletion_count, tracked_count, CASE WHEN requires_delete_confirmation IN (0, 1) THEN requires_delete_confirmation END,
		CASE WHEN delete_confirmed IN (0, 1) THEN delete_confirmed END,
		CASE WHEN legacy_revalidation_required IN (0, 1) THEN legacy_revalidation_required END,
		CASE WHEN length(CAST(source_commit AS BLOB)) <= 64 THEN source_commit END,
		CASE WHEN length(CAST(source_path AS BLOB)) <= 1024 THEN source_path END,
		CASE WHEN length(CAST(source_root AS BLOB)) <= 64 THEN source_root END,
		created_count, updated_count, type_replacement_count, removed_descendant_count, preserved_current_only_count,
		length(changed_path_preview), CASE WHEN length(changed_path_preview) BETWEEN 8 AND `+strconv.Itoa(_restorePreviewMaxBytes)+` THEN changed_path_preview END,
		changed_path_count, CASE WHEN preview_truncated IN (0, 1) THEN preview_truncated END,
		CASE WHEN restore_confirmed IN (0, 1) THEN restore_confirmed END,
		typeof(candidate_data) = 'blob' AND typeof(captured_data) = 'blob'
			AND typeof(candidate_history) = 'blob' AND typeof(changed_path_preview) = 'blob'
		FROM pending_publications WHERE worktree = ?`, binding.Worktree).Scan(
		&row.kind, &row.BaseCommit, &row.BaseRoot, &row.ExpectedHead, &row.ExpectedETag, &row.CandidateCommit,
		&row.CandidateRoot, &candidateLength, &row.CandidateData, &row.CapturedCommit, &row.CapturedRoot,
		&capturedLength, &row.CapturedData, &historyLength, &row.candidateHistory, &row.DeletionCount,
		&row.TrackedCount, &row.RequiresDeleteConfirmation, &row.DeleteConfirmed, &row.LegacyRevalidationRequired,
		&row.SourceCommit, &row.SourcePath, &row.SourceRoot, &row.CreatedCount, &row.UpdatedCount, &row.TypeReplacementCount,
		&row.RemovedDescendantCount, &row.PreservedCurrentOnlyCount, &previewLength, &row.pathPreview, &row.ChangedPathCount,
		&row.PreviewTruncated, &row.RestoreConfirmed, &blobs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pending publication: %w", err)
	}
	if candidateLength != len(row.CandidateData) || capturedLength != len(row.CapturedData) ||
		historyLength != len(row.candidateHistory) || previewLength != len(row.pathPreview) {
		return nil, errors.New("pending publication metadata exceeds synchronization budget")
	}
	if !blobs {
		return nil, errors.New("pending publication metadata has an invalid SQLite type")
	}
	return row.decode(binding)
}

func (row _pendingPublicationRow) decode(binding clientBinding) (_pendingPublicationState, error) {
	switch row.kind {
	case PublicationKindSync:
		if row._restorePublicationFields != (_restorePublicationFields{}) || string(row.pathPreview) != _emptyRestorePreview {
			return nil, errors.New("sync publication contains restore state")
		}
		history, err := _decodeCandidateHistory(row.candidateHistory)
		if err != nil {
			return nil, err
		}
		value := _syncPublicationState{_publicationCandidate: row._publicationCandidate,
			_syncPublicationFields: row._syncPublicationFields, CandidateHistory: history}
		if err := verifySyncPublication(&value, binding); err != nil {
			return nil, err
		}
		return value, nil
	case PublicationKindRestore:
		if row._syncPublicationFields != (_syncPublicationFields{}) || len(row.candidateHistory) != 0 {
			return nil, errors.New("restore publication contains synchronization-only state")
		}
		paths, err := _decodeRestorePreview(row.pathPreview)
		if err != nil {
			return nil, fmt.Errorf("validate restore publication: %w", err)
		}
		value := _restorePublicationState{row._publicationCandidate, row._restorePublicationFields, paths}
		if err := verifyRestorePublication(value, binding); err != nil {
			return nil, fmt.Errorf("validate restore publication: %w", err)
		}
		return value, nil
	default:
		return nil, errors.New("unsupported pending publication kind")
	}
}

func makePendingPublicationRow(value _pendingPublicationState) (_pendingPublicationRow, error) {
	var row _pendingPublicationRow
	var err error
	switch value := value.(type) {
	case _syncPublicationState:
		row = _pendingPublicationRow{kind: PublicationKindSync, _publicationCandidate: value._publicationCandidate,
			_syncPublicationFields: value._syncPublicationFields, pathPreview: []byte(_emptyRestorePreview)}
		row.candidateHistory, err = _encodeCandidateHistory(value.CandidateHistory)
	case _restorePublicationState:
		row = _pendingPublicationRow{kind: PublicationKindRestore, _publicationCandidate: value._publicationCandidate,
			_restorePublicationFields: value._restorePublicationFields, candidateHistory: []byte{}}
		row.pathPreview, err = _encodeRestorePreview(value.ChangedPathPreview)
	default:
		return row, errors.New("unsupported pending publication kind")
	}
	return row, err
}

func (row _pendingPublicationRow) args(worktree string) []any {
	return []any{worktree, row.kind, row.BaseCommit, row.BaseRoot, row.ExpectedHead, row.ExpectedETag,
		row.CandidateCommit, row.CandidateRoot, row.CandidateData, row.CapturedCommit, row.CapturedRoot,
		row.CapturedData, row.candidateHistory, row.DeletionCount, row.TrackedCount,
		row.RequiresDeleteConfirmation, row.DeleteConfirmed, row.LegacyRevalidationRequired,
		row.SourceCommit, row.SourcePath, row.SourceRoot, row.CreatedCount, row.UpdatedCount,
		row.TypeReplacementCount, row.RemovedDescendantCount, row.PreservedCurrentOnlyCount,
		row.pathPreview, row.ChangedPathCount, row.PreviewTruncated, row.RestoreConfirmed}
}

func saveSyncPublication(ctx context.Context, db *sql.DB, binding clientBinding, value *_syncPublicationState) error {
	if err := verifySyncPublication(value, binding); err != nil {
		return err
	}
	return insertPendingPublication(ctx, db, binding.Worktree, *value)
}

func saveRestorePublication(ctx context.Context, tx _pendingPublicationExecer, binding clientBinding, value _restorePublicationState) error {
	if err := verifyRestorePublication(value, binding); err != nil {
		return fmt.Errorf("verify restore candidate: %w", err)
	}
	return insertPendingPublication(ctx, tx, binding.Worktree, value)
}

func insertPendingPublication(ctx context.Context, execer _pendingPublicationExecer, worktree string, value _pendingPublicationState) error {
	row, err := makePendingPublicationRow(value)
	if err != nil {
		return err
	}
	args := row.args(worktree)
	_, err = execer.ExecContext(ctx, "INSERT INTO pending_publications("+_pendingPublicationColumns+
		") VALUES (?"+strings.Repeat(", ?", len(args)-1)+")", args...)
	if err != nil {
		return fmt.Errorf("save pending publication: %w", err)
	}
	return nil
}

func execPendingPublicationCAS(ctx context.Context, execer _pendingPublicationExecer, statement string, statementArgs []any,
	worktree string, old _pendingPublicationState, changedMessage string) error {
	row, err := makePendingPublicationRow(old)
	if err != nil {
		return err
	}
	result, err := execer.ExecContext(ctx, statement+" WHERE "+_pendingPublicationWhere,
		append(statementArgs, row.args(worktree)...)...)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.Join(errors.New(changedMessage), err)
	}
	return nil
}

func _deletePendingPublication(ctx context.Context, execer _pendingPublicationExecer, worktree string,
	old _pendingPublicationState, changedMessage string) error {
	return execPendingPublicationCAS(ctx, execer, "DELETE FROM pending_publications", nil, worktree, old, changedMessage)
}

func _assertPendingPublication(ctx context.Context, execer _pendingPublicationExecer, worktree string, old _pendingPublicationState) error {
	return execPendingPublicationCAS(ctx, execer, "UPDATE pending_publications SET worktree = worktree", nil,
		worktree, old, "pending publication changed before remote publication")
}

func discardPendingPublication(ctx context.Context, db *sql.DB, worktree string, pending _pendingPublicationState) error {
	if err := _deletePendingPublication(ctx, db, worktree, pending, "pending publication changed before discard"); err != nil {
		return fmt.Errorf("discard changed pending publication: %w", err)
	}
	return nil
}

// The caller owns the transaction: Sync also advances the captured baseline, while Restore leaves it for checkout.
func handoffPendingPublication(ctx context.Context, tx *sql.Tx, checkout pendingCheckout, pending _pendingPublicationState, changedMessage string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO pending_checkouts(server_url, library_id, worktree, user_id, device_id,
		target_commit, target_root, head_etag, apply_state, conflict_promotions)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, 'pending', ?)`, checkout.ServerURL, checkout.LibraryID, checkout.Worktree,
		checkout.UserID, checkout.DeviceID, checkout.TargetCommit, checkout.HeadETag, checkout.ConflictPromotions); err != nil {
		return err
	}
	return _deletePendingPublication(ctx, tx, checkout.Worktree, pending, changedMessage)
}

func _removePendingPublicationForUnbind(ctx context.Context, tx *sql.Tx, worktree string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM pending_publications WHERE worktree = ?", worktree)
	return err
}

func revalidateLegacySyncPublication(ctx context.Context, db *sql.DB, worktree string, pending *_syncPublicationState,
	deleted, tracked int64) error {
	protected := protectedDeletion(deleted, tracked)
	if err := execPendingPublicationCAS(ctx, db, `UPDATE pending_publications SET deletion_count = ?, tracked_count = ?,
		requires_delete_confirmation = ?, delete_confirmed = 0, legacy_revalidation_required = 0`,
		[]any{deleted, tracked, protected}, worktree, *pending, "legacy pending publication changed during revalidation"); err != nil {
		return fmt.Errorf("revalidate legacy pending publication: %w", err)
	}
	pending._syncPublicationFields = _syncPublicationFields{DeletionCount: deleted, TrackedCount: tracked, RequiresDeleteConfirmation: protected}
	return nil
}

func confirmSyncPublication(ctx context.Context, db *sql.DB, worktree string, pending *_syncPublicationState) error {
	if err := execPendingPublicationCAS(ctx, db, "UPDATE pending_publications SET delete_confirmed = 1", nil,
		worktree, *pending, "protected deletion candidate changed before confirmation"); err != nil {
		return fmt.Errorf("confirm pending deletion: %w", err)
	}
	pending.DeleteConfirmed = true
	return nil
}

func confirmRestorePublication(ctx context.Context, db *sql.DB, worktree string, pending *_restorePublicationState) error {
	if err := execPendingPublicationCAS(ctx, db, "UPDATE pending_publications SET restore_confirmed = 1", nil,
		worktree, *pending, "restore candidate changed before confirmation"); err != nil {
		return fmt.Errorf("confirm restore candidate: %w", err)
	}
	pending.RestoreConfirmed = true
	return nil
}

func refreshPendingETag(ctx context.Context, db *sql.DB, worktree string, pending *_syncPublicationState, etag string) error {
	if err := execPendingPublicationCAS(ctx, db, "UPDATE pending_publications SET expected_etag = ?", []any{etag},
		worktree, *pending, "pending publication changed before ETag refresh"); err != nil {
		return fmt.Errorf("refresh pending publication ETag: %w", err)
	}
	pending.ExpectedETag = etag
	return nil
}

func replaceSyncPublication(ctx context.Context, db *sql.DB, binding clientBinding, old _syncPublicationState, next *_syncPublicationState) error {
	history, err := _encodeCandidateHistory(next.CandidateHistory)
	if err != nil {
		return err
	}
	if err := verifySyncPublication(next, binding); err != nil {
		return fmt.Errorf("verify replacement pending publication: %w", err)
	}
	if err := execPendingPublicationCAS(ctx, db, `UPDATE pending_publications SET base_commit = ?, base_root = ?, expected_head = ?,
		expected_etag = ?, candidate_commit = ?, candidate_root = ?, candidate_data = ?, captured_commit = ?,
		captured_root = ?, captured_data = ?, candidate_history = ?, deletion_count = ?, tracked_count = ?,
		requires_delete_confirmation = ?, delete_confirmed = 0, legacy_revalidation_required = 0`,
		[]any{next.BaseCommit, next.BaseRoot, next.ExpectedHead, next.ExpectedETag, next.CandidateCommit,
			next.CandidateRoot, next.CandidateData, next.CapturedCommit, next.CapturedRoot, next.CapturedData,
			history, next.DeletionCount, next.TrackedCount, next.RequiresDeleteConfirmation},
		binding.Worktree, old, "pending publication changed before conflict replacement"); err != nil {
		return fmt.Errorf("replace pending publication after Head conflict: %w", err)
	}
	return nil
}

func _encodeCandidateHistory(commits [][]byte) ([]byte, error) {
	if len(commits) > maxSyncParentWalk {
		return nil, errors.New("candidate history exceeds synchronization budget")
	}
	size := 8
	for _, data := range commits {
		if len(data) == 0 || len(data) > _maxCandidateCommitBytes || size > _maxCandidateHistoryBytes-4-len(data) {
			return nil, errors.New("candidate history metadata exceeds synchronization budget")
		}
		size += 4 + len(data)
	}
	result := make([]byte, size)
	copy(result, _candidateHistoryMagic)
	binary.BigEndian.PutUint32(result[4:8], uint32(len(commits)))
	offset := 8
	for _, data := range commits {
		binary.BigEndian.PutUint32(result[offset:offset+4], uint32(len(data)))
		offset += 4
		copy(result[offset:], data)
		offset += len(data)
	}
	return result, nil
}

func _decodeCandidateHistory(data []byte) ([][]byte, error) {
	if len(data) < 8 || len(data) > _maxCandidateHistoryBytes || string(data[:4]) != _candidateHistoryMagic {
		return nil, errors.New("candidate history has an invalid encoding")
	}
	count := binary.BigEndian.Uint32(data[4:8])
	if count > maxSyncParentWalk {
		return nil, errors.New("candidate history exceeds synchronization budget")
	}
	result := make([][]byte, 0, int(count))
	offset := 8
	for range count {
		if len(data)-offset < 4 {
			return nil, errors.New("candidate history is truncated")
		}
		size := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4
		if size == 0 || size > _maxCandidateCommitBytes || uint64(size) > uint64(len(data)-offset) {
			return nil, errors.New("candidate history contains an invalid metadata length")
		}
		result = append(result, data[offset:offset+int(size)])
		offset += int(size)
	}
	if offset != len(data) {
		return nil, errors.New("candidate history has trailing data")
	}
	return result, nil
}

func _encodeRestorePreview(paths []string) ([]byte, error) {
	if len(paths) > _restorePreviewPathLimit {
		return nil, errors.New("restore preview exceeds path limit")
	}
	ordered := append([]string(nil), paths...)
	sort.Strings(ordered)
	for index, path := range ordered {
		if !object.ValidPath(path) {
			return nil, fmt.Errorf("restore preview path %q is invalid", path)
		}
		if index > 0 && ordered[index-1] == path {
			return nil, errors.New("restore preview contains a duplicate path")
		}
	}
	result := make([]byte, _restorePreviewHeader)
	copy(result, []byte(_emptyRestorePreview))
	binary.BigEndian.PutUint32(result[4:8], uint32(len(ordered)))
	for _, path := range ordered {
		if len(path) > _mergeMaxPath || len(result) > _restorePreviewMaxBytes-4-len(path) {
			return nil, errors.New("restore preview exceeds metadata budget")
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(path)))
		result = append(result, length[:]...)
		result = append(result, path...)
	}
	return result, nil
}

func _decodeRestorePreview(data []byte) ([]string, error) {
	if len(data) < _restorePreviewHeader || len(data) > _restorePreviewMaxBytes || string(data[:4]) != _emptyRestorePreview[:4] {
		return nil, errors.New("restore preview has an invalid encoding")
	}
	count := binary.BigEndian.Uint32(data[4:8])
	if count > _restorePreviewPathLimit {
		return nil, errors.New("restore preview exceeds path limit")
	}
	paths := make([]string, 0, int(count))
	offset := _restorePreviewHeader
	for range count {
		if len(data)-offset < 4 {
			return nil, errors.New("restore preview is truncated")
		}
		length := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4
		if length == 0 || length > _mergeMaxPath || uint64(length) > uint64(len(data)-offset) {
			return nil, errors.New("restore preview contains an invalid path length")
		}
		path := string(data[offset : offset+int(length)])
		offset += int(length)
		if !object.ValidPath(path) || len(paths) != 0 && paths[len(paths)-1] >= path {
			return nil, errors.New("restore preview paths are not canonical")
		}
		paths = append(paths, path)
	}
	if offset != len(data) {
		return nil, errors.New("restore preview has trailing data")
	}
	return paths, nil
}

type _pendingMergeCommit struct {
	id     string
	data   []byte
	commit object.Commit
}

func decodeSyncCommits(value *_syncPublicationState, binding clientBinding) error {
	if len(value.CapturedData) == 0 || len(value.CapturedData) > _maxCandidateCommitBytes ||
		len(value.CandidateData) == 0 || len(value.CandidateData) > _maxCandidateCommitBytes {
		return errors.New("pending publication commit metadata exceeds synchronization budget")
	}
	captured, err := object.VerifyCommit(value.CapturedData, value.CapturedCommit)
	capturedLocal := len(captured.Parents) == 1 && captured.Parents[0] == binding.SyncBase
	capturedMerge := len(captured.Parents) == 2 && captured.Parents[0] != captured.Parents[1] &&
		captured.Parents[0] != value.CapturedCommit && captured.Parents[1] != value.CapturedCommit
	if err != nil || captured.AuthorUserID != binding.UserID || captured.DeviceID != binding.DeviceID ||
		captured.Root != value.CapturedRoot || (!capturedLocal && !capturedMerge) {
		return errors.New("pending publication captured commit is invalid")
	}
	history := value.CandidateHistory
	candidate, err := object.VerifyCommit(value.CandidateData, value.CandidateCommit)
	if err != nil || candidate.AuthorUserID != binding.UserID || candidate.DeviceID != binding.DeviceID ||
		candidate.Root != value.CandidateRoot {
		return errors.New("pending publication candidate commit is invalid")
	}
	value.captured, value.candidate = captured, candidate
	var sequence []_pendingMergeCommit
	if capturedMerge {
		sequence = append(sequence, _pendingMergeCommit{id: value.CapturedCommit, data: value.CapturedData, commit: captured})
	}
	if value.CandidateCommit == value.CapturedCommit {
		if len(history) != 0 || !bytes.Equal(value.CandidateData, value.CapturedData) ||
			(capturedLocal && (value.BaseCommit != binding.SyncBase || value.BaseRoot != binding.SyncBaseRoot ||
				value.ExpectedHead != binding.SyncBase)) || (capturedMerge && captured.Parents[0] != value.ExpectedHead) {
			return errors.New("pending local publication has an invalid history")
		}
		value.mergeSequence = sequence
		return nil
	}
	if len(history)+len(sequence)+1 > maxSyncParentWalk {
		return errors.New("pending publication merge history exceeds synchronization budget")
	}
	seen := map[string]bool{value.CapturedCommit: true}
	if capturedMerge {
		seen[captured.Parents[0]] = true
	}
	previous := value.CapturedCommit
	for index := 0; index <= len(history); index++ {
		data, id, commit := value.CandidateData, value.CandidateCommit, candidate
		var verifyErr error
		if index < len(history) {
			data = history[index]
			id = object.ID(data)
			commit, verifyErr = object.VerifyCommit(data, id)
		}
		if verifyErr != nil || commit.AuthorUserID != binding.UserID || commit.DeviceID != binding.DeviceID ||
			len(commit.Parents) != 2 || commit.Parents[1] != previous || seen[id] || seen[commit.Parents[0]] {
			return fmt.Errorf("pending publication candidate chain is not linked to the binding Sync Base: merge history entry %d is invalid", index)
		}
		seen[id] = true
		seen[commit.Parents[0]] = true
		sequence = append(sequence, _pendingMergeCommit{id: id, data: data, commit: commit})
		previous = id
	}
	if candidate.Parents[0] != value.ExpectedHead {
		return errors.New("pending publication candidate does not match expected Head")
	}
	if len(sequence) > 1 && value.BaseCommit != sequence[len(sequence)-2].commit.Parents[0] ||
		len(sequence) == 1 && !capturedMerge && value.BaseCommit != binding.SyncBase {
		return errors.New("pending publication Base does not match merge history")
	}
	value.mergeSequence = sequence
	return nil
}

func verifySyncPublication(value *_syncPublicationState, binding clientBinding) error {
	if !object.ValidID(value.BaseCommit) || !object.ValidID(value.BaseRoot) || !object.ValidID(value.ExpectedHead) ||
		value.ExpectedETag == "" || value.BaseCommit == binding.SyncBase && value.BaseRoot != binding.SyncBaseRoot {
		return errors.New("pending publication is corrupt or does not match the binding")
	}
	return verifyLegacySyncPublication(value, binding)
}

func verifyLegacySyncPublication(value *_syncPublicationState, binding clientBinding) error {
	if err := decodeSyncCommits(value, binding); err != nil || value.DeletionCount < 0 ||
		value.TrackedCount < value.DeletionCount || value.DeleteConfirmed && !value.RequiresDeleteConfirmation ||
		(value.LegacyRevalidationRequired && (value.DeletionCount != 0 || value.TrackedCount != 0 ||
			value.RequiresDeleteConfirmation || value.DeleteConfirmed)) ||
		(!value.LegacyRevalidationRequired && value.RequiresDeleteConfirmation != protectedDeletion(value.DeletionCount, value.TrackedCount)) {
		return errors.Join(errors.New("pending publication is corrupt or does not match the binding"), err)
	}
	return nil
}

func validateRestorePublicationFields(value _restorePublicationState) error {
	if !object.ValidID(value.SourceCommit) || !object.ValidID(value.SourceRoot) || !object.ValidPath(value.SourcePath) {
		return errors.New("restore publication source fields are invalid")
	}
	if len(value.CandidateData) == 0 || len(value.CandidateData) > _maxCandidateCommitBytes ||
		len(value.CapturedData) == 0 || len(value.CapturedData) > _maxCandidateCommitBytes {
		return errors.New("restore publication commit metadata exceeds synchronization budget")
	}
	if value.CandidateRoot == value.CapturedRoot {
		return errors.New("restore publication candidate root must differ from captured root")
	}
	if value.ChangedPathCount <= 0 {
		return errors.New("restore publication must contain changed paths")
	}
	for _, count := range []int64{value.CreatedCount, value.UpdatedCount, value.TypeReplacementCount,
		value.RemovedDescendantCount, value.PreservedCurrentOnlyCount, value.ChangedPathCount} {
		if count < 0 || count > _mergeMaxObjects {
			return errors.New("restore publication statistics are invalid")
		}
	}
	if value.ChangedPathCount != value.CreatedCount+value.UpdatedCount+value.TypeReplacementCount {
		return errors.New("restore publication statistics do not match changed path count")
	}
	if value.TypeReplacementCount == 0 && value.RemovedDescendantCount != 0 {
		return errors.New("restore publication removed descendants without a type replacement")
	}
	paths := value.ChangedPathPreview
	if value.ChangedPathCount < int64(len(paths)) {
		return errors.New("restore publication changed path count is smaller than its preview")
	}
	if value.PreviewTruncated {
		if len(paths) != _restorePreviewPathLimit || value.ChangedPathCount <= int64(len(paths)) {
			return errors.New("restore publication preview truncation is inconsistent")
		}
	} else if value.ChangedPathCount != int64(len(paths)) {
		return errors.New("restore publication changed path count does not match its preview")
	}
	return nil
}

func verifyRestorePublication(value _restorePublicationState, binding clientBinding) error {
	if err := validateRestorePublicationFields(value); err != nil {
		return err
	}
	if !validClientUUID(binding.UserID) || !validClientUUID(binding.DeviceID) ||
		!object.ValidID(binding.SyncBase) || !object.ValidID(binding.SyncBaseRoot) || binding.HeadETag == "" {
		return errors.New("restore publication binding is invalid")
	}
	if value.ExpectedETag == "" || value.BaseCommit != value.ExpectedHead || value.CapturedCommit != value.ExpectedHead ||
		value.CapturedRoot != value.BaseRoot {
		return errors.New("restore publication captured state does not match its Base and expected Head")
	}
	for _, id := range []string{value.BaseCommit, value.BaseRoot, value.ExpectedHead, value.CandidateCommit,
		value.CandidateRoot, value.CapturedCommit, value.CapturedRoot} {
		if !object.ValidID(id) {
			return errors.New("restore publication object identity is invalid")
		}
	}
	captured, err := object.VerifyCommit(value.CapturedData, value.CapturedCommit)
	if err != nil || captured.AuthorUserID != binding.UserID || captured.Root != value.CapturedRoot {
		return errors.New("restore publication captured commit is invalid")
	}
	candidate, err := object.VerifyCommit(value.CandidateData, value.CandidateCommit)
	if err != nil || candidate.AuthorUserID != binding.UserID || candidate.DeviceID != binding.DeviceID ||
		candidate.Root != value.CandidateRoot || len(candidate.Parents) != 1 || candidate.Parents[0] != value.ExpectedHead ||
		candidate.Message != "restore "+value.SourceCommit+" "+value.SourcePath {
		return errors.New("restore publication candidate commit is invalid")
	}
	if _, err := parseCanonicalProtocolMtime(candidate.CreatedAt); err != nil {
		return errors.New("restore publication candidate timestamp is invalid")
	}
	return nil
}

// A valid preview can outlive the binding's Head or Sync Base (for example, after credentials are refreshed).
func restorePublicationMatchesBinding(value _restorePublicationState, binding clientBinding) bool {
	return value.BaseCommit == binding.SyncBase && value.BaseRoot == binding.SyncBaseRoot && value.ExpectedETag == binding.HeadETag
}
