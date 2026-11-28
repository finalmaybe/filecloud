package clientapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/syncignore"
)

func TestPublicationLifecycleSyncReplansContinuousCompetition(t *testing.T) {
	state := newImportedBinding(t)
	session, start := leaveTestSyncPending(t, state)
	captured := start.prepared.candidate.CapturedCommit
	firstHead := publishSameRootSuccessor(t, state, time.Unix(100, 0))
	outcome, err := advanceTestPublication(t, session, _resumePublicationRequest{round: start.round})
	if err != nil || outcome.Kind() != _publicationOutcomeSyncReplan {
		t.Fatalf("first conflict: kind=%v err=%v", outcome.Kind(), err)
	}
	plan, err := outcome.replan.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := readPendingCandidate(t, state.clientDir, state.worktree); got != captured {
		t.Fatal("planning replaced the observed candidate")
	}
	var secondHead string
	session.config.beforeHeadCAS = func() error {
		secondHead = publishSameRootSuccessor(t, state, time.Unix(200, 0))
		return nil
	}
	outcome, err = advanceTestPublication(t, session, plan)
	if err != nil || outcome.Kind() != _publicationOutcomeSyncReplan {
		t.Fatalf("second conflict: kind=%v err=%v", outcome.Kind(), err)
	}
	first := readTestSyncPublication(t, state.clientDir, state.worktree)
	if first.CapturedCommit != captured || first.ExpectedHead != firstHead || len(first.CandidateHistory) != 0 {
		t.Fatal("first replacement changed the captured commit or lost the observed Head")
	}
	second, err := outcome.replan.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	session.config.beforeHeadCAS = nil
	if _, err := advanceTestPublication(t, session, plan); !errors.Is(err, _errPublicationRequestInvalid) {
		t.Fatalf("replay superseded plan: %v", err)
	}
	stop := errors.New("stop after durable replacement")
	session.config.afterPendingReplacement = func() error { return stop }
	if _, err := advanceTestPublication(t, session, second); !errors.Is(err, stop) {
		t.Fatalf("replacement boundary: %v", err)
	}
	next := readTestSyncPublication(t, state.clientDir, state.worktree)
	if next.ExpectedHead != secondHead || next.CapturedCommit != captured || len(next.CandidateHistory) != 1 ||
		!bytes.Equal(next.CandidateHistory[0], first.CandidateData) || next.DeleteConfirmed {
		t.Fatal("second replacement did not retain the captured commit and candidate history")
	}
	session.config.afterPendingReplacement = nil
	outcome, err = advanceTestPublication(t, session, _resumePublicationRequest{round: start.round})
	if err != nil || outcome.Kind() != _publicationOutcomeFinalized {
		t.Fatalf("resume replacement: kind=%v err=%v", outcome.Kind(), err)
	}
	assertTestSyncPublicationFinalized(t, state, next.CandidateCommit)
}

