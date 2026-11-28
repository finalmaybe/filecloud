package clientapp

import (
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/syncignore"
)

const replacementNestedFile = "docs/inbox/incoming"

func TestLibrarySyncReplacesOnlyChangedFile(t *testing.T) {
	for _, test := range []struct {
		name, path, update string
		ignore             bool
	}{
		{"root content", "base", "content", false},
		{"nested mtime", replacementNestedFile, "mtime", false},
		{"nested file-version restore", replacementNestedFile, "restore", false},
		{"nested with protected ancestors", replacementNestedFile, "content", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pair := newFileReplacementTestPair(t)
			want := "incoming original"
			if test.path == "base" {
				want = "base"
			}
			originalMtime := fileReplacementTestStat(t, pair.publisherTree, test.path).ModTime()
			wantMtime := originalMtime
			publisherFile := filepath.Join(pair.publisherTree, filepath.FromSlash(test.path))
			if test.update == "mtime" {
				wantMtime = originalMtime.Add(time.Hour)
				if err := os.Chtimes(publisherFile, wantMtime, wantMtime); err != nil {
					t.Fatal(err)
				}
			} else {
				writeLibraryIgnoreFile(t, publisherFile, "replacement")
				wantMtime = fileReplacementTestStat(t, pair.publisherTree, test.path).ModTime()
			}
			source := mustAcceptanceHead(t, pair.environment)
			pair.syncPublisher(t)
			switch test.update {
			case "restore":
				pair.syncSubscriber(t)
				base := mustServerURL(t, pair.environment.server.URL)
				endpoint := base.JoinPath("v1", "libraries", testClientLibraryID, "file-history", source, "restore")
				query := endpoint.Query()
				query.Set("Path", test.path)
				query.Set("AnchorCommitId", mustAcceptanceHead(t, pair.environment))
				endpoint.RawQuery = query.Encode()
				request, err := authenticatedRequest(t.Context(), http.MethodPost, endpoint.String(), []byte(pair.environment.token), nil)
				if err != nil {
					t.Fatal(err)
				}
				status, body, _, err := doClientRequest(request)
				if err != nil || status != http.StatusOK {
					t.Fatalf("restore file version: status=%d body=%s err=%v", status, body, err)
				}
				wantMtime = originalMtime
			case "content":
				want = "replacement"
			}
			untouched := slices.DeleteFunc(slices.Clone(fileReplacementTestUntouched), func(path string) bool { return path == test.path })
			if test.ignore {
				setLibraryIgnoreRules(t, `{"SkipHidden":true,"Patterns":["*.tmp"]}`)
				for _, path := range []string{"docs/inbox/keep.tmp", "elsewhere/.keep"} {
					writeLibraryIgnoreFile(t, filepath.Join(pair.subscriberTree, filepath.FromSlash(path)), "local ignored")
					untouched = append(untouched, path)
				}
			}
			before := fileReplacementTestStats(t, pair.subscriberTree, untouched)
			oldFile := fileReplacementTestStat(t, pair.subscriberTree, test.path)
			held, err := openTestHeldFile(filepath.Join(pair.subscriberTree, "docs", "inbox", "sibling"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			var captured, installed []string
			var actions []fsAction
			err = pair.run(t, "sync", libraryClientConfig{
				afterSyncRecoveryRegistered: func(path string) error { captured = append(captured, path); return nil },
				afterCheckoutInstall:        func(path, _ string) error { installed = append(installed, path); return nil },
				fsActionFault: func(point string, action fsAction) error {
					if point == "after_intent_commit" {
						actions = append(actions, action)
					}
					return nil
				},
			})
			if err != nil || !slices.Equal(captured, []string{test.path}) || !slices.Equal(installed, []string{test.path}) {
				t.Fatalf("sync=%v; capture=%v install=%v; want only %q", err, captured, installed, test.path)
			}
			parent, _ := splitFSActionPath(test.path)
			for _, action := range actions {
				if action.ExpectedKind == "File" && action.Parent == parent || action.Op == fsOpMtime &&
					action.ExpectedKind == "Directory" && isCheckoutAncestor(joinScanPath(action.Parent, action.Source), test.path) {
					continue
				}
				t.Errorf("filesystem action outside replacement scope: %+v", action)
			}
			checkLibraryIgnoreFile(t, filepath.Join(pair.subscriberTree, filepath.FromSlash(test.path)), want)
			current := fileReplacementTestStat(t, pair.subscriberTree, test.path)
			if os.SameFile(oldFile, current) || canonicalProtocolMtime(current.ModTime()) != canonicalProtocolMtime(wantMtime) {
				t.Fatalf("target file not replaced with canonical mtime: old=%v current=%v want mtime=%v", oldFile, current, wantMtime)
			}
			if data, err := io.ReadAll(held); err != nil || string(data) != "sibling original" {
				t.Fatalf("held sibling content=%q err=%v", data, err)
			}
			assertFileReplacementTestInodes(t, pair.subscriberTree, before, test.path)
			if !test.ignore {
				assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
			}
			pair.assertNoFollowupPublication(t)
		})
	}
}

func TestLibrarySyncFileReplacementStagesAndSyncsBeforeCapture(t *testing.T) {
	pair := newFileReplacementTestPair(t)
	pair.publishReplacement(t)
	old := filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile))
	oldInfo := fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)
	publisherTime := fileReplacementTestStat(t, pair.publisherTree, replacementNestedFile).ModTime()
	stop := errors.New("interrupt with complete but unsynced bytes")
	var stagedPath string
	err := pair.run(t, "sync", libraryClientConfig{
		beforeCheckoutFileWrite: func(path, name string) error {
			if path != replacementNestedFile {
				t.Fatalf("staged unexpected path %q", path)
			}
			stagedPath = filepath.Join(filepath.Dir(old), name)
			checkLibraryIgnoreFile(t, old, "incoming original")
			return nil
		},
		syncFile: func(file *os.File) error {
			if file.Name() != filepath.Base(stagedPath) {
				return file.Sync()
			}
			checkLibraryIgnoreFile(t, old, "incoming original")
			checkLibraryIgnoreFile(t, stagedPath, "remote")
			if err := os.Chtimes(stagedPath, publisherTime, publisherTime); err != nil {
				return err
			}
			return stop
		},
	})
	if !errors.Is(err, stop) {
		t.Fatalf("did not reach staging sync: %v", err)
	}
	if !os.SameFile(oldInfo, fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)) {
		t.Fatal("staging failure removed original inode")
	}
	synced := false
	err = pair.run(t, "sync", libraryClientConfig{
		syncFile: func(file *os.File) error {
			if file.Name() == filepath.Base(stagedPath) {
				synced = true
				checkLibraryIgnoreFile(t, old, "incoming original")
			}
			return file.Sync()
		},
		beforeSyncRecoveryRename: func(path, _ string) error {
			if path != replacementNestedFile || !synced {
				t.Fatalf("capture %q started before staged file was synced", path)
			}
			checkLibraryIgnoreFile(t, old, "incoming original")
			checkLibraryIgnoreFile(t, stagedPath, "remote")
			return nil
		},
	})
	if err != nil || !synced {
		t.Fatalf("retry did not sync and reuse staged file: synced=%v err=%v", synced, err)
	}
	assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
}

