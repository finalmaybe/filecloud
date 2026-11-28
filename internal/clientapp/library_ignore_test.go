package clientapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/syncignore"
)

func setLibraryIgnoreRules(t *testing.T, contents string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "sync-ignore.json")
	writeLibraryIgnoreFile(t, file, contents)
	t.Setenv(syncignore.Env, file)
	return file
}

func writeLibraryIgnoreFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func checkLibraryIgnoreFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s: got %q, %v; want %q", path, data, err, want)
	}
}

func TestLibrarySyncIgnorePreservesTrackedRemoteAndRejoins(t *testing.T) {
	t.Setenv(syncignore.Env, "")
	env, aDir, aTree, bDir, bTree, _, _ := newSyncPair(t)
	sync := func(dir, tree string) {
		t.Helper()
		if err := syncTestWorktree(t, dir, tree); err != nil {
			t.Fatal(err)
		}
	}
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "nested", "draft.tmp"), "old")
	sync(bDir, bTree)
	sync(aDir, aTree)
	rules := setLibraryIgnoreRules(t, `{"SkipHidden":true,"Patterns":["*.tmp"]}`)
	sync(aDir, aTree) // Capture the baseline of an already tracked entry.
	t.Setenv(syncignore.Env, "")
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "nested", "draft.tmp"), "new remote draft")
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "nested", ".hidden"), "remote hidden")
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "nested", "remote.txt"), "remote visible")
	sync(bDir, bTree)

	t.Setenv(syncignore.Env, rules)
	writeLibraryIgnoreFile(t, filepath.Join(aTree, "nested", "local.txt"), "local visible")
	writeLibraryIgnoreFile(t, filepath.Join(aTree, "nested", "local.tmp"), "local only")
	sync(aDir, aTree) // Both sides changed: exercise merge and a retained checkout directory.
	checkLibraryIgnoreFile(t, filepath.Join(aTree, "nested", "draft.tmp"), "old")
	checkLibraryIgnoreFile(t, filepath.Join(aTree, "nested", "remote.txt"), "remote visible")
	if _, err := os.Stat(filepath.Join(aTree, "nested", ".hidden")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignored remote file materialized: %v", err)
	}
	writeLibraryIgnoreFile(t, filepath.Join(aTree, "nested", "local.txt"), "second visible edit")
	sync(aDir, aTree) // Direct push must also preserve ignored remote content.
	t.Setenv(syncignore.Env, "")
	sync(bDir, bTree)
	checkLibraryIgnoreFile(t, filepath.Join(bTree, "nested", "draft.tmp"), "new remote draft")
	checkLibraryIgnoreFile(t, filepath.Join(bTree, "nested", ".hidden"), "remote hidden")
	if _, err := os.Stat(filepath.Join(bTree, "nested", "local.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignored local file uploaded: %v", err)
	}

	sync(aDir, aTree) // Rejoin downloads stale/absent files and publishes local-only ones.
	checkLibraryIgnoreFile(t, filepath.Join(aTree, "nested", "draft.tmp"), "new remote draft")
	checkLibraryIgnoreFile(t, filepath.Join(aTree, "nested", ".hidden"), "remote hidden")
	sync(bDir, bTree)
	checkLibraryIgnoreFile(t, filepath.Join(bTree, "nested", "local.tmp"), "local only")
	assertTestConverged(t, env, aDir, aTree)
}