func TestPublicationLifecycleSyncHeadConflictRequiresNewDeletionConfirmation(t *testing.T) {
	state := newImportedBinding(t)
	var staleHead atomic.Bool
	var headStatus atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/head") {
			if r.Method == http.MethodGet && staleHead.Swap(false) {
				// Hold the last pre-CAS observation fixed while the server has already advanced.
				_ = json.NewEncoder(w).Encode(struct{ Head remoteHead }{remoteHead{CommitID: &state.binding.SyncBase, ETag: state.binding.HeadETag}})
				return
			}
			if r.Method == http.MethodPut {
				resp := httptest.NewRecorder()
				state.environment.handler.ServeHTTP(resp, r)
				headStatus.Store(int32(resp.Code))
				w.WriteHeader(resp.Code)
				_, _ = w.Write(resp.Body.Bytes())
				return
			}
		}
		state.environment.handler.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	updateRestoreTestServer(t, state, proxy.URL)
	if err := os.Remove(filepath.Join(state.worktree, "local")); err != nil {
		t.Fatal(err)
	}
	session := openTestPublicationSession(t, state)
	start := prepareTestSyncPublication(t, session)
	out, err := advanceTestPublication(t, session, start)
	if out.Kind() != _publicationOutcomeConfirmationRequired || err == nil {
		t.Fatalf("start: kind=%v err=%v", out.Kind(), err)
	}
	session.config.beforeHeadCAS = func() error {
		publishSameRootSuccessor(t, state, time.Unix(100, 0))
		staleHead.Store(true)
		return nil
	}
	confirm := _resumePublicationRequest{round: start.round}
	id := start.prepared.candidate.CandidateCommit
	confirm.round.confirmDeleteSet, confirm.round.confirmDelete = true, id[:deleteCandidatePrefixLen]
	out, err = advanceTestPublication(t, session, confirm)
	if err != nil || out.Kind() != _publicationOutcomeSyncReplan || headStatus.Load() != http.StatusPreconditionFailed ||
		!readTestSyncPublication(t, state.clientDir, state.worktree).DeleteConfirmed {
		t.Fatalf("confirmed candidate conflict: kind=%v HTTP=%d err=%v", out.Kind(), headStatus.Load(), err)
	}
	request, err := out.replan.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	session.config.beforeHeadCAS = nil
	out, err = advanceTestPublication(t, session, request)
	next := readTestSyncPublication(t, state.clientDir, state.worktree)
	if required, ok := errors.AsType[*deleteConfirmationRequiredError](err); !ok || out.Kind() != _publicationOutcomeConfirmationRequired ||
		required.candidate != next.CandidateCommit || next.CandidateCommit == id || next.CapturedCommit != id || next.DeleteConfirmed {
		t.Fatalf("replacement reused deletion confirmation: kind=%v err=%v", out.Kind(), err)
	}
	if _, err := advanceTestPublication(t, session, confirm); !errors.Is(err, _errWrongDeletePrefix) {
		t.Fatalf("old confirmation for replacement: %v", err)
	}
}

func TestPublicationLifecycleSyncRejectsStaleReplacement(t *testing.T) {
	for _, scenario := range []string{"different worktree", "binding", "changed worktree", "Head", "ETag only", "pending row"} {
		t.Run(scenario, func(t *testing.T) {
			state := newImportedBinding(t)
			session, start := leaveTestSyncPending(t, state)
			id := start.prepared.candidate.CandidateCommit
			publishSameRootSuccessor(t, state, time.Unix(100, 0))
			out, err := advanceTestPublication(t, session, _resumePublicationRequest{round: start.round})
			if err != nil || out.Kind() != _publicationOutcomeSyncReplan {
				t.Fatalf("conflict: kind=%v err=%v", out.Kind(), err)
			}
			request, err := out.replan.prepare(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			applyAt := session
			var want string
			switch scenario {
			case "different worktree":
				other := newImportedBinding(t)
				applyAt = openTestPublicationSession(t, other)
				want = _errPublicationRequestInvalid.Error()
			case "binding":
				_, err = session.db.Exec(`UPDATE bindings SET head_etag = 'changed' WHERE worktree = ?`, session.binding.Worktree)
				want = _errPublicationSessionBindingMismatch.Error()
			case "changed worktree":
				err = os.WriteFile(filepath.Join(state.worktree, "added"), []byte("newer"), 0o600)
				want = "worktree changed while preparing pending merge replacement"
			case "Head":
				publishSameRootSuccessor(t, state, time.Unix(200, 0))
				want = "remote Head changed"
			case "ETag only":
				_, err = state.environment.store.DB().Exec(`UPDATE libraries SET head_version = head_version + 1 WHERE owner_user_id = ? AND id = ?`, testClientUserID, testClientLibraryID)
				want = "remote Head changed"
			case "pending row":
				_, err = session.db.Exec(`UPDATE pending_publications SET tracked_count = tracked_count + 1 WHERE worktree = ?`, session.binding.Worktree)
				want = "changed before conflict replacement"
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeHead := restoreTestHead(t, state)
			if _, err := advanceTestPublication(t, applyAt, request); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("apply stale plan: err=%v, want %q", err, want)
			}
			got := readTestSyncPublication(t, state.clientDir, state.worktree)
			if got.CandidateCommit != id || restoreTestHead(t, state) != beforeHead ||
				scenario == "pending row" && got.TrackedCount != start.prepared.tracked+1 {
				t.Fatal("stale replacement overwrote the pending row or advanced Head")
			}
		})
	}
}

func leaveTestSyncPending(t *testing.T, state importedBinding) (*_lockedBoundWorktreeSession, _startSyncPublicationRequest) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state.worktree, "added"), []byte("captured"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := openTestPublicationSession(t, state)
	start := prepareTestSyncPublication(t, session)
	stop := errors.New("stop before publishing uploaded candidate")
	session.config.beforeHeadCAS = func() error { return stop }
	if _, err := advanceTestPublication(t, session, start); !errors.Is(err, stop) {
		t.Fatalf("prepare pending candidate: %v", err)
	}
	session.config.beforeHeadCAS = nil
	return session, start
}