func TestLibrarySyncFileReplacementRollsBackUncapturedLocalChanges(t *testing.T) {
	for _, change := range []string{"edit", "replace", "delete", "unbind interrupted staging"} {
		t.Run(change, func(t *testing.T) {
			pair := newFileReplacementTestPair(t)
			pair.publishReplacement(t)
			before := readTestBinding(t, pair.subscriberDir, pair.subscriberTree)
			path := filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile))
			stop := errors.New("interrupt after local write during staging")
			var edited os.FileInfo
			err := pair.run(t, "sync", libraryClientConfig{beforeCheckoutFileWrite: func(string, string) error {
				switch change {
				case "replace", "delete":
					if err := os.Remove(path); err != nil || change == "delete" {
						return err
					}
				}
				if err := os.WriteFile(path, []byte("local edit"), 0o600); err != nil {
					return err
				}
				edited = fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)
				if change == "unbind interrupted staging" {
					return stop
				}
				return nil
			}})
			if change == "unbind interrupted staging" {
				if !errors.Is(err, stop) {
					t.Fatalf("staging failure=%v", err)
				}
				if err := pair.run(t, "unbind", libraryClientConfig{}); err != nil {
					t.Fatalf("unbind with unstaged local edit: %v", err)
				}
			} else {
				if !errors.Is(err, errFileReplacementSourceChanged) {
					t.Fatalf("sync did not reject capture after local change: %v", err)
				}
				if got := readTestBinding(t, pair.subscriberDir, pair.subscriberTree); got != before {
					t.Fatalf("Sync Base advanced: %+v", got)
				}
			}
			if count := countClientRows(t, pair.subscriberDir, "pending_checkouts", pair.subscriberTree); count != 0 {
				t.Fatalf("uncaptured local change left %d pending checkouts: %v", count, err)
			}
			assertNoSyncInternalPaths(t, pair.subscriberTree)
			if change == "delete" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("local deletion was reverted: %v", err)
				}
			} else {
				checkLibraryIgnoreFile(t, path, "local edit")
				if !os.SameFile(edited, fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)) {
					t.Fatal("local inode was changed during rollback")
				}
			}
			if change != "unbind interrupted staging" {
				pair.syncSubscriber(t)
				assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
			}
		})
	}
}