func TestLibrarySyncIgnoreRejoinsDirectoryByEffectiveChildren(t *testing.T) {
	for _, scenario := range []string{"hydrate stale and missing children", "publish a local-only child", "block unknown children with different content"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv(syncignore.Env, "")
			env, aDir, aTree, bDir, bTree, _, _ := newSyncPair(t)
			sync := func(dir, tree string) {
				t.Helper()
				if err := syncTestWorktree(t, dir, tree); err != nil {
					t.Fatal(err)
				}
			}
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "visible"), "old visible")
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "keep.tmp"), "old ignored")
			sync(bDir, bTree)
			sync(aDir, aTree)
			rules := setLibraryIgnoreRules(t, `{"Patterns":["folder","*.tmp"]}`)
			sync(aDir, aTree)
			t.Setenv(syncignore.Env, "")
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "visible"), "new visible")
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "keep.tmp"), "new ignored")
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "new", "child"), "remote child")
			sync(bDir, bTree)
			head := mustAcceptanceHead(t, env)

			t.Setenv(syncignore.Env, rules)
			sync(aDir, aTree)
			checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "visible"), "old visible")
			switch scenario {
			case "publish a local-only child":
				writeLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "new", "local"), "new local child")
			case "block unknown children with different content":
				writeLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "new", "child"), "unknown local child")
			}
			writeLibraryIgnoreFile(t, rules, `{"Patterns":["*.tmp"]}`)
			if scenario == "block unknown children with different content" {
				if err := syncTestWorktree(t, aDir, aTree); !errors.Is(err, errSyncIgnoreBlocked) {
					t.Fatalf("same-name unknown files: %v", err)
				}
				checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "new", "child"), "unknown local child")
				checkLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "new", "child"), "remote child")
				checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "visible"), "old visible")
			} else {
				sync(aDir, aTree)
				checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "visible"), "new visible")
				checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "new", "child"), "remote child")
			}
			checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "keep.tmp"), "old ignored")
			if scenario == "publish a local-only child" {
				t.Setenv(syncignore.Env, "")
				sync(bDir, bTree)
				checkLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "new", "local"), "new local child")
				checkLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "keep.tmp"), "new ignored")
			} else if got := mustAcceptanceHead(t, env); got != head {
				t.Fatalf("rejoining directory changed Head: %s → %s", head, got)
			}
		})
	}
}

func TestLibrarySyncIgnoreDoesNotRepublishDeletedDirectoryShell(t *testing.T) {
	for _, scenario := range []string{"stale shell", "new content", "new empty directory"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv(syncignore.Env, "")
			env, aDir, aTree, bDir, bTree, _, _ := newSyncPair(t)
			sync := func(dir, tree string) {
				t.Helper()
				if err := syncTestWorktree(t, dir, tree); err != nil {
					t.Fatal(err)
				}
			}
			keep := filepath.Join(aTree, "folder", "subfolder", "keep.tmp")
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "subfolder", "keep.tmp"), "old ignored")
			sync(bDir, bTree)
			sync(aDir, aTree)
			rules := setLibraryIgnoreRules(t, `{"Patterns":["*.tmp"]}`)
			sync(aDir, aTree)
			t.Setenv(syncignore.Env, "")
			if err := os.RemoveAll(filepath.Join(bTree, "folder")); err != nil {
				t.Fatal(err)
			}
			if err := syncTestWorktreeConfirmingDeletes(t, bDir, bTree); err != nil {
				t.Fatal(err)
			}
			head := mustAcceptanceHead(t, env)
			t.Setenv(syncignore.Env, rules)
			sync(aDir, aTree)
			checkLibraryIgnoreFile(t, keep, "old ignored")

			switch scenario {
			case "new content":
				writeLibraryIgnoreFile(t, filepath.Join(filepath.Dir(keep), "new"), "new local child")
			case "new empty directory":
				if err := os.RemoveAll(filepath.Join(aTree, "folder")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(aTree, "folder"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(syncignore.Env, "")
			sync(aDir, aTree)
			sync(bDir, bTree)
			for _, tree := range []string{aTree, bTree} {
				folder := filepath.Join(tree, "folder")
				if _, err := os.Stat(filepath.Join(folder, "subfolder", "keep.tmp")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted file resurrected in %s: %v", tree, err)
				}
				switch scenario {
				case "stale shell":
					if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("stale shell remains in %s: %v", tree, err)
					}
				case "new content":
					checkLibraryIgnoreFile(t, filepath.Join(folder, "subfolder", "new"), "new local child")
				case "new empty directory":
					if entries, err := os.ReadDir(folder); err != nil || len(entries) != 0 {
						t.Fatalf("new empty directory in %s: %v, %v", tree, entries, err)
					}
				}
			}
			if got := mustAcceptanceHead(t, env); scenario == "stale shell" && got != head {
				t.Fatalf("rejoining a stale shell changed Head: %s → %s", head, got)
			}
			assertTestConverged(t, env, aDir, aTree)
		})
	}
}

