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

func (outcome _publicationOutcome) Kind() _publicationOutcomeKind {
	return outcome.kind
}

type _publicationRequest interface {
	publicationRequest()
}

type _syncPublicationRequest struct{}

func (_syncPublicationRequest) publicationRequest() {}

type _restorePublicationRequest struct{}

func (_restorePublicationRequest) publicationRequest() {}

type _publicationLifecycle struct{}

func (*_publicationLifecycle) Advance(ctx context.Context, session *_lockedBoundWorktreeSession,
	request _publicationRequest) (_publicationOutcome, error) {
	if session == nil {
		return _publicationOutcome{}, _errPublicationSessionClosed
	}
	release, err := session.beginAdvance()
	if err != nil {
		return _publicationOutcome{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return _publicationOutcome{}, err
	}
	if err := session.validate(ctx); err != nil {
		return _publicationOutcome{}, err
	}
	switch request.(type) {
	case _syncPublicationRequest, _restorePublicationRequest:
		return _publicationOutcome{kind: _publicationOutcomeIdle}, nil
	default:
		return _publicationOutcome{}, _errPublicationRequestInvalid
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

func (session *_lockedBoundWorktreeSession) beginAdvance() (func(), error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, _errPublicationSessionClosed
	}
	if session.active {
		return nil, _errPublicationSessionConcurrentUse
	}
	session.active = true
	return func() {
		session.mu.Lock()
		session.active = false
		session.mu.Unlock()
	}, nil
}

func (session *_lockedBoundWorktreeSession) validate(ctx context.Context) error {
	if session.locks == nil || len(session.lockNames) == 0 {
		return _errPublicationSessionLockMismatch
	}
	if err := session.locks.validate(session.lockNames); err != nil {
		return fmt.Errorf("%w: %v", _errPublicationSessionLockMismatch, err)
	}
	if session.root == nil {
		return _errPublicationSessionWorktreeMismatch
	}
	if err := session.root.validateIdentity(); err != nil {
		return fmt.Errorf("%w: %v", _errPublicationSessionWorktreeMismatch, err)
	}
	if session.db == nil || session.base == nil {
		return _errPublicationSessionBindingMismatch
	}
	binding, token, err := readLockedSessionBinding(ctx, session.db, session.binding.Worktree)
	if err != nil {
		return fmt.Errorf("%w: %v", _errPublicationSessionBindingMismatch, err)
	}
	defer clear(token)
	if binding != session.binding || !bytes.Equal(token, session.token) || session.root.path != session.binding.Worktree ||
		session.base.String() != session.binding.ServerURL {
		return _errPublicationSessionBindingMismatch
	}
	return nil
}

func (session *_lockedBoundWorktreeSession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return _errPublicationSessionConcurrentUse
	}
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	session.mu.Unlock()

	var err error
	if session.root != nil {
		err = errors.Join(err, session.root.Close())
		session.root = nil
	}
	if session.db != nil {
		err = errors.Join(err, session.db.Close())
		session.db = nil
	}
	clear(session.token)
	session.token = nil
	if session.locks != nil {
		err = errors.Join(err, session.locks.Close())
		session.locks = nil
	}
	return err
}