func TestLibrarySyncFileReplacementMtimeDoesNotTouchReplacementDirectory(t *testing.T) {
	pair := newFileReplacementTestPair(t)
	pair.publishReplacement(t)
	parent := filepath.Join(pair.subscriberTree, "docs", "inbox")
	moved := filepath.Join(pair.subscriberTree, "docs", "moved")
	var original time.Time
	err := pair.run(t, "sync", libraryClientConfig{fsActionFault: func(point string, action fsAction) error {
		if point == "before_action" && action.Op == fsOpMtime && action.Phase == fsPhasePreBase && action.Source == "inbox" {
			if err := os.Rename(parent, moved); err != nil {
				return err
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				return err
			}
			mtime := time.Date(2024, 2, 1, 11, 12, 13, 0, time.UTC)
			if err := os.Chtimes(parent, mtime, mtime); err != nil {
				return err
			}
			original = fileReplacementTestStat(t, pair.subscriberTree, "docs/inbox").ModTime()
		}
		return nil
	}})
	if err == nil || !strings.Contains(err.Error(), "opened a different source identity") || original.IsZero() {
		t.Fatalf("mtime failed to check opened directory identity: %v", err)
	}
	if got := fileReplacementTestStat(t, pair.subscriberTree, "docs/inbox").ModTime(); !got.Equal(original) {
		t.Fatalf("unrelated replacement directory was touched: before=%v after=%v", original, got)
	}
}

func TestLibrarySyncFileReplacementRestartsDurableScope(t *testing.T) {
	for _, test := range []struct {
		name, point, phase, op, kind, role, target string
		finalized                                  bool
	}{
		{"capture", "after_action", fsPhasePreBase, fsOpRename, "File", "capture-file", "", false},
		{"install", "after_action", fsPhasePreBase, fsOpRename, "File", "", "incoming", false},
		{"recovery removal", "after_action", fsPhasePostBase, fsOpUnlink, "File", "", "", true},
		{"cleanup parent mtime", "after_action", fsPhasePostBase, fsOpMtime, "Directory", "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pair := newFileReplacementTestPair(t)
			before := fileReplacementTestStats(t, pair.subscriberTree, fileReplacementTestUntouched)
			oldBinding := readTestBinding(t, pair.subscriberDir, pair.subscriberTree)
			pair.publishReplacement(t)
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPublicSyncFSActionCrashHelper$")
			command.Env = append(os.Environ(), "FILECLOUD_PUBLIC_SYNC_HELPER=1",
				"FILECLOUD_PUBLIC_CRASH_CLIENT="+pair.subscriberDir, "FILECLOUD_PUBLIC_CRASH_WORKTREE="+pair.subscriberTree,
				"FILECLOUD_PUBLIC_CRASH_POINT="+test.point, "FILECLOUD_PUBLIC_CRASH_PHASE="+test.phase,
				"FILECLOUD_PUBLIC_CRASH_OP="+test.op, "FILECLOUD_PUBLIC_CRASH_KIND="+test.kind,
				"FILECLOUD_PUBLIC_CRASH_ROLE="+test.role, "FILECLOUD_PUBLIC_CRASH_TARGET="+test.target)
			assertProcessSIGKILL(t, command.Run())
			if got := readTestBinding(t, pair.subscriberDir, pair.subscriberTree); (got != oldBinding) != test.finalized {
				t.Fatalf("unexpected binding after interruption: %+v", got)
			}
			for table, want := range map[string]int{"checkout_file_replacements": 1, "checkout_paths": 3, "sync_recoveries": 1} {
				if count := countClientRows(t, pair.subscriberDir, table, pair.subscriberTree); count != want {
					t.Errorf("%s rows=%d want=%d", table, count, want)
				}
			}
			assertFileReplacementTestInodes(t, pair.subscriberTree, before, replacementNestedFile)
			pair.syncSubscriber(t)
			assertFileReplacementTestInodes(t, pair.subscriberTree, before, replacementNestedFile)
			assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
			pair.assertNoFollowupPublication(t)
		})
	}
}

