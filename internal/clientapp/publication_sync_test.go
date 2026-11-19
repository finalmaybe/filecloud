package clientapp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicationLifecycleSyncConfirmsAndResumesOnlyMissingUploads(t *testing.T) {
	state := newImportedBinding(t)
	db, err := openClientDB(filepath.Join(state.clientDir, _clientDatabaseName), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	original := readTestBinding(t, state.clientDir, state.worktree)
	var snapshotPUTs, commitPUTs, headPUTs atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			state.environment.handler.ServeHTTP(w, request)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/head") {
			headPUTs.Add(1)
			state.environment.handler.ServeHTTP(w, request)
			return
		}
		pending, err := loadPendingPublication(request.Context(), db, original)
		if syncState, ok := pending.(_syncPublicationState); err != nil || !ok || !syncState.DeleteConfirmed {
			t.Errorf("upload started before durable confirmation: pending=%T err=%v", pending, err)
			http.Error(w, "missing confirmation", http.StatusInternalServerError)
			return
		}
		if strings.Contains(request.URL.Path, "/objects/commits/") {
			if commitPUTs.Add(1) == 2 {
				// This attempt persists the Commit but the client only sees a failed upload.
				response := httptest.NewRecorder()
				state.environment.handler.ServeHTTP(response, request)
				if response.Code != http.StatusCreated && response.Code != http.StatusOK {
					t.Errorf("store commit: status %d", response.Code)
				}
			}
			http.Error(w, "injected upload failure", http.StatusServiceUnavailable)
			return
		}
		snapshotPUTs.Add(1)
		state.environment.handler.ServeHTTP(w, request)
	}))
	t.Cleanup(proxy.Close)
	updateRestoreTestServer(t, state, proxy.URL)
	if err := os.Remove(filepath.Join(state.worktree, "local")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.worktree, "replacement"), []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := openTestPublicationSession(t, state)
	start := prepareTestSyncPublication(t, session)
	id := start.prepared.candidate.CandidateCommit
	prefix := id[:deleteCandidatePrefixLen]
	outcome, err := advanceTestPublication(t, session, start)
	if required, ok := errors.AsType[*deleteConfirmationRequiredError](err); !ok || required.candidate != id ||
		outcome.Kind() != _publicationOutcomeConfirmationRequired {
		t.Fatalf("start: outcome=%v err=%v", outcome.Kind(), err)
	}
	if pending := readTestSyncPublication(t, state.clientDir, state.worktree); pending.DeleteConfirmed || pending.CandidateCommit != id {
		t.Fatalf("start persisted the wrong candidate")
	}

	resume := _resumePublicationRequest{round: start.round}
	resume.round.confirmDeleteSet, resume.round.confirmDelete = true, id
	if _, err := advanceTestPublication(t, session, resume); !errors.Is(err, _errWrongDeletePrefix) {
		t.Fatalf("full-length confirmation: %v", err)
	}
	if snapshotPUTs.Load()+commitPUTs.Load()+headPUTs.Load() != 0 {
		t.Fatal("unconfirmed deletion made a remote write")
	}

	resume.round.confirmDelete = prefix
	if _, err := advanceTestPublication(t, session, resume); err == nil || !strings.Contains(err.Error(), "Service Unavailable") {
		t.Fatalf("confirm with incomplete upload: %v", err)
	}
	if pending := readTestSyncPublication(t, state.clientDir, state.worktree); !pending.DeleteConfirmed || pending.CandidateCommit != id {
		t.Fatal("upload failure lost durable confirmation")
	}
	written := snapshotPUTs.Load()
	if written == 0 || commitPUTs.Load() != 1 || headPUTs.Load() != 0 {
		t.Fatalf("first upload: snapshot=%d commit=%d head=%d", written, commitPUTs.Load(), headPUTs.Load())
	}
	resume.round.confirmDeleteSet = false
	if _, err := advanceTestPublication(t, session, resume); err == nil || !strings.Contains(err.Error(), "Service Unavailable") {
		t.Fatalf("resume with lost commit response: %v", err)
	}
	if snapshotPUTs.Load() != written || commitPUTs.Load() != 2 || headPUTs.Load() != 0 {
		t.Fatal("resume uploaded an already-present snapshot object or attempted Head CAS")
	}
	outcome, err = advanceTestPublication(t, session, resume)
	if err != nil || outcome.Kind() != _publicationOutcomeFinalized || snapshotPUTs.Load() != written || commitPUTs.Load() != 2 || headPUTs.Load() != 1 {
		t.Fatalf("finalized resume: outcome=%v err=%v snapshot=%d commit=%d head=%d",
			outcome.Kind(), err, snapshotPUTs.Load(), commitPUTs.Load(), headPUTs.Load())
	}
	assertTestSyncPublicationFinalized(t, state, id)
}