func TestPublicationRecoveryPreservesDeletedIgnoredPathIndex(t *testing.T) {
	for _, liftBeforeRecovery := range []bool{false, true} {
		name := "rules still active"
		if liftBeforeRecovery {
			name = "rules already lifted"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(syncignore.Env, "")
			env, aDir, aTree, bDir, bTree, _, _ := newSyncPair(t)
			sync := func(dir, tree string) {
				t.Helper()
				if err := syncTestWorktree(t, dir, tree); err != nil {
					t.Fatal(err)
				}
			}
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "keep.tmp"), "old ignored")
			sync(bDir, bTree)
			sync(aDir, aTree)
			rules := setLibraryIgnoreRules(t, `{"Patterns":["*.tmp"]}`)
			sync(aDir, aTree)
			t.Setenv(syncignore.Env, "")
			if err := os.Remove(filepath.Join(bTree, "keep.tmp")); err != nil {
				t.Fatal(err)
			}
			if err := syncTestWorktreeConfirmingDeletes(t, bDir, bTree); err != nil {
				t.Fatal(err)
			}
			t.Setenv(syncignore.Env, rules)
			sync(aDir, aTree)
			checkLibraryIgnoreFile(t, filepath.Join(aTree, "keep.tmp"), "old ignored")
			index := captureTestPathIndex(t, aDir, aTree)
			i := slices.IndexFunc(index, func(row testPathIndexRow) bool { return row.path == "keep.tmp" })
			if i < 0 || index[i].actualMtime != "" || index[i].canonicalMtime == "" {
				t.Fatalf("missing prior unmaterialized row: %v", index)
			}

			writeLibraryIgnoreFile(t, filepath.Join(aTree, "added"), "captured")
			stop := errors.New("stop before Head publication")
			err := runLibraryWithConfig(t.Context(), []string{"sync", "--client-dir", aDir, "--worktree", aTree}, strings.NewReader(""), io.Discard, io.Discard, libraryClientConfig{
				checkFilesystem: func(*os.File) error { return nil }, beforeHeadCAS: func() error { return stop },
			})
			if !errors.Is(err, stop) {
				t.Fatalf("leave a pending publication: %v", err)
			}
			pending := readTestSyncPublication(t, aDir, aTree)
			if _, conflict, err := updateRemoteHead(t.Context(), mustServerURL(t, env.server.URL), testClientLibraryID, []byte(env.token), pending.ExpectedETag, pending.CandidateCommit); err != nil || conflict {
				t.Fatalf("publish pending candidate: conflict=%t err=%v", conflict, err)
			}
			writeLibraryIgnoreFile(t, filepath.Join(aTree, "added"), "dirty after publication")
			if liftBeforeRecovery {
				t.Setenv(syncignore.Env, "")
			}
			if err := syncTestWorktree(t, aDir, aTree); err == nil || !strings.Contains(err.Error(), "preserving local changes") {
				t.Fatalf("dirty recovery: %v", err)
			}
			checkLibraryIgnoreFile(t, filepath.Join(aTree, "keep.tmp"), "old ignored")
			checkLibraryIgnoreFile(t, filepath.Join(aTree, "added"), "dirty after publication")
			if got := captureTestPathIndex(t, aDir, aTree); !slices.Contains(got, index[i]) {
				t.Fatalf("recovery lost the omitted local copy: %v", got)
			}
			if got := readTestBinding(t, aDir, aTree); got.SyncBase != pending.CandidateCommit || countClientRows(t, aDir, "pending_publications", aTree) != 0 {
				t.Fatalf("recovery did not settle the pending publication: %+v", got)
			}

			t.Setenv(syncignore.Env, "")
			sync(aDir, aTree)
			sync(bDir, bTree)
			checkLibraryIgnoreFile(t, filepath.Join(bTree, "added"), "dirty after publication")
			for _, tree := range []string{aTree, bTree} {
				if _, err := os.Stat(filepath.Join(tree, "keep.tmp")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted file resurrected in %s: %v", tree, err)
				}
			}
			assertTestConverged(t, env, aDir, aTree)
		})
	}
}