func TestLibrarySyncFileReplacementCancelsUnexecutedCaptureAfterRestart(t *testing.T) {
	for _, test := range []struct {
		resume string
		atomic bool
	}{{"sync", false}, {"unbind", false}, {"sync", true}, {"unbind", true}} {
		name := test.resume
		if test.atomic {
			name += "/atomic save"
		}
		t.Run(name, func(t *testing.T) {
			pair := newFileReplacementTestPair(t)
			pair.publishReplacement(t)
			original := filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile))
			old := fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPublicSyncFSActionCrashHelper$")
			command.Env = append(os.Environ(), "FILECLOUD_PUBLIC_SYNC_HELPER=1",
				"FILECLOUD_PUBLIC_CRASH_CLIENT="+pair.subscriberDir, "FILECLOUD_PUBLIC_CRASH_WORKTREE="+pair.subscriberTree,
				"FILECLOUD_PUBLIC_CRASH_POINT=after_intent_commit", "FILECLOUD_PUBLIC_CRASH_PHASE="+fsPhasePreBase,
				"FILECLOUD_PUBLIC_CRASH_OP="+fsOpRename, "FILECLOUD_PUBLIC_CRASH_KIND=File", "FILECLOUD_PUBLIC_CRASH_ROLE=capture-file")
			assertProcessSIGKILL(t, command.Run())
			checkLibraryIgnoreFile(t, original, "incoming original")
			if test.atomic {
				temp := filepath.Join(filepath.Dir(original), "editor save")
				writeLibraryIgnoreFile(t, temp, "edit after capture intent")
				if err := os.Rename(temp, original); err != nil {
					t.Fatal(err)
				}
			} else {
				writeLibraryIgnoreFile(t, original, "edit after capture intent")
			}
			edited := fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)
			if os.SameFile(old, edited) == test.atomic {
				t.Fatal("staged local edit has unexpected inode")
			}
			err := pair.run(t, test.resume, libraryClientConfig{})
			if test.resume == "sync" && !errors.Is(err, errFileReplacementSourceChanged) || test.resume == "unbind" && err != nil {
				t.Fatalf("resume after interrupted capture intent: %v", err)
			}
			if count := countClientRows(t, pair.subscriberDir, "pending_checkouts", pair.subscriberTree); count != 0 {
				t.Fatalf("capture cancellation left %d pending checkout(s): %v", count, err)
			}
			checkLibraryIgnoreFile(t, original, "edit after capture intent")
			if !os.SameFile(edited, fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)) {
				t.Fatal("replay replaced locally edited inode")
			}
			assertNoSyncInternalPaths(t, pair.subscriberTree)
			if test.resume == "sync" {
				pair.syncSubscriber(t)
				assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
			}
		})
	}
}