func TestLibrarySyncIgnoreRetainsDirectoryAndBlocksReplacement(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "delete parent"
		if replace {
			name = "replace parent with file"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(syncignore.Env, "")
			_, aDir, aTree, bDir, bTree, _, _ := newSyncPair(t)
			if err := os.Mkdir(filepath.Join(bTree, "folder"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "visible"), "old")
			if err := syncTestWorktree(t, bDir, bTree); err != nil {
				t.Fatal(err)
			}
			if err := syncTestWorktree(t, aDir, aTree); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(bTree, "folder")); err != nil {
				t.Fatal(err)
			}
			if replace {
				writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder"), "replacement")
			}
			if err := syncTestWorktreeConfirmingDeletes(t, bDir, bTree); err != nil {
				t.Fatal(err)
			}
			setLibraryIgnoreRules(t, `{"Patterns":["*.tmp"]}`)
			keep := filepath.Join(aTree, "folder", "local.tmp")
			writeLibraryIgnoreFile(t, keep, "keep")
			before, err := os.Stat(filepath.Dir(keep))
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = runTest(t.Context(), []string{"library", "sync", "--client-dir", aDir, "--worktree", aTree, "--output", "jsonl"}, strings.NewReader(""), &output, io.Discard)
			if replace {
				if err == nil || !strings.Contains(output.String(), `"code":"ignore_blocked"`) {
					t.Fatalf("replacement: %v; %s", err, output.String())
				}
				checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "visible"), "old")
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(aTree, "folder", "visible")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("remote deletion not applied: %v", err)
				}
			}
			checkLibraryIgnoreFile(t, keep, "keep")
			after, err := os.Stat(filepath.Dir(keep))
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("ignored parent identity changed: %v", err)
			}
		})
	}
}

func TestLibraryBindIgnoreOnlyFolderAndMissingRulesFile(t *testing.T) {
	file := setLibraryIgnoreRules(t, `{"SkipHidden":true,"Patterns":["scratch"]}`)
	env, bDir, bTree, _, _ := newEmptySyncPair(t)
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "remote.txt"), "remote")
	t.Setenv(syncignore.Env, "")
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "nested", "scratch", "remote"), "ignored remote subtree")
	if err := syncTestWorktree(t, bDir, bTree); err != nil {
		t.Fatal(err)
	}
	clientDir, tree := newClientPaths(t)
	keep := filepath.Join(tree, "nested", "scratch", ".local")
	writeLibraryIgnoreFile(t, keep, "local")
	t.Setenv(syncignore.Env, file)
	if err := runTest(t.Context(), bindArgs(clientDir, env.server.URL, testClientLibraryID, tree, testOtherDeviceID), strings.NewReader(env.token+"\n"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	checkLibraryIgnoreFile(t, keep, "local")
	checkLibraryIgnoreFile(t, filepath.Join(tree, "remote.txt"), "remote")
	if _, err := os.Stat(filepath.Join(tree, "nested", "scratch", "remote")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignored child was downloaded: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := syncTestWorktree(t, bDir, bTree); err != nil {
		t.Fatalf("missing rules file: %v", err)
	}
	writeLibraryIgnoreFile(t, file, `{"Patterns":["[bad"]}`)
	if err := syncTestWorktree(t, bDir, bTree); err == nil {
		t.Fatal("invalid rules accepted")
	}
}

func TestLibrarySyncIgnorePendingCheckoutRulesAndNestedConflict(t *testing.T) {
	t.Setenv(syncignore.Env, "")
	_, aDir, aTree, bDir, bTree, _, _ := newSyncPair(t)
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "visible"), "base")
	for _, pair := range [][2]string{{bDir, bTree}, {aDir, aTree}} {
		if err := syncTestWorktree(t, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	writeLibraryIgnoreFile(t, filepath.Join(bTree, "folder", "visible"), "remote")
	if err := syncTestWorktree(t, bDir, bTree); err != nil {
		t.Fatal(err)
	}
	writeLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "visible"), "local")
	writeLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "keep.tmp"), "keep")
	const firstRules = `{"Patterns":["*.tmp"]}`
	rules := setLibraryIgnoreRules(t, firstRules)
	args := []string{"sync", "--client-dir", aDir, "--worktree", aTree}
	interrupted := errors.New("interrupt before nested checkout materialization")
	err := runLibraryWithConfig(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard, libraryClientConfig{
		checkFilesystem: func(*os.File) error { return nil }, beforeCheckoutMaterialize: func() error { return interrupted },
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("checkout interruption: %v", err)
	}
	writeLibraryIgnoreFile(t, rules, `{"Patterns":["visible"]}`)
	if err := syncTestWorktree(t, aDir, aTree); err != nil {
		t.Fatalf("resume interrupted checkout with its saved rules: %v", err)
	}
	checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "visible"), "remote")
	checkLibraryIgnoreFile(t, filepath.Join(aTree, "folder", "keep.tmp"), "keep")
	entries, err := filepath.Glob(filepath.Join(aTree, "folder", "visible (Filecloud conflict *)"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("nested conflict paths = %v, %v", entries, err)
	}
	checkLibraryIgnoreFile(t, entries[0], "local")
}

