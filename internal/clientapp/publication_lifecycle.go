package clientapp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"path/filepath"
	"sync"
)

var (
	_errPublicationSessionClosed           = errors.New("publication session is closed")
	_errPublicationSessionConcurrentUse    = errors.New("publication session is already in use")
	_errPublicationSessionLockMismatch     = errors.New("publication session lock capability does not match")
	_errPublicationSessionBindingMismatch  = errors.New("publication session binding does not match")
	_errPublicationSessionWorktreeMismatch = errors.New("publication session worktree identity does not match")
	_errPublicationRequestInvalid          = errors.New("publication request is invalid")
)

type _publicationOutcomeKind uint8

const (
	_publicationOutcomeIdle _publicationOutcomeKind = iota + 1
	_publicationOutcomeConfirmationRequired
	_publicationOutcomePreviewReady
	_publicationOutcomeRestoreNoOp
	_publicationOutcomeFinalized
	_publicationOutcomeCheckoutPending
	_publicationOutcomeRerunSync
	_publicationOutcomeRerunRestore
	_publicationOutcomeSyncReplan
	_publicationOutcomePending
)

type _publicationOutcome struct {
	kind           _publicationOutcomeKind
	replan         *_syncReplanChallenge
	checkout       *pendingCheckout
	restorePreview *restorePreview
}

// Kind classifies the lifecycle result without requiring error-string parsing.
func (o _publicationOutcome) Kind() _publicationOutcomeKind {
	return o.kind
}

type _publicationRequester interface {
	publicationRequest()
}

type _inspectPublicationRequest struct{}

func (_inspectPublicationRequest) publicationRequest() {}

type _startSyncPublicationRequest struct {
	prepared _preparedSyncPublication
	round    _syncPublicationRoundOptions
}

func (_startSyncPublicationRequest) publicationRequest() {}

// Sync and watch resume whichever workflow owns the durable candidate.
type _resumePublicationRequest struct {
	round _syncPublicationRoundOptions
}

func (_resumePublicationRequest) publicationRequest() {}

type _publicationLifecycle struct{}

// Advance validates the session scope before advancing one sealed lifecycle request.
func (m *_publicationLifecycle) Advance(ctx context.Context, s *_lockedBoundWorktreeSession,
	request _publicationRequester) (_publicationOutcome, error) {
	var noOutcome _publicationOutcome
	release, err := s.acquire(ctx)
	if err != nil {
		return noOutcome, err
	}
	defer release()
	switch request.(type) {
	case _inspectPublicationRequest, _startSyncPublicationRequest, _resumePublicationRequest, _replaceSyncPublicationRequest,
		_previewRestorePublicationRequest, _confirmRestorePublicationRequest:
	default:
		return noOutcome, _errPublicationRequestInvalid
	}
	checkout, err := loadPendingCheckout(ctx, s.db, s.binding.ServerURL, s.binding.LibraryID, s.binding.Worktree)
	if err != nil {
		return noOutcome, err
	}
	if checkout != nil {
		s.challenge = nil
		outcome := _publicationOutcome{kind: _publicationOutcomeCheckoutPending, checkout: checkout}
		switch request.(type) {
		case _previewRestorePublicationRequest, _confirmRestorePublicationRequest:
			return outcome, errors.New("restore cannot continue while a checkout is pending; run sync first")
		}
		return outcome, nil
	}
	pending, err := loadPendingPublication(ctx, s.db, s.binding)
	if err != nil {
		return noOutcome, err
	}
	switch request := request.(type) {
	case _previewRestorePublicationRequest:
		return newRestorePublicationRound(s).preview(ctx, pending, request)
	case _confirmRestorePublicationRequest:
		return newRestorePublicationRound(s).confirm(ctx, pending, request)
	case _startSyncPublicationRequest:
		if pending != nil || request.prepared.session != s {
			return noOutcome, _errPublicationRequestInvalid
		}
		s.challenge = nil
		return newSyncPublicationRound(s, request.round).start(ctx, request.prepared)
	case _resumePublicationRequest:
		s.challenge = nil
		switch value := pending.(type) {
		case _syncPublicationState:
			return newSyncPublicationRound(s, request.round).resumeCurrent(ctx, value)
		case _restorePublicationState:
			round := newRestorePublicationRound(s)
			round.options.scanConfig = clonePublicationScanConfig(request.round.scanConfig)
			return round.resumeCurrent(ctx, value)
		default:
			return noOutcome, _errPublicationRequestInvalid
		}
	case _replaceSyncPublicationRequest:
		c := request.challenge
		if c == nil || c != s.challenge || c.binding != s.binding || c.replacement == nil {
			return noOutcome, _errPublicationRequestInvalid
		}
		s.challenge = nil
		return c.replace(ctx)
	default:
		if pending != nil {
			return _publicationOutcome{kind: _publicationOutcomePending}, nil
		}
		return _publicationOutcome{kind: _publicationOutcomeIdle}, nil
	}
}

