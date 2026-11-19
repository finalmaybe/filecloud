package clientapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicationLifecycleRestoreRecoversCASOutcome(t *testing.T) {
	for _, scenario := range []string{"lost response", "already Head", "ancestor", "dirty worktree", "cancel during CAS", "cancel during resolution"} {
		t.Run(scenario, func(t *testing.T) {
			state, request := newRestoreLifecycleFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var headPUTs, objectPUTs, resolutions atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/head") {
					switch req.Method {
					case http.MethodPut:
						headPUTs.Add(1)
						response := httptest.NewRecorder()
						state.environment.handler.ServeHTTP(response, req)
						if response.Code != http.StatusOK {
							t.Errorf("publish before losing response: status=%d", response.Code)
						}
						if scenario == "cancel during CAS" {
							cancel()
							<-req.Context().Done()
							return
						}
						panic(http.ErrAbortHandler)
					case http.MethodGet:
						if headPUTs.Load() != 0 && resolutions.Add(1) == 1 {
							switch scenario {
							case "cancel during resolution":
								cancel()
								<-req.Context().Done()
								return
							case "already Head", "ancestor", "dirty worktree":
								http.Error(w, "private response body", http.StatusServiceUnavailable)
								return
							}
						}
					}
				} else if req.Method == http.MethodPut {
					objectPUTs.Add(1)
				}
				state.environment.handler.ServeHTTP(w, req)
			}))
			t.Cleanup(proxy.Close)
			updateRestoreTestServer(t, state, proxy.URL)
			session := openTestPublicationSession(t, state)
			preview := advanceTestRestorePreview(t, session, request)
			before := captureRestoreConfirmationTestState(t, state)
			confirm := _confirmRestorePublicationRequest{prefix: preview.confirmationPrefix()}
			var lifecycle _publicationLifecycle
			outcome, err := lifecycle.Advance(ctx, session, confirm)
			wantCheckout := preview.candidateCommit
			uploaded := objectPUTs.Load()
			wantResolutions := int32(1)
			if scenario == "cancel during CAS" {
				wantResolutions = 0
			}
			if headPUTs.Load() != 1 || resolutions.Load() != wantResolutions || uploaded == 0 {
				t.Fatalf("first publication: CAS=%d resolutions=%d object PUTs=%d err=%v", headPUTs.Load(), resolutions.Load(), uploaded, err)
			}
			if scenario != "lost response" {
				if err == nil || (strings.HasPrefix(scenario, "cancel") && !errors.Is(err, context.Canceled)) ||
					!strings.Contains(err.Error(), "resolve library Head after publish") || strings.Contains(err.Error(), "private response body") {
					t.Fatalf("interrupted resolution: outcome=%v err=%v", outcome.Kind(), err)
				}
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
				session = openTestPublicationSession(t, state)
				inspected, inspectErr := advanceTestPublication(t, session, _inspectPublicationRequest{})
				pending, ok := inspected.pending.(_restorePublicationState)
				if inspectErr != nil || !ok || !pending.RestoreConfirmed || pending.CandidateCommit != preview.candidateCommit {
					t.Fatalf("interrupted resolution did not retain exact confirmation: outcome=%v err=%v", inspected.Kind(), inspectErr)
				}
				if scenario == "ancestor" {
					wantCheckout = publishSameRootSuccessor(t, state, time.Date(2026, 8, 9, 4, 5, 6, 0, time.UTC))
				}
				if scenario == "dirty worktree" {
					if err := os.WriteFile(filepath.Join(state.worktree, "local"), []byte("after-cas"), 0o600); err != nil {
						t.Fatal(err)
					}
					before.worktree = captureHistoryInspectWorktree(t, state.worktree)
				}
				if scenario == "already Head" {
					outcome, err = advanceTestPublication(t, session, confirm)
				} else {
					scanConfig, scanErr := session.loadScanConfig(t.Context(), nil)
					if scanErr != nil {
						t.Fatal(scanErr)
					}
					outcome, err = advanceTestPublication(t, session, _resumeRestorePublicationRequest{scanConfig: scanConfig})
				}
			}
			if headPUTs.Load() != 1 || objectPUTs.Load() != uploaded {
				t.Fatalf("recovery republished: CAS=%d object PUTs=%d, want 1/%d", headPUTs.Load(), objectPUTs.Load(), uploaded)
			}
			if scenario == "dirty worktree" {
				if outcome.Kind() != _publicationOutcomeRerunSync || err == nil || !strings.Contains(err.Error(), "worktree changed; rerun sync") ||
					countClientRows(t, state.clientDir, "pending_checkouts", state.worktree) != 0 {
					t.Fatalf("dirty published Restore: outcome=%v err=%v", outcome.Kind(), err)
				}
				before.head = restoreTestHead(t, state)
			} else {
				if err != nil || outcome.Kind() != _publicationOutcomeCheckoutPending || outcome.checkout.TargetCommit != wantCheckout {
					t.Fatalf("published Restore: outcome=%v checkout=%+v want=%s err=%v", outcome.Kind(), outcome.checkout, wantCheckout, err)
				}
				before.head = wantCheckout + "\x00" + outcome.checkout.HeadETag
			}
			before.pendingRows = 0
			assertRestoreConfirmationTestState(t, scenario, state, before)
		})
	}
}

