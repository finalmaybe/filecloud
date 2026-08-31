package clientapp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	fscompat "github.com/mingming-cn/filecloud/internal/fscompat"
)

func TestPublicationLifecycleAdvanceAcceptsLockedBoundWorktreeSession(t *testing.T) {
	state := newImportedBinding(t)
	session := openTestPublicationSession(t, state)
	module := &_publicationLifecycle{}

	for _, request := range []_publicationRequest{_syncPublicationRequest{}, _restorePublicationRequest{}} {
		outcome, err := module.Advance(t.Context(), session, request)
		if err != nil {
			t.Fatalf("Advance(%T) error = %v", request, err)
		}
		if outcome.Kind() != _publicationOutcomeIdle {
			t.Fatalf("Advance(%T) outcome = %v, want %v", request, outcome.Kind(), _publicationOutcomeIdle)
		}
	}
}

func TestPublicationLifecycleAdvanceRejectsUnbootstrappedSession(t *testing.T) {
	_, err := (&_publicationLifecycle{}).Advance(t.Context(), &_lockedBoundWorktreeSession{}, _syncPublicationRequest{})
	if !errors.Is(err, _errPublicationSessionLockMismatch) {
		t.Fatalf("Advance() error = %v, want classification %v", err, _errPublicationSessionLockMismatch)
	}
}

func TestPublicationLifecycleAdvanceRejectsChangedSessionScope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *_lockedBoundWorktreeSession)
		want   error
	}{
		{
			name: "lock capability released",
			mutate: func(t *testing.T, session *_lockedBoundWorktreeSession) {
				t.Helper()
				if err := session.locks.Close(); err != nil {
					t.Fatal(err)
				}
			},
			want: _errPublicationSessionLockMismatch,
		},
		{
			name: "binding changed",
			mutate: func(t *testing.T, session *_lockedBoundWorktreeSession) {
				t.Helper()
				if _, err := session.db.ExecContext(t.Context(), "UPDATE bindings SET head_etag = 'changed' WHERE worktree = ?", session.binding.Worktree); err != nil {
					t.Fatal(err)
				}
			},
			want: _errPublicationSessionBindingMismatch,
		},
		{
			name: "credential changed",
			mutate: func(t *testing.T, session *_lockedBoundWorktreeSession) {
				t.Helper()
				if _, err := session.db.ExecContext(t.Context(), "UPDATE bindings SET access_token = x'00' WHERE worktree = ?", session.binding.Worktree); err != nil {
					t.Fatal(err)
				}
			},
			want: _errPublicationSessionBindingMismatch,
		},
		{
			name: "remote location changed",
			mutate: func(t *testing.T, session *_lockedBoundWorktreeSession) {
				t.Helper()
				if _, err := session.db.ExecContext(t.Context(), "UPDATE bindings SET server_url = 'https://changed.invalid' WHERE worktree = ?", session.binding.Worktree); err != nil {
					t.Fatal(err)
				}
			},
			want: _errPublicationSessionBindingMismatch,
		},
		{
			name: "worktree identity changed",
			mutate: func(t *testing.T, session *_lockedBoundWorktreeSession) {
				t.Helper()
				original := session.binding.Worktree + ".original"
				if err := os.Rename(session.binding.Worktree, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(session.binding.Worktree, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: _errPublicationSessionWorktreeMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newImportedBinding(t)
			session := openTestPublicationSession(t, state)
			test.mutate(t, session)

			_, err := (&_publicationLifecycle{}).Advance(t.Context(), session, _syncPublicationRequest{})
			if !errors.Is(err, test.want) {
				t.Fatalf("Advance() error = %v, want classification %v", err, test.want)
			}
		})
	}
}

func TestPublicationLifecycleAdvanceRejectsReleasedOperatingSystemLock(t *testing.T) {
	state := newImportedBinding(t)
	session := openTestPublicationSession(t, state)
	file := session.locks.files[0]
	if err := fscompat.Flock(int(file.Fd()), fscompat.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fscompat.Flock(int(file.Fd()), fscompat.LOCK_EX|fscompat.LOCK_NB); err != nil {
			t.Errorf("restore client lock for cleanup: %v", err)
		}
	}()

	_, err := (&_publicationLifecycle{}).Advance(t.Context(), session, _syncPublicationRequest{})
	if !errors.Is(err, _errPublicationSessionLockMismatch) {
		t.Fatalf("Advance() error = %v, want classification %v", err, _errPublicationSessionLockMismatch)
	}
}

func TestPublicationLifecycleAdvanceRejectsClosedAndConcurrentSession(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		state := newImportedBinding(t)
		session := openTestPublicationSession(t, state)
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}

		_, err := (&_publicationLifecycle{}).Advance(t.Context(), session, _syncPublicationRequest{})
		if !errors.Is(err, _errPublicationSessionClosed) {
			t.Fatalf("Advance() error = %v, want classification %v", err, _errPublicationSessionClosed)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		state := newImportedBinding(t)
		session := openTestPublicationSession(t, state)
		release, err := session.beginAdvance()
		if err != nil {
			t.Fatal(err)
		}
		defer release()

		_, err = (&_publicationLifecycle{}).Advance(t.Context(), session, _restorePublicationRequest{})
		if !errors.Is(err, _errPublicationSessionConcurrentUse) {
			t.Fatalf("Advance() error = %v, want classification %v", err, _errPublicationSessionConcurrentUse)
		}
	})
}

func TestOpenLockedBoundWorktreeSessionRejectsUnboundWorktree(t *testing.T) {
	clientDir := filepath.Join(t.TempDir(), "client")
	worktree := t.TempDir()
	if err := os.Mkdir(clientDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := initializeClientDB(t.Context(), clientDir, syncDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	session, err := openLockedBoundWorktreeSession(t.Context(), clientDir, worktree, libraryClientConfig{
		checkFilesystem: func(*os.File) error { return nil },
	})
	if session != nil || err == nil || err.Error() != "worktree is not bound" {
		t.Fatalf("openLockedBoundWorktreeSession() = (%v, %v), want (nil, worktree is not bound)", session, err)
	}
}

func openTestPublicationSession(t *testing.T, state importedBinding) *_lockedBoundWorktreeSession {
	t.Helper()
	session, err := openLockedBoundWorktreeSession(t.Context(), state.clientDir, state.worktree, libraryClientConfig{
		checkFilesystem: func(*os.File) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close publication session: %v", err)
		}
	})
	return session
}