func TestLibrarySyncFileReplacementResumesCleanupAfterParentMove(t *testing.T) {
	pair := newFileReplacementTestPair(t)
	pair.publishReplacement(t)
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPublicSyncFSActionCrashHelper$")
	command.Env = append(os.Environ(), "FILECLOUD_PUBLIC_SYNC_HELPER=1",
		"FILECLOUD_PUBLIC_CRASH_CLIENT="+pair.subscriberDir, "FILECLOUD_PUBLIC_CRASH_WORKTREE="+pair.subscriberTree,
		"FILECLOUD_PUBLIC_CRASH_POINT=after_intent_commit", "FILECLOUD_PUBLIC_CRASH_PHASE="+fsPhasePostBase,
		"FILECLOUD_PUBLIC_CRASH_OP="+fsOpMtime, "FILECLOUD_PUBLIC_CRASH_KIND=Directory")
	assertProcessSIGKILL(t, command.Run())
	if state := readPendingCheckoutState(t, pair.subscriberDir, pair.subscriberTree); state != "finalized" {
		t.Fatalf("interrupted state=%q want finalized", state)
	}
	original := filepath.Join(pair.subscriberTree, "docs", "inbox")
	moved := filepath.Join(pair.subscriberTree, "docs", "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	writeLibraryIgnoreFile(t, filepath.Join(original, "local"), "new local parent")
	before := fileReplacementTestStats(t, pair.subscriberTree, []string{"docs/inbox", "docs/inbox/local", "docs/moved", "docs/moved/incoming"})
	pair.syncSubscriber(t) // Finish the fixed transaction; don't mutate either directory.
	assertFileReplacementTestInodes(t, pair.subscriberTree, before, "")
	checkLibraryIgnoreFile(t, filepath.Join(moved, "incoming"), "remote")
	checkLibraryIgnoreFile(t, filepath.Join(original, "local"), "new local parent")
	assertNoSyncInternalPaths(t, pair.subscriberTree)
	if count := countClientRows(t, pair.subscriberDir, "pending_checkouts", pair.subscriberTree); count != 0 {
		t.Fatalf("moved parent left %d completed checkout(s) pending", count)
	}
}

func TestLibrarySyncFileReplacementRollsBackForConcurrentSiblingEdit(t *testing.T) {
	for _, stage := range []string{"before checkout", "before finalize", "inside final transaction"} {
		t.Run(stage, func(t *testing.T) {
			pair := newFileReplacementTestPair(t)
			sibling := filepath.Join(pair.subscriberTree, "docs", "inbox", "sibling")
			before := fileReplacementTestStats(t, pair.subscriberTree, append(slices.Clone(fileReplacementTestUntouched), replacementNestedFile))
			oldBinding := readTestBinding(t, pair.subscriberDir, pair.subscriberTree)
			pair.publishReplacement(t)
			editSibling := func() error { return os.WriteFile(sibling, []byte("concurrent local save"), 0o600) }
			var config libraryClientConfig
			switch stage {
			case "before checkout":
				config.beforeCheckoutMaterialize = editSibling
			case "before finalize":
				config.beforeFinalize = editSibling
			case "inside final transaction":
				config.fsTransactionFault = func(point string) error {
					if point == "before_base_commit" {
						return editSibling()
					}
					return nil
				}
			}
			err := pair.run(t, "sync", config)
			if err == nil {
				t.Fatal("expected fixed-target verification to reject concurrent local change")
			}
			if count := countClientRows(t, pair.subscriberDir, "pending_checkouts", pair.subscriberTree); count != 0 {
				t.Fatalf("rollback left %d fixed pending checkout(s); sync: %v", count, err)
			}
			if got := readTestBinding(t, pair.subscriberDir, pair.subscriberTree); got != oldBinding {
				t.Fatalf("advanced Sync Base on concurrent change: %+v", got)
			}
			checkLibraryIgnoreFile(t, sibling, "concurrent local save")
			checkLibraryIgnoreFile(t, filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile)), "incoming original")
			assertFileReplacementTestInodes(t, pair.subscriberTree, before, "docs/inbox/sibling")
			pair.syncSubscriber(t)
			checkLibraryIgnoreFile(t, sibling, "concurrent local save")
			checkLibraryIgnoreFile(t, filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile)), "remote")
			assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
		})
	}
}

func TestLibrarySyncFileReplacementPreservesUnownedSameContentTemp(t *testing.T) {
	pair := newFileReplacementTestPair(t)
	pair.publishReplacement(t)
	before := fileReplacementTestStats(t, pair.subscriberTree, append(slices.Clone(fileReplacementTestUntouched), replacementNestedFile))
	unowned := filepath.Join(platformTestTempDir(t), "unowned")
	writeLibraryIgnoreFile(t, unowned, "remote")
	unownedInfo, err := os.Stat(unowned)
	if err != nil {
		t.Fatal(err)
	}
	var candidate string
	err = pair.run(t, "sync", libraryClientConfig{fsActionFault: func(point string, action fsAction) error {
		if point == "between_create_identity" && action.Op == fsOpCreateFile {
			candidate = filepath.Join(pair.subscriberTree, filepath.FromSlash(action.Parent), action.Source)
			if err := os.Remove(candidate); err != nil {
				return err
			}
			return os.Rename(unowned, candidate)
		}
		return nil
	}})
	if err == nil || candidate == "" || !strings.Contains(err.Error(), "changed before identity was recorded") {
		t.Fatalf("checkout accepted unowned same-content temp: candidate=%q err=%v", candidate, err)
	}
	if err := pair.run(t, "unbind", libraryClientConfig{}); err == nil || !strings.Contains(err.Error(), "preserved unknown path") {
		t.Fatalf("unowned temporary path was not preserved on recovery: %v", err)
	}
	if err := pair.run(t, "unbind", libraryClientConfig{}); err != nil {
		t.Fatalf("retry rollback: %v", err)
	}
	assertFileReplacementTestInodes(t, pair.subscriberTree, before, replacementNestedFile)
	entries, err := os.ReadDir(filepath.Dir(candidate))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && os.SameFile(unownedInfo, info) {
			checkLibraryIgnoreFile(t, filepath.Join(filepath.Dir(candidate), entry.Name()), "remote")
			assertSyncStateCleared(t, pair.subscriberDir, pair.subscriberTree)
			return
		}
	}
	t.Fatal("original unowned inode was lost")
}

