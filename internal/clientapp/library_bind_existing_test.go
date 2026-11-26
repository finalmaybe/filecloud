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
	"strings"
	"sync/atomic"
	"testing"
)

func TestExistingFolderPreviewRejectsChangedHeadAndCaseCollision(t *testing.T) {
	state := newImportedBinding(t)
	var transfers atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut || strings.Contains(r.URL.Path, "/blocks/") {
			transfers.Add(1)
		}
		state.environment.handler.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	dir, tree := newClientPaths(t)
	local := filepath.Join(tree, "LOCAL")
	if err := os.WriteFile(local, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	args := bindArgs(dir, proxy.URL, testClientLibraryID, tree, testOtherDeviceID)
	var out bytes.Buffer
	run := func(extra ...string) error {
		out.Reset()
		return runTest(t.Context(), append(args, extra...), strings.NewReader(state.environment.token+"\n"), &out, io.Discard)
	}
	if err := run("--preview-merge"); !errors.Is(err, errMergeNameCollision) {
		t.Fatalf("case collision not reported: %v", err)
	}
	if err := os.Rename(local, filepath.Join(tree, "other")); err != nil {
		t.Fatal(err)
	}
	if err := run("--preview-merge"); err != nil {
		t.Fatal(err)
	}
	var plan folderMergePreview
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Upload.Count != 1 || plan.Download.Count != 1 || plan.Conflict.Count != 0 {
		t.Fatalf("unexpected preview: %+v", plan)
	}
	if err := os.WriteFile(filepath.Join(state.worktree, "local"), []byte("new remote content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runTest(t.Context(), []string{"library", "sync", "--client-dir", state.clientDir, "--worktree", state.worktree}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var stale staleSyncCandidate
	if err := run("--merge-existing", plan.Candidate); !errors.As(err, &stale) {
		t.Fatalf("stale confirmation: %v", err)
	}
	if transfers.Load() != 0 {
		t.Fatalf("preview or stale confirmation transferred file data or published a version")
	}
	if _, err := os.Stat(filepath.Join(dir, _clientDatabaseName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected binding database: %v", err)
	}
}
