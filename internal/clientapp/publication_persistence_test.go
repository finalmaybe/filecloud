package clientapp

import (
	"strings"
	"testing"
	"time"
)

func TestPublicationLifecyclePreservesCorruptPendingState(t *testing.T) {
	for _, test := range []struct {
		name, update, want string
		args               []any
		restore            bool
	}{
		{name: "unknown kind", update: `publication_kind = 'unknown'`, want: "unsupported pending publication kind"},
		{name: "oversized commit", update: `candidate_data = zeroblob(?)`, args: []any{_maxCandidateCommitBytes + 1}, want: "metadata exceeds synchronization budget"},
		{name: "sync with restore fields", update: `restore_confirmed = 1`, want: "sync publication contains restore state"},
		{name: "sync noncanonical history", update: `candidate_history = X'464348310000000000'`, want: "candidate history has trailing data"},
		{name: "sync confirmation without protection", update: `delete_confirmed = 1`, want: "pending publication is corrupt"},
		{name: "restore with sync fields", update: `requires_delete_confirmation = 1`, restore: true, want: "restore publication contains synchronization-only state"},
		{name: "restore oversized preview", update: `changed_path_preview = zeroblob(?)`, args: []any{_restorePreviewMaxBytes + 1}, restore: true, want: "metadata exceeds synchronization budget"},
		{name: "restore noncanonical preview", update: `changed_path_preview = CAST(changed_path_preview || X'00' AS BLOB)`, restore: true, want: "restore preview has trailing data"},
		{name: "restore invalid path", update: `source_path = '../outside'`, restore: true, want: "source fields are invalid"},
		{name: "restore invalid stats", update: `updated_count = updated_count + 1`, restore: true, want: "statistics do not match"},
		{name: "restore invalid preview count", update: `changed_path_count = changed_path_count + 1, updated_count = updated_count + 1`, restore: true, want: "changed path count does not match its preview"},
		{name: "restore invalid truncation", update: `preview_truncated = 1`, restore: true, want: "preview truncation is inconsistent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var session *_lockedBoundWorktreeSession
			var request _publicationRequester = _inspectPublicationRequest{}
			if test.restore {
				state, pending, _ := prepareRestoreConfirmationTest(t)
				session = openTestPublicationSession(t, state)
				request = _confirmRestorePublicationRequest{prefix: pending.CandidateCommit[:deleteCandidatePrefixLen]}
			} else {
				session = openTestPublicationSession(t, newImportedBinding(t))
				pending := testSyncPublicationState(t, session.binding)
				if err := saveSyncPublication(t.Context(), session.db, session.binding, &pending); err != nil {
					t.Fatal(err)
				}
			}
			args := append(append([]any(nil), test.args...), session.binding.Worktree)
			if _, err := session.db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON;
				UPDATE pending_publications SET `+test.update+` WHERE worktree = ?;
				PRAGMA ignore_check_constraints=OFF`, args...); err != nil {
				t.Fatal(err)
			}

			for range 2 {
				outcome, err := advanceTestPublication(t, session, request)
				if err == nil || !strings.Contains(err.Error(), test.want) || outcome.Kind() != 0 {
					t.Fatalf("Advance corrupt publication: kind=%v err=%v; want %q", outcome.Kind(), err, test.want)
				}
			}
		})
	}
}

func TestPublicationLifecycleRejectsRestoreCandidateWithDifferentDevice(t *testing.T) {
	state, pending, _ := prepareRestoreConfirmationTest(t)
	session := openTestPublicationSession(t, state)
	data, id, err := canonicalRestoreCommit(session.binding.UserID, testOtherDeviceID, pending.CandidateRoot,
		pending.SourceCommit, pending.SourcePath, []string{pending.ExpectedHead}, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.db.ExecContext(t.Context(), `UPDATE pending_publications SET candidate_commit = ?, candidate_data = ? WHERE worktree = ?`,
		id, data, session.binding.Worktree); err != nil {
		t.Fatal(err)
	}
	outcome, err := advanceTestPublication(t, session, _confirmRestorePublicationRequest{prefix: id[:deleteCandidatePrefixLen]})
	if err == nil || !strings.Contains(err.Error(), "restore publication candidate commit is invalid") || outcome.Kind() != 0 {
		t.Fatalf("Advance with mismatched device: kind=%v err=%v", outcome.Kind(), err)
	}
}