func TestLibrarySyncFileReplacementUnbindBeforeCapture(t *testing.T) {
	pair := newFileReplacementTestPair(t)
	before := scanTestRoot(t, pair.subscriberTree)
	pair.publishReplacement(t)
	stop := errors.New("stop after registering recovery")
	if err := pair.run(t, "sync", libraryClientConfig{afterSyncRecoveryRegistered: func(string) error { return stop }}); !errors.Is(err, stop) {
		t.Fatalf("interrupt registration: %v", err)
	}
	if err := pair.run(t, "unbind", libraryClientConfig{}); err != nil {
		t.Fatalf("rollback before capture: %v", err)
	}
	if got := scanTestRoot(t, pair.subscriberTree); got != before {
		t.Fatalf("rollback root=%s want=%s", got, before)
	}
	assertSyncStateCleared(t, pair.subscriberDir, pair.subscriberTree)
}

var fileReplacementTestUntouched = []string{"base", "docs", "docs/inbox", "docs/inbox/sibling", "docs/inbox/child", "docs/inbox/child/keep", "elsewhere", "elsewhere/keep"}

type fileReplacementTestPair struct {
	environment                                                libraryCLIEnvironment
	publisherDir, publisherTree, subscriberDir, subscriberTree string
}

func newFileReplacementTestPair(t *testing.T) fileReplacementTestPair {
	t.Helper()
	t.Setenv(syncignore.Env, "")
	environment, publisherDir, publisherTree, subscriberDir, subscriberTree, _, _ := newSyncPair(t)
	pair := fileReplacementTestPair{environment, publisherDir, publisherTree, subscriberDir, subscriberTree}
	for path, contents := range map[string]string{replacementNestedFile: "incoming original", "docs/inbox/sibling": "sibling original", "docs/inbox/child/keep": "nested", "elsewhere/keep": "unrelated"} {
		writeLibraryIgnoreFile(t, filepath.Join(publisherTree, filepath.FromSlash(path)), contents)
	}
	past := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	for _, path := range []string{"docs", "docs/inbox", replacementNestedFile} {
		if err := os.Chtimes(filepath.Join(publisherTree, filepath.FromSlash(path)), past, past); err != nil {
			t.Fatal(err)
		}
	}
	pair.syncPublisher(t)
	pair.syncSubscriber(t)
	return pair
}

func (p fileReplacementTestPair) syncPublisher(t *testing.T) {
	t.Helper()
	if err := syncTestWorktree(t, p.publisherDir, p.publisherTree); err != nil {
		t.Fatal(err)
	}
}

func (p fileReplacementTestPair) syncSubscriber(t *testing.T) {
	t.Helper()
	if err := syncTestWorktree(t, p.subscriberDir, p.subscriberTree); err != nil {
		t.Fatal(err)
	}
}

func (p fileReplacementTestPair) run(t *testing.T, command string, config libraryClientConfig) error {
	t.Helper()
	config.checkFilesystem = func(*os.File) error { return nil }
	return runLibraryWithConfig(t.Context(), []string{command, "--client-dir", p.subscriberDir, "--worktree", p.subscriberTree},
		strings.NewReader(""), io.Discard, io.Discard, config)
}

func (p fileReplacementTestPair) publishReplacement(t *testing.T) {
	t.Helper()
	writeLibraryIgnoreFile(t, filepath.Join(p.publisherTree, filepath.FromSlash(replacementNestedFile)), "remote")
	p.syncPublisher(t)
}