type _lockedBoundWorktreeSession struct {
	mu sync.Mutex

	active bool
	closed bool

	locks     *clientLocks
	ownsLocks bool
	lockNames []string
	db        *sql.DB
	binding   clientBinding
	token     []byte
	base      *url.URL
	root      *openedWorktree
	clientDir string
	config    libraryClientConfig
	challenge *_syncReplanChallenge
}

func openLockedBoundWorktreeSession(ctx context.Context, clientDir, worktree string,
	config libraryClientConfig) (_ *_lockedBoundWorktreeSession, retErr error) {
	config = normalizeLibraryClientConfig(config)
	canonicalClientDir, err := canonicalStateDir(clientDir)
	if err != nil {
		return nil, err
	}
	if err := checkStateDirFilesystem(canonicalClientDir, config.checkFilesystem); err != nil {
		return nil, err
	}
	canonicalWorktree, err := canonicalExistingPath(worktree)
	if err != nil {
		return nil, err
	}
	databasePath := filepath.Join(canonicalClientDir, _clientDatabaseName)
	locks := config.watchLocks
	ownsLocks := locks == nil
	if ownsLocks {
		locks, err = tryLockUnbind(ctx, canonicalClientDir, databasePath, canonicalWorktree, config)
		if err != nil {
			return nil, err
		}
	}
	session := &_lockedBoundWorktreeSession{locks: locks, ownsLocks: ownsLocks, clientDir: canonicalClientDir, config: config}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, session.Close())
		}
	}()

	db, err := openClientDB(databasePath, false)
	if err != nil {
		return nil, err
	}
	session.db = db
	if err := initializeClientSchema(ctx, db); err != nil {
		return nil, err
	}
	binding, token, err := readLockedSessionBinding(ctx, db, canonicalWorktree)
	if err != nil {
		return nil, err
	}
	defer clear(token)
	lockNames := bindingLockNames(binding.Worktree, binding.ServerURL, binding.LibraryID)
	if err := locks.validate(lockNames); err != nil {
		return nil, fmt.Errorf("%w: %v", _errPublicationSessionLockMismatch, err)
	}
	base, err := validateServerURL(binding.ServerURL)
	if err != nil {
		return nil, err
	}
	root, err := openWorktreeRoot(canonicalWorktree, config.checkFilesystem)
	if err != nil {
		return nil, err
	}
	if err := root.validateIdentity(); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %v", _errPublicationSessionWorktreeMismatch, err), root.Close())
	}
	session.lockNames = lockNames
	session.binding = binding
	session.token = bytes.Clone(token)
	session.base = base
	session.root = root
	return session, nil
}

func readLockedSessionBinding(ctx context.Context, db *sql.DB, worktree string) (clientBinding, []byte, error) {
	var binding clientBinding
	var token []byte
	err := db.QueryRowContext(ctx, `SELECT server_url, library_id, worktree, user_id, device_id, sync_base_commit,
		sync_base_root, head_etag, access_token FROM bindings WHERE worktree = ?`, worktree).Scan(
		&binding.ServerURL, &binding.LibraryID, &binding.Worktree, &binding.UserID, &binding.DeviceID,
		&binding.SyncBase, &binding.SyncBaseRoot, &binding.HeadETag, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, nil, errors.New("worktree is not bound")
	}
	if err != nil {
		return binding, nil, fmt.Errorf("read client binding: %w", err)
	}
	return binding, token, nil
}

// Planning reads the same worktree and consumes the same replay budget as Advance.
func (s *_lockedBoundWorktreeSession) acquire(ctx context.Context) (func(), error) {
	if s == nil {
		return nil, _errPublicationSessionClosed
	}
	release, err := s.beginAdvance()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if err := s.validate(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (s *_lockedBoundWorktreeSession) beginAdvance() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, _errPublicationSessionClosed
	}
	if s.active {
		return nil, _errPublicationSessionConcurrentUse
	}
	s.active = true
	return func() {
		s.mu.Lock()
		s.active = false
		s.mu.Unlock()
	}, nil
}