func TestPublicationLifecycleSyncRecoversPublishedCandidate(t *testing.T) {
	for _, scenario := range []string{"already Head", "dirty", "successor", "dirty successor", "interrupted successor handoff"} {
		t.Run(scenario, func(t *testing.T) {
			state := newImportedBinding(t)
			var writes atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					writes.Add(1)
				}
				state.environment.handler.ServeHTTP(w, r)
			}))
			t.Cleanup(proxy.Close)
			updateRestoreTestServer(t, state, proxy.URL)
			session, start := leaveTestSyncPending(t, state)
			candidate := start.prepared.candidate
			head, conflict, err := updateRemoteHead(t.Context(), session.base, session.binding.LibraryID, session.token, candidate.ExpectedETag, candidate.CandidateCommit)
			if err != nil || conflict {
				t.Fatalf("publish candidate: conflict=%t err=%v", conflict, err)
			}
			var successor string
			if strings.Contains(scenario, "successor") {
				// This successor removes "added"; applying checkout before handoff is observable.
				data, id, err := canonicalCommit(testClientUserID, testOtherDeviceID, state.binding.SyncBaseRoot,
					[]string{candidate.CandidateCommit}, func() time.Time { return time.Unix(300, 0) })
				if err != nil {
					t.Fatal(err)
				}
				if err := putMetadata(t.Context(), session.base, session.binding.LibraryID, session.token, "commits", id, data); err != nil {
					t.Fatal(err)
				}
				if _, conflict, err := updateRemoteHead(t.Context(), session.base, session.binding.LibraryID, session.token, head.ETag, id); err != nil || conflict {
					t.Fatalf("publish successor: conflict=%t err=%v", conflict, err)
				}
				successor = id
			}
			dirty := strings.Contains(scenario, "dirty")
			wantBytes := "captured"
			if dirty {
				wantBytes = "edited since publication"
				if err := os.WriteFile(filepath.Join(state.worktree, "added"), []byte(wantBytes), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writes.Store(0)
			resume := _resumePublicationRequest{round: start.round}
			stop := errors.New("interrupted after handoff commit")
			if scenario == "interrupted successor handoff" {
				index := captureTestPathIndex(t, state.clientDir, state.worktree)
				// Abort the last write of the handoff; the checkout and baseline must roll back with it.
				if _, err := session.db.Exec(`CREATE TEMP TRIGGER interrupt_handoff BEFORE DELETE ON pending_publications
					BEGIN SELECT RAISE(ABORT, 'interrupt handoff transaction'); END`); err != nil {
					t.Fatal(err)
				}
				out, err := advanceTestPublication(t, session, resume)
				if err == nil || !strings.Contains(err.Error(), "interrupt handoff transaction") || out.checkout != nil ||
					countClientRows(t, state.clientDir, "pending_checkouts", state.worktree) != 0 ||
					readPendingCandidate(t, state.clientDir, state.worktree) != candidate.CandidateCommit ||
					readTestBinding(t, state.clientDir, state.worktree).SyncBase != candidate.BaseCommit ||
					!slices.Equal(captureTestPathIndex(t, state.clientDir, state.worktree), index) {
					t.Fatalf("interrupted transaction changed durable state: kind=%v err=%v", out.Kind(), err)
				}
				if _, err := session.db.Exec(`DROP TRIGGER interrupt_handoff`); err != nil {
					t.Fatal(err)
				}
				session.config.afterSyncCheckoutTransition = func() error { return stop }
			}
			out, err := advanceTestPublication(t, session, resume)
			switch {
			case dirty:
				if out.Kind() != _publicationOutcomeRerunSync || err == nil || !strings.Contains(err.Error(), "preserving local changes") {
					t.Fatalf("dirty recovery: kind=%v err=%v", out.Kind(), err)
				}
			case scenario == "interrupted successor handoff":
				if !errors.Is(err, stop) {
					t.Fatalf("post-commit interruption: %v", err)
				}
			case successor != "":
				if err != nil || out.Kind() != _publicationOutcomeCheckoutPending || out.checkout.TargetCommit != successor {
					t.Fatalf("handoff: kind=%v err=%v", out.Kind(), err)
				}
			default:
				if err != nil || out.Kind() != _publicationOutcomeFinalized {
					t.Fatalf("already Head: kind=%v err=%v", out.Kind(), err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(state.worktree, "added")); err != nil || string(data) != wantBytes {
				t.Fatalf("publication touched the worktree: %q, %v", data, err)
			}
			binding := readTestBinding(t, state.clientDir, state.worktree)
			if writes.Load() != 0 || binding.SyncBase != candidate.CapturedCommit || binding.SyncBaseRoot != candidate.CapturedRoot ||
				countClientRows(t, state.clientDir, "pending_publications", state.worktree) != 0 {
				t.Fatalf("recovery republished or did not settle the captured baseline; writes=%d", writes.Load())
			}
			index := captureTestPathIndex(t, state.clientDir, state.worktree)
			for _, captured := range start.prepared.snapshot.paths {
				if !slices.ContainsFunc(index, func(row testPathIndexRow) bool {
					return row.path == captured.path && row.id == captured.id && row.size == captured.size
				}) {
					t.Fatal("recovery indexed the dirty or successor tree instead of the captured snapshot")
				}
			}
			if successor == "" || dirty {
				if countClientRows(t, state.clientDir, "pending_checkouts", state.worktree) != 0 {
					t.Fatal("unexpected checkout")
				}
				return
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openTestPublicationSession(t, state)
			out, err = advanceTestPublication(t, reopened, start)
			if err != nil || out.Kind() != _publicationOutcomeCheckoutPending || out.checkout.TargetCommit != successor {
				t.Fatalf("restart did not prioritize durable checkout: kind=%v err=%v", out.Kind(), err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			if err := syncTestWorktree(t, state.clientDir, state.worktree); err != nil {
				t.Fatal(err)
			}
			assertTestSyncPublicationFinalized(t, state, successor)
			if _, err := os.Stat(filepath.Join(state.worktree, "added")); !errors.Is(err, os.ErrNotExist) || writes.Load() != 0 {
				t.Fatalf("restarted checkout: added err=%v remote writes=%d", err, writes.Load())
			}
		})
	}
}