func (p fileReplacementTestPair) assertNoFollowupPublication(t *testing.T) {
	t.Helper()
	head := mustAcceptanceHead(t, p.environment)
	p.syncSubscriber(t)
	if got := mustAcceptanceHead(t, p.environment); got != head {
		t.Fatalf("cleanup made a clean worktree republish: before=%s after=%s", head, got)
	}
	for _, table := range []string{"pending_checkouts", "checkout_paths", "checkout_file_replacements", "sync_recoveries", "fs_actions"} {
		if count := countClientRows(t, p.subscriberDir, table, p.subscriberTree); count != 0 {
			t.Errorf("%s retained %d journal rows", table, count)
		}
	}
	assertNoSyncInternalPaths(t, p.subscriberTree)
}

func fileReplacementTestStat(t *testing.T, tree, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(filepath.Join(tree, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func fileReplacementTestStats(t *testing.T, tree string, paths []string) map[string]os.FileInfo {
	t.Helper()
	result := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		result[path] = fileReplacementTestStat(t, tree, path)
	}
	return result
}

func assertFileReplacementTestInodes(t *testing.T, tree string, before map[string]os.FileInfo, allowedMtimeChange string) {
	t.Helper()
	for path, prior := range before {
		after := fileReplacementTestStat(t, tree, path)
		if !os.SameFile(prior, after) {
			t.Errorf("unchanged path %q was replaced", path)
		}
		if path != allowedMtimeChange && !isCheckoutAncestor(path, allowedMtimeChange) && !after.ModTime().Equal(prior.ModTime()) {
			t.Errorf("unchanged path %q mtime changed: %v to %v", path, prior.ModTime(), after.ModTime())
		}
	}
}

func TestLibrarySyncFileReplacementPreservesSaveAfterInstall(t *testing.T) {
	for _, scenario := range []string{"in-place save", "atomic save", "delete", "resume applying"} {
		t.Run(scenario, func(t *testing.T) {
			pair := newFileReplacementTestPair(t)
			pair.publishReplacement(t)
			binding := readTestBinding(t, pair.subscriberDir, pair.subscriberTree)
			path := filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile))
			stop := errors.New("interrupted after target was saved")
			var saved os.FileInfo
			err := pair.run(t, "sync", libraryClientConfig{beforeFinalize: func() error {
				checkLibraryIgnoreFile(t, path, "remote")
				switch scenario {
				case "delete":
					return os.Remove(path)
				case "atomic save":
					temp := filepath.Join(filepath.Dir(path), "editor temporary file")
					writeLibraryIgnoreFile(t, temp, "post-install local save")
					if err := os.Rename(temp, path); err != nil {
						return err
					}
				default:
					writeLibraryIgnoreFile(t, path, "post-install local save")
				}
				saved = fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)
				if scenario == "resume applying" {
					return stop
				}
				return nil
			}})
			if err == nil {
				t.Fatal("changed checkout unexpectedly finalized")
			}
			if scenario == "resume applying" {
				if !errors.Is(err, stop) {
					t.Fatalf("interrupted checkout: %v", err)
				}
				if err = pair.run(t, "sync", libraryClientConfig{}); err == nil {
					t.Fatal("resumed checkout unexpectedly finalized")
				}
			}
			if count := countClientRows(t, pair.subscriberDir, "pending_checkouts", pair.subscriberTree); count != 0 {
				t.Fatalf("local save left %d checkout(s) pending: %v", count, err)
			}
			if got := readTestBinding(t, pair.subscriberDir, pair.subscriberTree); got != binding {
				t.Fatalf("Sync Base advanced after local change: %+v", got)
			}
			var copyPath string
			if scenario == "delete" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rollback restored a locally deleted path: %v", err)
				}
			} else {
				checkLibraryIgnoreFile(t, path, "incoming original")
				copyPath = findFileReplacementSave(t, filepath.Dir(path), "post-install local save")
				info, err := os.Stat(copyPath)
				if err != nil || !os.SameFile(info, saved) {
					t.Fatalf("visible local save did not retain its inode: %q %v", copyPath, err)
				}
			}
			assertNoSyncInternalPaths(t, pair.subscriberTree)
			pair.syncSubscriber(t)
			checkLibraryIgnoreFile(t, path, "remote")
			if copyPath != "" {
				checkLibraryIgnoreFile(t, copyPath, "post-install local save")
			}
			assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
		})
	}
}

