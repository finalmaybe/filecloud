package clientapp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

func TestPublicationLifecycleRestorePreviewAndOfflineReplay(t *testing.T) {
	state, request := newRestoreLifecycleFixture(t)
	var requests, writes atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if req.Method == http.MethodPut {
			writes.Add(1)
		}
		state.environment.handler.ServeHTTP(w, req)
	}))
	t.Cleanup(proxy.Close)
	updateRestoreTestServer(t, state, proxy.URL)
	session := openTestPublicationSession(t, state)
	preview := advanceTestRestorePreview(t, session, request)
	if preview.sourceCommit != request.sourceCommit || preview.sourcePath != request.sourcePath ||
		preview.expectedHead != session.binding.SyncBase || preview.updatedCount != 1 ||
		preview.preservedCurrentOnlyCount != 1 || preview.changedPathCount != 1 ||
		!slices.Equal(preview.changedPaths, []string{"local"}) || preview.truncated || writes.Load() != 0 {
		t.Fatalf("unexpected preview: %+v (PUTs=%d)", preview, writes.Load())
	}
	inspected, err := advanceTestPublication(t, session, _inspectPublicationRequest{})
	pending, ok := inspected.pending.(_restorePublicationState)
	if err != nil || !ok || pending.RestoreConfirmed || pending.CandidateCommit != preview.candidateCommit {
		t.Fatalf("preview did not persist its unconfirmed candidate: kind=%v err=%v", inspected.Kind(), err)
	}
	candidate, err := object.VerifyCommit(pending.CandidateData, preview.candidateCommit)
	if err != nil || !slices.Equal(candidate.Parents, []string{preview.expectedHead}) || candidate.Root != pending.CandidateRoot {
		t.Fatalf("preview commit is not a canonical single-parent forward commit: %+v err=%v", candidate, err)
	}

	// An empty path index would make scan setup fetch legacy tracked paths from the remote.
	if _, err := session.db.ExecContext(t.Context(), "DELETE FROM path_index WHERE worktree = ?", state.worktree); err != nil {
		t.Fatal(err)
	}
	before := captureRestoreConfirmationTestState(t, state)
	session.config.scanFault = func(scanFault) error { return errors.New("unexpected replay scan") }
	calls := requests.Load()
	replayed := advanceTestRestorePreview(t, session, request)
	if !reflect.DeepEqual(replayed, preview) {
		t.Fatalf("replay changed preview: got %+v, want %+v", replayed, preview)
	}
	replayed.changedPaths[0] = "caller edit"
	if again := advanceTestRestorePreview(t, session, request); !reflect.DeepEqual(again, preview) {
		t.Fatalf("caller changed durable preview: got %+v, want %+v", again, preview)
	}
	wrongPrefix := "0" + preview.confirmationPrefix()[1:]
	if wrongPrefix == preview.confirmationPrefix() {
		wrongPrefix = "1" + wrongPrefix[1:]
	}
	for _, prefix := range []string{wrongPrefix, preview.candidateCommit} {
		outcome, err := advanceTestPublication(t, session, _confirmRestorePublicationRequest{prefix: prefix})
		if outcome.Kind() != _publicationOutcomeConfirmationRequired || !errors.Is(err, _errWrongRestorePrefix) {
			t.Fatalf("wrong confirmation: outcome=%v err=%v", outcome.Kind(), err)
		}
	}
	if requests.Load() != calls {
		t.Fatalf("replay or wrong confirmation made %d unexpected HTTP requests", requests.Load()-calls)
	}
	after, err := advanceTestPublication(t, session, _inspectPublicationRequest{})
	if err != nil || !reflect.DeepEqual(after.pending, pending) {
		t.Fatalf("replay or wrong confirmation changed the candidate: kind=%v err=%v", after.Kind(), err)
	}
	assertRestoreConfirmationTestState(t, "replay and wrong confirmation", state, before)
}