func TestPublicationLifecycleRestoreResolvesHeadConflict(t *testing.T) {
	for _, scenario := range []struct {
		name, body  string
		resolutions int32
	}{
		{name: "complete 412", resolutions: 0},
		{name: "412 without Head", body: `{}`, resolutions: 1},
		{name: "412 with invalid body", body: `{`, resolutions: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			state, request := newRestoreLifecycleFixture(t)
			binding := readTestBinding(t, state.clientDir, state.worktree)
			data, competitor, err := canonicalCommit(testClientUserID, testOtherDeviceID, binding.SyncBaseRoot,
				[]string{binding.SyncBase}, func() time.Time { return time.Date(2026, 8, 9, 3, 4, 5, 0, time.UTC) })
			if err != nil {
				t.Fatal(err)
			}
			base := mustServerURL(t, state.environment.server.URL)
			token := []byte(state.environment.token)
			if err := putMetadata(t.Context(), base, testClientLibraryID, token, "commits", competitor, data); err != nil {
				t.Fatal(err)
			}
			var headPUTs, resolutions atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/head") {
					if req.Method == http.MethodGet && headPUTs.Load() != 0 {
						resolutions.Add(1)
					}
					if req.Method == http.MethodPut && headPUTs.Add(1) == 1 {
						if _, conflict, err := updateRemoteHead(req.Context(), base, testClientLibraryID, token, binding.HeadETag, competitor); err != nil || conflict {
							t.Errorf("publish competitor: conflict=%t err=%v", conflict, err)
						}
						if scenario.body != "" {
							w.WriteHeader(http.StatusPreconditionFailed)
							_, _ = w.Write([]byte(scenario.body))
							return
						}
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
			outcome, err := advanceTestPublication(t, session, confirm)
			if outcome.Kind() != _publicationOutcomeRerunRestore || err == nil || !strings.HasSuffix(err.Error(), "restore candidate became stale; rerun restore preview") ||
				headPUTs.Load() != 1 || resolutions.Load() != scenario.resolutions {
				t.Fatalf("Head conflict: outcome=%v err=%v CAS=%d follow-up GETs=%d", outcome.Kind(), err, headPUTs.Load(), resolutions.Load())
			}
			if inspected, err := advanceTestPublication(t, session, _inspectPublicationRequest{}); err != nil || inspected.Kind() != _publicationOutcomeIdle {
				t.Fatalf("Head conflict retained the old confirmation or created a checkout: outcome=%v err=%v", inspected.Kind(), err)
			}
			if _, err := advanceTestPublication(t, session, confirm); err == nil || !strings.Contains(err.Error(), "requires a pending restore candidate") {
				t.Fatalf("old confirmation was accepted after a Head conflict: %v", err)
			}
			before.head = restoreTestHead(t, state)
			before.pendingRows = 0
			if !strings.HasPrefix(before.head, competitor+"\x00") {
				t.Fatalf("Head conflict changed the winning Head: %q", before.head)
			}
			assertRestoreConfirmationTestState(t, scenario.name, state, before)
		})
	}
}

func TestPublicationLifecycleRestoreRequiresAncestryEvidenceBeforeDiscard(t *testing.T) {
	state, request := newRestoreLifecycleFixture(t)
	var hideHeadCommit atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hideHeadCommit.Load() && req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/objects/commits/") {
			http.Error(w, "private response body", http.StatusNotFound)
			return
		}
		state.environment.handler.ServeHTTP(w, req)
	}))
	t.Cleanup(proxy.Close)
	updateRestoreTestServer(t, state, proxy.URL)
	session := openTestPublicationSession(t, state)
	preview := advanceTestRestorePreview(t, session, request)
	publishSameRootSuccessor(t, state, time.Date(2026, 8, 9, 4, 5, 6, 0, time.UTC))
	before := captureRestoreConfirmationTestState(t, state)
	confirm := _confirmRestorePublicationRequest{prefix: preview.confirmationPrefix()}
	hideHeadCommit.Store(true)
	if _, err := advanceTestPublication(t, session, confirm); err == nil || !strings.Contains(err.Error(), "verify remote publication history") ||
		strings.Contains(err.Error(), "private response body") {
		t.Fatalf("unverifiable ancestry: %v", err)
	}
	assertRestoreConfirmationTestState(t, "unverifiable ancestry", state, before)
	hideHeadCommit.Store(false)
	if outcome, err := advanceTestPublication(t, session, confirm); outcome.Kind() != _publicationOutcomeRerunRestore ||
		err == nil || err.Error() != "stale restore candidate discarded; rerun restore" {
		t.Fatalf("proven unrelated Head: outcome=%v err=%v", outcome.Kind(), err)
	}
	before.pendingRows = 0
	assertRestoreConfirmationTestState(t, "proven unrelated Head", state, before)
}
