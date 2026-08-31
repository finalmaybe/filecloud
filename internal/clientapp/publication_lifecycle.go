package clientapp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	_publicationOutcomeFinalized
	_publicationOutcomeCheckoutPending
	_publicationOutcomeRerunSync
	_publicationOutcomeRerunRestore
	_publicationOutcomeSyncReplan
)

type _publicationOutcome struct {
	kind _publicationOutcomeKind
}

func (o _publicationOutcome) Kind() _publicationOutcomeKind {
	return o.kind
}

type _publicationRequester interface {
	publicationRequest()
}

type _syncPublicationRequest struct{}

func (r _syncPublicationRequest) publicationRequest() {}

type _restorePublicationRequest struct{}

func (r _restorePublicationRequest) publicationRequest() {}

type _publicationLifecycle struct{}

func (m *_publicationLifecycle) Advance(ctx context.Context, s *_lockedBoundWorktreeSession,
	request _publicationRequester) (_publicationOutcome, error) {
	var noOutcome _publicationOutcome
	if s == nil {
		return noOutcome, _errPublicationSessionClosed
	}
	release, err := s.beginAdvance()
	if err != nil {
		return noOutcome, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return noOutcome, err
	}
	if err := s.validate(ctx); err != nil {
		return noOutcome, err
	}
	switch request.(type) {
	case _syncPublicationRequest, _restorePublicationRequest:
		return _publicationOutcome{kind: _publicationOutcomeIdle}, nil
	default:
		return noOutcome, _errPublicationRequestInvalid
	}
}

type _lockedBoundWorktreeSession struct {
	mu sync.Mutex

	active bool
	closed bool

	locks     *clientLocks
	lockNames []string
	db        *sql.DB
	binding   clientBinding
	token     []byte
	base      *url.URL
	root      *openedWorktree
}

func openLockedBoundWorktreeSession(ctx context.Context, clientDir, worktree string,
	config libraryClientConfig) (session *_lockedBoundWorktreeSession, retErr error) {
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
	locks, err := tryLockUnbind(ctx, canonicalClientDir, databasePath, canonicalWorktree, config)
	if err != nil {
		return nil, err
	}
	session = &_lockedBoundWorktreeSession{locks: locks}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, session.Close())
			session = nil
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
		return clientBinding{}, nil, errors.New("worktree is not bound")
	}
	if err != nil {
		return clientBinding{}, nil, fmt.Errorf("read client binding: %w", err)
	}
	return binding, token, nil
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
	if s.locks != nil {
		err = errors.Join(err, s.locks.Close())
		s.locks = nil
	}
	return err
}
