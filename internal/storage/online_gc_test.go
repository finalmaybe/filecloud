package storage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

func TestOnlineGCRetriesOnlyBusyOwnerLibrary(t *testing.T) {
	dir, old, _ := newGarbageCollectionFixture(t)
	ctx := t.Context()
	s, err := OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeObjectStore(t, s)
	// Both owners have the same library UUID and the same orphan block.
	id := object.ID(old["blocks"])
	pathA := gcTestObjectPath(dir, _gcTestOwnerA, _gcTestLibrary, "blocks", id)
	pathB := gcTestObjectPath(dir, _gcTestOwnerB, _gcTestLibrary, "blocks", id)
	if err := os.MkdirAll(filepath.Dir(pathB), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, old["blocks"], 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{pathA, pathB} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	round, err := s.NewOnlineGCRound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := s.UseLibrary(_gcTestOwnerA, _gcTestLibrary)
	report, err := round.Run(ctx)
	done()
	if err != nil || report.Completed != 1 || report.Deferred != 1 {
		t.Fatal(report, err)
	}
	if _, err = os.Stat(pathA); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(pathB); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("other owner's orphan still present", err)
	}
	report, err = round.Run(ctx)
	if err != nil || report.Completed != 1 || report.Deferred != 0 {
		t.Fatal(report, err)
	}
	if _, err = os.Stat(pathA); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("busy library not retried", err)
	}
}

func TestOnlineGCDedupGraceSurvivesRestart(t *testing.T) {
	dir, old, _ := newGarbageCollectionFixture(t)
	ctx := t.Context()
	s, err := OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	for kind, data := range old {
		if err := os.Chtimes(gcTestObjectPath(dir, _gcTestOwnerA, _gcTestLibrary, kind, object.ID(data)), aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	done := s.UseTransfer(_gcTestOwnerA, _gcTestLibrary)
	found, err := s.CheckForUpload(ctx, _gcTestOwnerA, _gcTestLibrary, "blocks", object.ID(old["blocks"]))
	done()
	if err != nil || !found {
		t.Fatal(found, err)
	}
	data := old["files"]
	if created, err := s.PutObject(ctx, _gcTestOwnerA, _gcTestLibrary, "files", object.ID(data), bytes.NewReader(data)); err != nil || created {
		t.Fatal(created, err)
	}
	if _, _, err = s.collectIdleLibrary(ctx, gcScope{_gcTestOwnerA, _gcTestLibrary}, time.Now()); !errors.Is(err, errGCYield) {
		t.Fatal("did not protect gap between transfers", err)
	}
	closeObjectStore(t, s)
	s, err = OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeObjectStore(t, s)
	if _, _, err := s.collectIdleLibrary(ctx, gcScope{_gcTestOwnerA, _gcTestLibrary}, time.Now().Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for kind, data := range old {
		want := kind == "files" || kind == "blocks"
		ok, err := s.HasObject(ctx, _gcTestOwnerA, _gcTestLibrary, kind, object.ID(data))
		if err != nil || want != ok {
			t.Fatal(kind, ok, err)
		}
	}
}

// Pause a real sweep between batches. The next publication uses an aged block
// from the remaining plan; it must finish without waiting for the background fsync.
func TestOnlineGCDiscardsStalePlanOnPublication(t *testing.T) {
	ctx := t.Context()
	dir, _, _ := newGarbageCollectionFixture(t)
	s, err := OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := gcScope{_gcTestOwnerA, _gcTestLibrary}
	now := time.Now()
	aged := now.Add(-48 * time.Hour)
	values := map[string][]byte{}
	var ids []string
	for i := range 40 {
		data := []byte(fmt.Sprintf("batch-%02d", i))
		id := object.ID(data)
		values[id] = data
		ids = append(ids, id)
		if _, err := s.PutObject(ctx, scope.owner, scope.library, "blocks", id, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(s.objectPath(scope.owner, scope.library, "blocks", id), aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(ids)
	keep := ids[len(ids)-1]
	closeObjectStore(t, s)
	s, err = OpenForServe(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeObjectStore(t, s)
	paused := make(chan struct{})
	resume := make(chan struct{})
	var first atomic.Bool
	s.syncObjectDirectory = func(path string) error {
		if first.CompareAndSwap(false, true) {
			close(paused)
			<-resume
		}
		return syncDirectory(path)
	}
	result := make(chan error, 1)
	go func() { _, _, err := s.collectIdleLibrary(ctx, scope, now); result <- err }()
	select {
	case <-paused:
	case err := <-result:
		t.Fatalf("no batch: %v", err)
	}
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	done := s.UseLibrary(scope.owner, scope.library)
	l, err := s.GetLibrary(ctx, scope.owner, scope.library)
	if err != nil {
		t.Fatal(err)
	}
	graph := putGCTestGraph(t, s, scope.owner, scope.library, values[keep], []string{*l.HeadCommitID}, now, false)
	if _, err = s.UpdateLibraryHead(ctx, scope.owner, scope.library, l.HeadCommitID, l.HeadVersion, graph.commitID, nil, now); err != nil {
		t.Fatal(err)
	}
	done()
	close(resume)
	if err := <-result; !errors.Is(err, errGCYield) {
		t.Fatal("kept stale plan", err)
	}
	if _, _, err := s.collectIdleLibrary(ctx, scope, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ok, err := s.HasObject(ctx, scope.owner, scope.library, "blocks", keep)
	if err != nil || !ok {
		t.Fatal("lost newly published block", err)
	}
	for _, id := range ids[:len(ids)-1] {
		if ok, err := s.HasObject(ctx, scope.owner, scope.library, "blocks", id); err != nil || ok {
			t.Fatal("missed orphan after retry", err)
		}
	}
}

func TestOnlineGCCrashBeforeDirectorySync(t *testing.T) {
	if dir := os.Getenv("FILECLOUD_ONLINE_GC_CRASH_DIR"); dir != "" {
		s, err := OpenForServe(t.Context(), dir)
		if err != nil {
			t.Fatal(err)
		}
		s.syncObjectDirectory = func(string) error { return killObjectTestProcess() }
		_, _, err = s.collectIdleLibrary(t.Context(), gcScope{_gcTestOwnerA, _gcTestLibrary}, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
		t.Fatalf("missed crash point: %v", err)
	}
	dir, old, kept := newGarbageCollectionFixture(t)
	seed, err := OpenForServe(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	aged := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	for i := range 40 {
		data := []byte(fmt.Sprintf("interrupted-%d", i))
		id := object.ID(data)
		if _, err := seed.PutObject(t.Context(), _gcTestOwnerA, _gcTestLibrary, "blocks", id, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		p := seed.objectPath(_gcTestOwnerA, _gcTestLibrary, "blocks", id)
		if err := os.Chtimes(p, aged, aged); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	closeObjectStore(t, seed)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOnlineGCCrashBeforeDirectorySync$")
	cmd.Env = append(os.Environ(), "FILECLOUD_ONLINE_GC_CRASH_DIR="+dir)
	assertObjectPublicationSIGKILL(t, cmd.Run())
	remaining := 0
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			remaining++
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if remaining == 0 || remaining == len(paths) {
		t.Fatalf("expected partial sweep, %d of %d still on disk", remaining, len(paths))
	}
	s, err := OpenForServe(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeObjectStore(t, s)
	_, _, err = s.collectIdleLibrary(t.Context(), gcScope{_gcTestOwnerA, _gcTestLibrary}, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	assertGCObjects(t, dir, old, false)
	assertPreservedGCObjects(t, kept)
	for _, p := range paths {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed to resume", err)
		}
	}
}
