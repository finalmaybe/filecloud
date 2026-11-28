package clientapp

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanonicalRestoreCommitFixedVector(t *testing.T) {
	data, id, err := canonicalRestoreCommit(testClientUserID, testClientDeviceID, strings.Repeat("a", 64),
		strings.Repeat("b", 64), "docs/file.txt", []string{strings.Repeat("c", 64)},
		time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	const wantData = `{"AuthorUserId":"01234567-89ab-4def-8123-456789abcdef","CreatedAt":"2026-08-09T01:02:03Z","DeviceId":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","Message":"restore bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb docs/file.txt","Parents":["cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"],"Root":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Type":"Commit","Version":1}`
	const wantID = "7090d86be36313bc1ccca897b18f1d78b82c778dae5a1d5959bfb5241e585d90"
	if string(data) != wantData || id != wantID {
		t.Fatalf("fixed restore vector data=%s id=%s, want data=%s id=%s", data, id, wantData, wantID)
	}
}

func TestClientV24RestorePublicationSchemaAndRoundTrip(t *testing.T) {
	db, err := initializeClientDB(t.Context(), t.TempDir(), syncDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRowContext(t.Context(), "SELECT MAX(version) FROM client_schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != _clientSchemaVersion {
		t.Fatalf("schema migration version=%d, want %d", version, _clientSchemaVersion)
	}

	binding, pending := testRestorePendingPublication(t)
	if err := saveRestorePublication(t.Context(), db, binding, pending); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadPendingPublication(t.Context(), db, binding)
	if err != nil || loaded == nil {
		t.Fatalf("load restore publication=%+v err=%v", loaded, err)
	}
	if !reflect.DeepEqual(loaded, pending) {
		t.Fatalf("round trip changed restore publication:\nwant=%+v\ngot=%+v", pending, loaded)
	}
}

func TestClientV24RestorePublicationDatabaseChecks(t *testing.T) {
	db, err := initializeClientDB(t.Context(), t.TempDir(), syncDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	binding, pending := testRestorePendingPublication(t)
	if err := saveRestorePublication(t.Context(), db, binding, pending); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, statement string
	}{
		{name: "candidate root equals captured root", statement: `UPDATE pending_publications SET candidate_root = captured_root WHERE worktree = ?`},
		{name: "zero changed paths", statement: `UPDATE pending_publications SET changed_path_count = 0 WHERE worktree = ?`},
		{name: "statistics mismatch", statement: `UPDATE pending_publications SET created_count = 0 WHERE worktree = ?`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), test.statement, "/work"); err == nil {
				t.Fatal("database accepted invalid restore publication cross-fields")
			}
		})
	}
	loaded, err := loadPendingPublication(t.Context(), db, binding)
	if err != nil || loaded == nil || !reflect.DeepEqual(loaded, pending) {
		t.Fatalf("failed database checks changed valid publication: loaded=%+v want=%+v err=%v", loaded, pending, err)
	}
}