func TestLibrarySyncIgnoreDiscardsPendingDeletion(t *testing.T) {
	t.Setenv(syncignore.Env, "")
	env, aDir, aTree, _, _, _, _ := newSyncPair(t)
	file := filepath.Join(aTree, "draft.tmp")
	writeLibraryIgnoreFile(t, file, "already tracked")
	if err := syncTestWorktree(t, aDir, aTree); err != nil {
		t.Fatal(err)
	}
	head := mustAcceptanceHead(t, env)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	var deletion *deleteConfirmationRequiredError
	if err := syncTestWorktree(t, aDir, aTree); !errors.As(err, &deletion) {
		t.Fatalf("create pending deletion: %v", err)
	}
	setLibraryIgnoreRules(t, `{"Patterns":["*.tmp"]}`)
	err := confirmTestDeletion(t, aDir, aTree, deletion.candidate[:deleteCandidatePrefixLen], libraryClientConfig{})
	var stale staleSyncCandidate
	if !errors.As(err, &stale) {
		t.Fatalf("stale deletion candidate: %v", err)
	}
	if err := syncTestWorktree(t, aDir, aTree); err != nil {
		t.Fatal(err)
	}
	if got := mustAcceptanceHead(t, env); got != head {
		t.Fatalf("ignored deletion changed Head: %s → %s", head, got)
	}
}

func TestLibraryWatchUsesSyncIgnoreRules(t *testing.T) {
	t.Setenv(syncignore.Env, "")
	_, aDir, aTree, bDir, bTree, _, _ := newSyncPair(t)
	setLibraryIgnoreRules(t, `{"Patterns":["*.tmp"]}`)
	writeLibraryIgnoreFile(t, filepath.Join(aTree, "local.tmp"), "ignored")
	writeLibraryIgnoreFile(t, filepath.Join(aTree, "visible"), "synced")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := runLibraryWithConfig(ctx, []string{"watch", "--client-dir", aDir, "--worktree", aTree, "--interval", "1h"}, strings.NewReader(""), io.Discard, io.Discard, libraryClientConfig{
		checkFilesystem: func(*os.File) error { return nil },
		syncEvent: func(event string) error {
			if event == "scan_completed" {
				cancel()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(syncignore.Env, "")
	if err := syncTestWorktree(t, bDir, bTree); err != nil {
		t.Fatal(err)
	}
	checkLibraryIgnoreFile(t, filepath.Join(bTree, "visible"), "synced")
	if _, err := os.Stat(filepath.Join(bTree, "local.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("watch uploaded ignored file: %v", err)
	}
}