func TestPublicationLifecycleRestorePreviewRequiresConvergenceAndSkipsNoOp(t *testing.T) {
	for _, scenario := range []string{"no-op", "dirty worktree", "advanced Head"} {
		t.Run(scenario, func(t *testing.T) {
			state := newImportedBinding(t)
			request := _previewRestorePublicationRequest{sourceCommit: state.binding.SyncBase, sourcePath: "."}
			switch scenario {
			case "dirty worktree":
				if err := os.WriteFile(filepath.Join(state.worktree, "local"), []byte("unsynced"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "advanced Head":
				publishSameRootSuccessor(t, state, time.Date(2026, 8, 9, 5, 6, 7, 0, time.UTC))
			}
			before := captureRestoreConfirmationTestState(t, state)
			session := openTestPublicationSession(t, state)
			outcome, err := advanceTestPublication(t, session, request)
			if scenario == "no-op" {
				if err != nil || outcome.Kind() != _publicationOutcomeRestoreNoOp {
					t.Fatalf("no-op preview: outcome=%v err=%v", outcome.Kind(), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "worktree is not converged with Sync Base and Head") {
				t.Fatalf("divergent preview: outcome=%v err=%v", outcome.Kind(), err)
			}
			assertRestoreConfirmationTestState(t, scenario, state, before)
		})
	}
}

func TestPublicationLifecycleRestoreRevalidatesBeforeConfirming(t *testing.T) {
	state, request := newRestoreLifecycleFixture(t)
	db, err := openClientDB(filepath.Join(state.clientDir, _clientDatabaseName), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	binding := readTestBinding(t, state.clientDir, state.worktree)
	var changeRow, headRead atomic.Bool
	var writes atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/head") {
			headRead.Store(true)
		}
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/objects/commits/"+binding.SyncBase) && changeRow.CompareAndSwap(true, false) {
			if _, err := db.ExecContext(req.Context(), `UPDATE pending_publications SET preserved_current_only_count =
				preserved_current_only_count + 1 WHERE worktree = ?`, state.worktree); err != nil {
				t.Errorf("change preview evidence: %v", err)
			}
		}
		if req.Method == http.MethodPut {
			writes.Add(1)
		}
		state.environment.handler.ServeHTTP(w, req)
	}))
	t.Cleanup(proxy.Close)
	updateRestoreTestServer(t, state, proxy.URL)
	session := openTestPublicationSession(t, state)
	preview := advanceTestRestorePreview(t, session, request)
	headRead.Store(false)
	session.config.scanFault = func(scanFault) error {
		if !headRead.Load() {
			return errors.New("scanned before reading Head")
		}
		return nil
	}
	changeRow.Store(true)
	confirm := _confirmRestorePublicationRequest{prefix: preview.confirmationPrefix()}
	if _, err := advanceTestPublication(t, session, confirm); err == nil || !strings.Contains(err.Error(), "restore candidate changed before confirmation") {
		t.Fatalf("confirmation did not compare the full preview row: %v", err)
	}
	outcome, err := advanceTestPublication(t, session, confirm)
	if outcome.Kind() != _publicationOutcomeRerunRestore || err == nil || !strings.Contains(err.Error(), "stale restore candidate discarded") || writes.Load() != 0 {
		t.Fatalf("confirmation did not reject stale statistics: outcome=%v err=%v PUTs=%d", outcome.Kind(), err, writes.Load())
	}
	if after, err := advanceTestPublication(t, session, _inspectPublicationRequest{}); err != nil || after.Kind() != _publicationOutcomeIdle {
		t.Fatalf("stale candidate was not discarded: outcome=%v err=%v", after.Kind(), err)
	}
}

func TestPublicationLifecycleRestoreResumesUploadsAndHandsOffSingleCAS(t *testing.T) {
	state, request := newRestoreLifecycleFixture(t)
	db, err := openClientDB(filepath.Join(state.clientDir, _clientDatabaseName), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	binding := readTestBinding(t, state.clientDir, state.worktree)
	var directoryPUTs, commitPUTs, headPUTs atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPut {
			pending, err := loadPendingPublication(req.Context(), db, binding)
			if restore, ok := pending.(_restorePublicationState); err != nil || !ok || !restore.RestoreConfirmed {
				t.Errorf("PUT %s started before durable confirmation: pending=%T err=%v", req.URL.Path, pending, err)
			}
			switch {
			case strings.Contains(req.URL.Path, "/objects/directories/"):
				directoryPUTs.Add(1)
			case strings.Contains(req.URL.Path, "/objects/commits/"):
				if commitPUTs.Add(1) == 1 {
					http.Error(w, "injected upload failure", http.StatusServiceUnavailable)
					return
				}
			case strings.HasSuffix(req.URL.Path, "/head"):
				if headPUTs.Add(1) == 1 {
					w.Header().Set("Retry-After", "0")
					http.Error(w, "injected CAS failure", http.StatusServiceUnavailable)
					return
				}
			default:
				t.Errorf("unexpected restore PUT: %s", req.URL.Path)
			}
		}
		state.environment.handler.ServeHTTP(w, req)
	}))
	t.Cleanup(proxy.Close)
	updateRestoreTestServer(t, state, proxy.URL)
	session := openTestPublicationSession(t, state)
	preview := advanceTestRestorePreview(t, session, request)
	before := captureRestoreConfirmationTestState(t, state)
	confirm := _confirmRestorePublicationRequest{prefix: preview.confirmationPrefix()}
	if _, err := advanceTestPublication(t, session, confirm); err == nil || !strings.Contains(err.Error(), "Service Unavailable") ||
		directoryPUTs.Load() == 0 || commitPUTs.Load() != 1 || headPUTs.Load() != 0 {
		t.Fatalf("failed upload: directories=%d candidate=%d Head=%d err=%v", directoryPUTs.Load(), commitPUTs.Load(), headPUTs.Load(), err)
	}
	assertRestoreConfirmationTestState(t, "upload failure", state, before)
	dirs := directoryPUTs.Load()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session = openTestPublicationSession(t, state)
	inspected, err := advanceTestPublication(t, session, _inspectPublicationRequest{})
	pending, ok := inspected.pending.(_restorePublicationState)
	if err != nil || !ok || !pending.RestoreConfirmed || pending.CandidateCommit != preview.candidateCommit {
		t.Fatalf("upload failure did not preserve exact confirmation: outcome=%v err=%v", inspected.Kind(), err)
	}
	scanConfig, err := session.loadScanConfig(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resume := _resumeRestorePublicationRequest{scanConfig: scanConfig}
	if _, err := advanceTestPublication(t, session, resume); err == nil || !strings.Contains(err.Error(), "failed after 1 attempts") ||
		directoryPUTs.Load() != dirs || commitPUTs.Load() != 2 || headPUTs.Load() != 1 {
		t.Fatalf("first CAS: directories=%d candidate=%d Head=%d err=%v", directoryPUTs.Load(), commitPUTs.Load(), headPUTs.Load(), err)
	}
	assertRestoreConfirmationTestState(t, "single CAS failure", state, before)
	outcome, err := advanceTestPublication(t, session, confirm)
	if err != nil || outcome.Kind() != _publicationOutcomeCheckoutPending || outcome.checkout.TargetCommit != preview.candidateCommit ||
		directoryPUTs.Load() != dirs || commitPUTs.Load() != 2 || headPUTs.Load() != 2 {
		t.Fatalf("successful CAS: outcome=%v directories=%d candidate=%d Head=%d err=%v", outcome.Kind(), directoryPUTs.Load(), commitPUTs.Load(), headPUTs.Load(), err)
	}
	before.head, before.pendingRows = preview.candidateCommit+"\x00"+outcome.checkout.HeadETag, 0
	assertRestoreConfirmationTestState(t, "durable handoff", state, before)
	if checkout, err := advanceTestPublication(t, session, _inspectPublicationRequest{}); err != nil ||
		checkout.Kind() != _publicationOutcomeCheckoutPending || !reflect.DeepEqual(checkout.checkout, outcome.checkout) {
		t.Fatalf("checkout handoff was not persisted: outcome=%v err=%v", checkout.Kind(), err)
	}
}