func TestLibrarySyncFileReplacementReplaysSaveRenameAfterCollision(t *testing.T) {
	pair := newFileReplacementTestPair(t)
	pair.publishReplacement(t)
	path := filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile))
	stop := errors.New("interrupted after preserving rename intent")
	var occupied string
	err := pair.run(t, "sync", libraryClientConfig{
		beforeFinalize: func() error { return os.WriteFile(path, []byte("post-install save across crash"), 0o600) },
		fsActionFault: func(point string, action fsAction) error {
			if point == "after_intent_commit" && isFileReplacementSaveAction(action) {
				occupied = filepath.Join(filepath.Dir(path), action.Target)
				writeLibraryIgnoreFile(t, occupied, "occupied recovery name")
				return stop
			}
			return nil
		},
	})
	if !errors.Is(err, stop) || occupied == "" {
		t.Fatalf("preservation intent was not interrupted: %v", err)
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPublicSyncFSActionCrashHelper$")
	command.Env = append(os.Environ(), "FILECLOUD_PUBLIC_SYNC_HELPER=1",
		"FILECLOUD_PUBLIC_CRASH_CLIENT="+pair.subscriberDir, "FILECLOUD_PUBLIC_CRASH_WORKTREE="+pair.subscriberTree,
		"FILECLOUD_PUBLIC_CRASH_POINT=after_action", "FILECLOUD_PUBLIC_CRASH_PHASE="+fsPhaseRollback,
		"FILECLOUD_PUBLIC_CRASH_OP="+fsOpRename, "FILECLOUD_PUBLIC_CRASH_KIND=File")
	assertProcessSIGKILL(t, command.Run())
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash did not follow preservation rename: %v", err)
	}
	copyPath := findFileReplacementSave(t, filepath.Dir(path), "post-install save across crash")
	checkLibraryIgnoreFile(t, occupied, "occupied recovery name")
	pair.syncSubscriber(t)
	checkLibraryIgnoreFile(t, occupied, "occupied recovery name")
	checkLibraryIgnoreFile(t, copyPath, "post-install save across crash")
	checkLibraryIgnoreFile(t, path, "remote")
	pair.assertNoFollowupPublication(t)
	assertTestConverged(t, pair.environment, pair.subscriberDir, pair.subscriberTree)
}

func findFileReplacementSave(t *testing.T, parent, contents string) string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "Filecloud recovered ") {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		if data, err := os.ReadFile(path); err == nil && string(data) == contents {
			return path
		}
	}
	t.Fatalf("no visible copy contains %q", contents)
	return ""
}

func TestLibrarySyncFileReplacementRetainsReturnedInodeAfterRecoveryUnlink(t *testing.T) {
	pair := newFileReplacementTestPair(t)
	pair.publishReplacement(t)
	path := filepath.Join(pair.subscriberTree, filepath.FromSlash(replacementNestedFile))
	moved := filepath.Join(platformTestTempDir(t), "moved installed file")
	stop := errors.New("interrupted after removing the old recovery")
	var installed os.FileInfo
	err := pair.run(t, "sync", libraryClientConfig{
		beforeFinalize: func() error {
			installed = fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)
			return os.Rename(path, moved)
		},
		fsActionFault: func(point string, action fsAction) error {
			if point == "after_action" && action.Phase == fsPhaseRollback && action.Op == fsOpUnlink && strings.HasPrefix(action.Source, syncRecoveryPrefix) {
				return stop
			}
			return nil
		},
	})
	if !errors.Is(err, stop) {
		t.Fatalf("recovery unlink interruption: %v", err)
	}
	if err := os.Rename(moved, path); err != nil {
		t.Fatal(err)
	}
	err = pair.run(t, "sync", libraryClientConfig{syncEvent: func(point string) error {
		if point == "scan_completed" {
			return stop // Inspect after rollback, before a new checkout can run.
		}
		return nil
	}})
	if !errors.Is(err, stop) || countClientRows(t, pair.subscriberDir, "pending_checkouts", pair.subscriberTree) != 0 {
		t.Fatalf("rollback did not release the completed unlink: %v", err)
	}
	checkLibraryIgnoreFile(t, path, "remote")
	if !os.SameFile(installed, fileReplacementTestStat(t, pair.subscriberTree, replacementNestedFile)) {
		t.Fatal("rollback removed the returned installed inode")
	}
}