func TestClientV23ToV24SyncPendingMigrationPreservesFields(t *testing.T) {
	db, err := initializeClientDB(t.Context(), t.TempDir(), syncDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const worktree = "/migration-work"
	binding := clientBinding{Worktree: worktree, UserID: testClientUserID, DeviceID: testClientDeviceID,
		SyncBase: strings.Repeat("a", 64), SyncBaseRoot: strings.Repeat("b", 64)}
	expected := testSyncPublicationState(t, binding)
	expected.DeletionCount, expected.TrackedCount = 7, 20
	expected.RequiresDeleteConfirmation, expected.DeleteConfirmed = true, true
	if _, err := db.Exec(`ALTER TABLE pending_publications RENAME TO v23_pending_publications`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(_clientV23PendingSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO pending_publications(worktree,publication_kind,base_commit,base_root,
		expected_head,expected_etag,candidate_commit,candidate_root,candidate_data,captured_commit,captured_root,captured_data,
		candidate_history,deletion_count,tracked_count,requires_delete_confirmation,delete_confirmed,legacy_revalidation_required)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		worktree, PublicationKindSync, expected.BaseCommit, expected.BaseRoot, expected.ExpectedHead, expected.ExpectedETag,
		expected.CandidateCommit, expected.CandidateRoot, expected.CandidateData, expected.CapturedCommit, expected.CapturedRoot,
		expected.CapturedData, []byte(_emptyCandidateHistory), expected.DeletionCount, expected.TrackedCount,
		expected.RequiresDeleteConfirmation, expected.DeleteConfirmed, expected.LegacyRevalidationRequired); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE v23_pending_publications; DELETE FROM client_schema_migrations WHERE version>=24`); err != nil {
		t.Fatal(err)
	}

	if err := initializeClientSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadPendingPublication(t.Context(), db, binding)
	if err != nil || loaded == nil {
		t.Fatalf("load migrated sync publication=%+v err=%v", loaded, err)
	}
	got, ok := loaded.(_syncPublicationState)
	if !ok || !reflect.DeepEqual(got._publicationCandidate, expected._publicationCandidate) ||
		got._syncPublicationFields != expected._syncPublicationFields || len(got.CandidateHistory) != 0 {
		t.Fatalf("v23 sync publication changed during migration:\nwant=%+v\n got=%+v", expected, loaded)
	}
	var version int
	if err := db.QueryRowContext(t.Context(), "SELECT MAX(version) FROM client_schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != _clientSchemaVersion {
		t.Fatalf("migrated schema version=%d, want %d", version, _clientSchemaVersion)
	}
}

func TestRestorePublicationDispatcherRejectsUnconfirmedWithoutMutation(t *testing.T) {
	state, pending, _ := prepareRestoreConfirmationTest(t)
	before := captureRestoreConfirmationTestState(t, state)
	session := openTestPublicationSession(t, state)
	scanConfig, err := session.loadScanConfig(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := advanceTestPublication(t, session, _resumePublicationRequest{round: _syncPublicationRoundOptions{scanConfig: scanConfig}})
	if outcome.Kind() != _publicationOutcomeConfirmationRequired || err == nil ||
		!strings.Contains(err.Error(), "restore --confirm "+pending.CandidateCommit[:deleteCandidatePrefixLen]) {
		t.Fatalf("unconfirmed restore outcome=%v, error=%v", outcome.Kind(), err)
	}
	assertRestoreConfirmationTestState(t, "unconfirmed restore", state, before)
	preview := advanceTestRestorePreview(t, session, _previewRestorePublicationRequest{sourceCommit: pending.SourceCommit, sourcePath: pending.SourcePath})
	if preview.candidateCommit != pending.CandidateCommit {
		t.Fatalf("resume changed unconfirmed restore: got %s, want %s", preview.candidateCommit, pending.CandidateCommit)
	}
}

func testRestorePendingPublication(t *testing.T) (clientBinding, _restorePublicationState) {
	t.Helper()
	baseRoot := strings.Repeat("a", 64)
	candidateRoot := strings.Repeat("b", 64)
	sourceRoot := strings.Repeat("c", 64)
	baseCommit := strings.Repeat("d", 64)
	sourceCommit := strings.Repeat("e", 64)
	capturedData, capturedCommit, err := canonicalCommit(testClientUserID, testClientDeviceID, baseRoot,
		[]string{baseCommit}, func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	candidateData, candidateCommit, err := canonicalRestoreCommit(testClientUserID, testClientDeviceID, candidateRoot,
		sourceCommit, "docs/file.txt", []string{capturedCommit}, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	binding := clientBinding{ServerURL: "http://localhost", LibraryID: testClientLibraryID, Worktree: "/work",
		UserID: testClientUserID, DeviceID: testClientDeviceID, SyncBase: capturedCommit, SyncBaseRoot: baseRoot,
		HeadETag: `"head-version-1"`}
	return binding, _restorePublicationState{
		_publicationCandidate: _publicationCandidate{BaseCommit: capturedCommit, BaseRoot: baseRoot, ExpectedHead: capturedCommit,
			ExpectedETag: binding.HeadETag, CandidateCommit: candidateCommit, CandidateRoot: candidateRoot,
			CandidateData: candidateData, CapturedCommit: capturedCommit, CapturedRoot: baseRoot, CapturedData: capturedData},
		_restorePublicationFields: _restorePublicationFields{SourceCommit: sourceCommit, SourcePath: "docs/file.txt", SourceRoot: sourceRoot,
			CreatedCount: 1, UpdatedCount: 1, PreservedCurrentOnlyCount: 5, ChangedPathCount: 2},
		ChangedPathPreview: []string{"docs/file.txt", "other.txt"},
	}
}

func testSyncPublicationState(t *testing.T, binding clientBinding) _syncPublicationState {
	t.Helper()
	root := strings.Repeat("e", 64)
	data, id, err := canonicalCommit(binding.UserID, binding.DeviceID, root, []string{binding.SyncBase},
		func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return _syncPublicationState{
		_publicationCandidate: _publicationCandidate{BaseCommit: binding.SyncBase, BaseRoot: binding.SyncBaseRoot,
			ExpectedHead: binding.SyncBase, ExpectedETag: `"test-head"`, CandidateCommit: id, CandidateRoot: root,
			CandidateData: data, CapturedCommit: id, CapturedRoot: root, CapturedData: data},
		CandidateHistory: [][]byte{},
	}
}
