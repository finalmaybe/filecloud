package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalWatchNestedAtomicSave(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	dir := filepath.Join(root, "nested")
	must(t, os.Mkdir(dir, 0700))
	w := &bindingWatch{worktree: root}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); w.watchLocal(ctx) }()
	defer func() { cancel(); <-done }()
	waitHint := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if w.changed(time.Now(), "") {
				w.localAt.Store(0)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("no local change hint")
	}
	waitHint() // recursive watch installed
	dir = filepath.Join(dir, "new folder")
	must(t, os.Mkdir(dir, 0700))
	waitHint()
	file := filepath.Join(dir, "draft")
	must(t, os.WriteFile(file, []byte("draft"), 0600))
	waitHint()
	final := filepath.Join(dir, "document")
	must(t, os.Rename(file, final))
	waitHint()
	must(t, os.Remove(final))
	waitHint()
}