func TestPublicationLifecycleRestoreRollsBackHandoffIfPublicationCannotBeDeleted(t *testing.T) {
	state, request := newRestoreLifecycleFixture(t)
	session := openTestPublicationSession(t, state)
	preview := advanceTestRestorePreview(t, session, request)
	before := captureRestoreConfirmationTestState(t, state)
	if _, err := session.db.ExecContext(t.Context(), `CREATE TEMP TRIGGER interrupt_handoff
		BEFORE DELETE ON main.pending_publications BEGIN SELECT RAISE(ABORT, 'interrupt handoff'); END`); err != nil {
		t.Fatal(err)
	}
	confirm := _confirmRestorePublicationRequest{prefix: preview.confirmationPrefix()}
	if _, err := advanceTestPublication(t, session, confirm); err == nil || !strings.Contains(err.Error(), "interrupt handoff") {
		t.Fatalf("expected handoff deletion to fail: %v", err)
	}
	inspected, err := advanceTestPublication(t, session, _inspectPublicationRequest{})
	pending, ok := inspected.pending.(_restorePublicationState)
	if err != nil || inspected.Kind() != _publicationOutcomePending || !ok || !pending.RestoreConfirmed || pending.CandidateCommit != preview.candidateCommit {
		t.Fatalf("failed handoff did not retain only the confirmed Publication: outcome=%v err=%v", inspected.Kind(), err)
	}
	commit, _, _ := strings.Cut(restoreTestHead(t, state), "\x00")
	if commit != preview.candidateCommit {
		t.Fatalf("Head=%s, want published candidate %s", commit, preview.candidateCommit)
	}
	before.head = restoreTestHead(t, state)
	assertRestoreConfirmationTestState(t, "failed handoff", state, before)
}

func newRestoreLifecycleFixture(t *testing.T) (importedBinding, _previewRestorePublicationRequest) {
	t.Helper()
	state := newImportedBinding(t)
	for name, content := range map[string]string{"local": "current", "keep": "current only"} {
		if err := os.WriteFile(filepath.Join(state.worktree, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runRestoreTestSync(t, state.clientDir, state.worktree)
	return state, _previewRestorePublicationRequest{sourceCommit: state.binding.SyncBase, sourcePath: "."}
}

func advanceTestRestorePreview(t *testing.T, session *_lockedBoundWorktreeSession, request _previewRestorePublicationRequest) restorePreview {
	t.Helper()
	outcome, err := advanceTestPublication(t, session, request)
	if err != nil || outcome.Kind() != _publicationOutcomePreviewReady {
		t.Fatalf("restore preview: outcome=%v err=%v", outcome.Kind(), err)
	}
	return *outcome.restorePreview
}