func TestPublicationLifecycleSyncReconcilesCASWithAuthoritativeHead(t *testing.T) {
	for _, scenario := range []string{"success", "transient retry", "lost response", "unchanged Head"} {
		t.Run(scenario, func(t *testing.T) {
			state := newImportedBinding(t)
			var headPUTs, headsAfterCAS atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if !strings.HasSuffix(request.URL.Path, "/head") {
					state.environment.handler.ServeHTTP(w, request)
					return
				}
				if request.Method == http.MethodGet && headPUTs.Load() > 0 {
					headsAfterCAS.Add(1)
				}
				if request.Method == http.MethodPut {
					attempt := headPUTs.Add(1)
					switch scenario {
					case "transient retry":
						if attempt == 1 {
							w.Header().Set("Retry-After", "0")
							http.Error(w, "injected retry", http.StatusServiceUnavailable)
							return
						}
					case "lost response":
						response := httptest.NewRecorder()
						state.environment.handler.ServeHTTP(response, request)
						if response.Code != http.StatusOK {
							t.Errorf("publish Head: status %d", response.Code)
						}
						_, _ = io.WriteString(w, "{")
						return
					case "unchanged Head":
						_, _ = io.WriteString(w, `{"Head":{}}`)
						return
					}
				}
				state.environment.handler.ServeHTTP(w, request)
			}))
			t.Cleanup(proxy.Close)
			updateRestoreTestServer(t, state, proxy.URL)
			if err := os.WriteFile(filepath.Join(state.worktree, "new"), []byte("new bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			session := openTestPublicationSession(t, state)
			start := prepareTestSyncPublication(t, session)
			id := start.prepared.candidate.CandidateCommit
			outcome, err := advanceTestPublication(t, session, start)
			wantPUTs := int32(1)
			if scenario == "transient retry" {
				wantPUTs++
			}
			if headPUTs.Load() != wantPUTs || headsAfterCAS.Load() != 1 {
				t.Fatalf("Head requests: PUT=%d want %d, authoritative GET=%d want 1", headPUTs.Load(), wantPUTs, headsAfterCAS.Load())
			}
			if scenario == "unchanged Head" {
				if err == nil || outcome.Kind() != _publicationOutcomeRerunSync ||
					readTestBinding(t, state.clientDir, state.worktree) != session.binding ||
					readTestSyncPublication(t, state.clientDir, state.worktree).CandidateCommit != id {
					t.Fatalf("unchanged Head: outcome=%v err=%v", outcome.Kind(), err)
				}
				return
			}
			if err != nil || outcome.Kind() != _publicationOutcomeFinalized {
				t.Fatalf("published Head: outcome=%v err=%v", outcome.Kind(), err)
			}
			assertTestSyncPublicationFinalized(t, state, id)
		})
	}
}

func TestPublicationLifecycleSyncDoesNotConfirmChangedWorktree(t *testing.T) {
	state := newImportedBinding(t)
	beforeHead := restoreTestHead(t, state)
	if err := os.Remove(filepath.Join(state.worktree, "local")); err != nil {
		t.Fatal(err)
	}
	session := openTestPublicationSession(t, state)
	start := prepareTestSyncPublication(t, session)
	outcome, err := advanceTestPublication(t, session, start)
	if outcome.Kind() != _publicationOutcomeConfirmationRequired || err == nil {
		t.Fatalf("start protected deletion: outcome=%v err=%v", outcome.Kind(), err)
	}
	if err := os.WriteFile(filepath.Join(state.worktree, "changed"), []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	resume := _resumePublicationRequest{round: start.round}
	resume.round.confirmDeleteSet = true
	resume.round.confirmDelete = start.prepared.candidate.CandidateCommit[:deleteCandidatePrefixLen]
	outcome, err = advanceTestPublication(t, session, resume)
	if outcome.Kind() != _publicationOutcomeRerunSync || err == nil || !strings.Contains(err.Error(), "stale candidate discarded") {
		t.Fatalf("confirm changed worktree: outcome=%v err=%v", outcome.Kind(), err)
	}
	if countClientRows(t, state.clientDir, "pending_publications", state.worktree) != 0 ||
		readTestBinding(t, state.clientDir, state.worktree) != session.binding || restoreTestHead(t, state) != beforeHead {
		t.Fatal("stale candidate changed the binding or Head, or was retained")
	}
}

func TestPublicationLifecycleSyncRejectsPreparedCandidateFromAnotherSession(t *testing.T) {
	state := newImportedBinding(t)
	if err := os.WriteFile(filepath.Join(state.worktree, "new"), []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := openTestPublicationSession(t, state)
	start := prepareTestSyncPublication(t, original)
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	session := openTestPublicationSession(t, state)
	if _, err := advanceTestPublication(t, session, start); !errors.Is(err, _errPublicationRequestInvalid) {
		t.Fatalf("start in another session: %v", err)
	}
	if countClientRows(t, state.clientDir, "pending_publications", state.worktree) != 0 ||
		readTestBinding(t, state.clientDir, state.worktree) != session.binding {
		t.Fatal("rejected candidate mutated its binding or persisted state")
	}
}

func prepareTestSyncPublication(t *testing.T, session *_lockedBoundWorktreeSession) _startSyncPublicationRequest {
	t.Helper()
	tracked, err := loadTrackedPaths(t.Context(), session.db, session.binding.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	scanConfig := worktreeScanConfig{trackedPaths: tracked}
	snapshot, err := scanWorktreeWithConfig(session.root, scanConfig)
	if err != nil {
		t.Fatal(err)
	}
	head, err := getRemoteHead(t.Context(), session.base, session.binding.LibraryID, session.token)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareDirectSyncPublication(session, tracked, snapshot, head)
	if err != nil {
		t.Fatal(err)
	}
	return _startSyncPublicationRequest{prepared: prepared,
		round: _syncPublicationRoundOptions{scanConfig: scanConfig, budget: _newReplayBudget()}}
}

func assertTestSyncPublicationFinalized(t *testing.T, state importedBinding, id string) {
	t.Helper()
	if binding := assertTestConverged(t, state.environment, state.clientDir, state.worktree); binding.SyncBase != id {
		t.Fatalf("published %s instead of prepared candidate %s", binding.SyncBase, id)
	}
	if countClientRows(t, state.clientDir, "pending_publications", state.worktree) != 0 ||
		countClientRows(t, state.clientDir, "pending_checkouts", state.worktree) != 0 {
		t.Fatal("direct publication left pending state")
	}
}