func (s *_lockedBoundWorktreeSession) validate(ctx context.Context) error {
	if s.locks == nil || len(s.lockNames) == 0 {
		return _errPublicationSessionLockMismatch
	}
	if err := s.locks.validate(s.lockNames); err != nil {
		return fmt.Errorf("%w: %v", _errPublicationSessionLockMismatch, err)
	}
	if s.root == nil {
		return _errPublicationSessionWorktreeMismatch
	}
	if err := s.root.validateIdentity(); err != nil {
		return fmt.Errorf("%w: %v", _errPublicationSessionWorktreeMismatch, err)
	}
	if s.db == nil || s.base == nil {
		return _errPublicationSessionBindingMismatch
	}
	binding, token, err := readLockedSessionBinding(ctx, s.db, s.binding.Worktree)
	if err != nil {
		return fmt.Errorf("%w: %v", _errPublicationSessionBindingMismatch, err)
	}
	defer clear(token)
	if binding != s.binding || !bytes.Equal(token, s.token) || s.root.path != s.binding.Worktree ||
		s.base.String() != s.binding.ServerURL {
		return _errPublicationSessionBindingMismatch
	}
	return nil
}

// Close releases the Worktree, database, credentials, and lock capability held by the session.
func (s *_lockedBoundWorktreeSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return _errPublicationSessionConcurrentUse
	}
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.challenge = nil
	s.mu.Unlock()

	var err error
	if s.root != nil {
		err = errors.Join(err, s.root.Close())
		s.root = nil
	}
	if s.db != nil {
		err = errors.Join(err, s.db.Close())
		s.db = nil
	}
	clear(s.token)
	s.token = nil
	if s.ownsLocks {
		err = errors.Join(err, s.locks.Close())
	}
	s.locks = nil
	return err
}

func (s *_lockedBoundWorktreeSession) bindOptions() bindOptions {
	return bindOptions{clientDir: s.clientDir, serverURL: s.binding.ServerURL, libraryID: s.binding.LibraryID,
		worktree: s.binding.Worktree, deviceID: s.binding.DeviceID, base: s.base, token: s.token,
		worktreeRoot: s.root, fallbackOccupied: s.config.fallbackOccupied}
}

func (s *_lockedBoundWorktreeSession) loadScanConfig(ctx context.Context, warning io.Writer) (worktreeScanConfig, error) {
	tracked, err := loadTrackedPaths(ctx, s.db, s.binding.Worktree)
	if err != nil {
		return worktreeScanConfig{}, err
	}
	if len(tracked) == 0 {
		tracked, err = loadRemoteTrackedPaths(ctx, s.bindOptions(), s.binding)
		if err != nil {
			return worktreeScanConfig{}, err
		}
	}
	return worktreeScanConfig{trackedPaths: tracked, warning: warning, fault: s.config.scanFault, ignoreUntrackedUnsupported: true}, nil
}

func clonePublicationScanConfig(config worktreeScanConfig) worktreeScanConfig {
	config.trackedPaths = maps.Clone(config.trackedPaths)
	config.ignoredRootNames = maps.Clone(config.ignoredRootNames)
	config.ignoredPaths = maps.Clone(config.ignoredPaths)
	return config
}

func headBeforePublication(ctx context.Context, options bindOptions, config libraryClientConfig, candidate _publicationCandidate) (remoteHead, error) {
	if config.beforeHeadCAS != nil {
		if err := config.beforeHeadCAS(); err != nil {
			return remoteHead{}, fmt.Errorf("prepare Head publication: %w", err)
		}
	}
	verified, err := scanWorktreeWithConfig(options.worktreeRoot, options.scanConfig)
	if err != nil || verified.root != candidate.CapturedRoot {
		return remoteHead{}, errors.New("worktree changed before Head publication")
	}
	current, err := getRemoteHead(ctx, options.base, options.libraryID, options.token)
	if err != nil {
		return remoteHead{}, err
	}
	if current.CommitID == nil {
		return remoteHead{}, errors.New("library Head is empty before publication")
	}
	return current, nil
}

func headAfterPublication(ctx context.Context, options bindOptions, publishErr error) (remoteHead, worktreeSnapshot, error) {
	head, err := getRemoteHead(ctx, options.base, options.libraryID, options.token)
	if err != nil {
		return remoteHead{}, worktreeSnapshot{}, errors.Join(publishErr, fmt.Errorf("resolve library Head after publish: %w", err))
	}
	snapshot, err := scanWorktreeWithConfig(options.worktreeRoot, options.scanConfig)
	if err != nil {
		return remoteHead{}, worktreeSnapshot{}, errors.Join(publishErr, fmt.Errorf("rescan worktree after publish: %w", err))
	}
	return head, snapshot, nil
}
