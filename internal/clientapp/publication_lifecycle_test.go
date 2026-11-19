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
	var module _publicationLifecycle
	outcome, err := module.Advance(t.Context(), session, _inspectPublicationRequest{})
	if err != nil || outcome.Kind() != _publicationOutcomeIdle {
		t.Fatalf("Advance() outcome = %v, error = %v; want idle", outcome.Kind(), err)
	}
}

func TestPublicationLifecycleAdvanceRejectsUnbootstrappedSession(t *testing.T) {
	var session _lockedBoundWorktreeSession
	_, err := advanceTestSyncPublication(t, &session)
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
				updateTestPublicationBinding(t, session, "UPDATE bindings SET head_etag = 'changed' WHERE worktree = ?")
			},
			want: _errPublicationSessionBindingMismatch,
		},
		{
			name: "credential changed",
			mutate: func(t *testing.T, session *_lockedBoundWorktreeSession) {
				updateTestPublicationBinding(t, session, "UPDATE bindings SET access_token = x'00' WHERE worktree = ?")
			},
			want: _errPublicationSessionBindingMismatch,
		},
		{
			name: "remote location changed",
			mutate: func(t *testing.T, session *_lockedBoundWorktreeSession) {
				updateTestPublicationBinding(t, session, "UPDATE bindings SET server_url = 'https://changed.invalid' WHERE worktree = ?")
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

			_, err := advanceTestSyncPublication(t, session)
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

	_, err := advanceTestSyncPublication(t, session)
	if !errors.Is(err, _errPublicationSessionLockMismatch) {
		t.Fatalf("Advance() error = %v, want classification %v", err, _errPublicationSessionLockMismatch)
	}
}

func TestPublicationLifecycleAdvanceRejectsReleasedLockHeldByCompetitor(t *testing.T) {
	state := newImportedBinding(t)
	session := openTestPublicationSession(t, state)
	file := session.locks.files[0]
	if err := fscompat.Flock(int(file.Fd()), fscompat.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	competitor, err := os.OpenFile(file.Name(), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := fscompat.Flock(int(competitor.Fd()), fscompat.LOCK_EX|fscompat.LOCK_NB); err != nil {
		t.Fatal(errors.Join(err, competitor.Close()))
	}
	defer func() {
		if err := fscompat.Flock(int(competitor.Fd()), fscompat.LOCK_UN); err != nil {
			t.Errorf("release competing client lock: %v", err)
		}
		if err := competitor.Close(); err != nil {
			t.Errorf("close competing client lock: %v", err)
		}
		if err := fscompat.Flock(int(file.Fd()), fscompat.LOCK_EX|fscompat.LOCK_NB); err != nil {
			t.Errorf("restore client lock for cleanup: %v", err)
		}
	}()

	_, err = advanceTestSyncPublication(t, session)
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

		_, err := advanceTestSyncPublication(t, session)
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

		_, err = advanceTestPublication(t, session, _confirmRestorePublicationRequest{})
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

func advanceTestSyncPublication(t *testing.T, session *_lockedBoundWorktreeSession) (_publicationOutcome, error) {
	t.Helper()
	var request _inspectPublicationRequest
	return advanceTestPublication(t, session, request)
}

func advanceTestPublication(t *testing.T, session *_lockedBoundWorktreeSession,
	request _publicationRequester) (_publicationOutcome, error) {
	t.Helper()
	var module _publicationLifecycle
	return module.Advance(t.Context(), session, request)
}

func updateTestPublicationBinding(t *testing.T, session *_lockedBoundWorktreeSession, statement string) {
	t.Helper()
	if _, err := session.db.ExecContext(t.Context(), statement, session.binding.Worktree); err != nil {
		t.Fatal(err)
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
