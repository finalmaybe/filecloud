package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// Set both paths to exercise the bundled CLI and a real, disposable server on the host filesystem.
func TestDesktopTwoDevices(t *testing.T) {
	cli, server := os.Getenv("FILECLOUD_DESKTOP_CLI"), os.Getenv("FILECLOUD_DESKTOP_SERVER")
	if cli == "" || server == "" {
		t.Skip("set FILECLOUD_DESKTOP_CLI and FILECLOUD_DESKTOP_SERVER")
	}
	keyring.MockInit()
	root, err := os.MkdirTemp(".", ".desktop-acceptance-")
	must(t, err)
	defer os.RemoveAll(root)
	root, err = filepath.Abs(root)
	must(t, err)
	data := filepath.Join(root, "data")
	pass := newUUID()
	run := func(stdin string, args ...string) {
		c := exec.Command(server, args...)
		c.Stdin = strings.NewReader(stdin)
		must(t, c.Run())
	}
	run("", "init", "--data-dir", data)
	run(pass, "user", "add", "--data-dir", data, "--username", "desktop-test", "--password-stdin")
	defaults := Policy{TrashDays: 47, HistoryDays: 121}
	process := exec.Command(server, "serve", "--data-dir", data, "--listen", "127.0.0.1:0",
		"--trash-days", fmt.Sprint(defaults.TrashDays), "--history-days", fmt.Sprint(defaults.HistoryDays))
	out, err := process.StdoutPipe()
	must(t, err)
	must(t, process.Start())
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	must(t, err)
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "listening on ")
	if !ok {
		t.Fatal("no server address")
	}
	var build struct{ Version, Commit string }
	raw, err := exec.Command(cli, "version").Output()
	must(t, err)
	must(t, json.Unmarshal(raw, &build))
	if _, err := newWorkspace(filepath.Join(root, "mismatch"), cli, "wrong", build.Commit); err == nil {
		t.Fatal("accepted mismatched companion")
	}
	makeDevice := func(n string) *Workspace {
		s, err := newWorkspace(filepath.Join(root, n), cli, build.Version, build.Commit)
		must(t, err)
		must(t, s.CheckServer("http://"+addr))
		must(t, s.Login("http://"+addr, "desktop-test", pass, n))
		return s
	}
	a := makeDevice("a")
	must(t, a.CreateLibrary("资料库", 0, 0))
	b := makeDevice("b")
	if len(b.Snapshot().Libraries) != 1 {
		t.Fatal("missing remote library")
	}
	id := b.Snapshot().Libraries[0].ID
	bind := func(s *Workspace, name string) string {
		path := filepath.Join(root, name)
		must(t, os.Mkdir(path, 0700))
		s.pick = func() (string, error) { return path, nil }
		p, err := s.PickDirectory(id)
		must(t, err)
		probe := filepath.Join(path, "check.txt")
		must(t, os.WriteFile(probe, []byte("existing"), 0600))
		check, err := s.CheckDirectory(id)
		must(t, err)
		if check.Empty || check.LocalItems != 1 || check.Published != (s == b) {
			t.Fatal("directory import/download branch did not match actual state")
		}
		must(t, os.Remove(probe))
		check, err = s.CheckDirectory(id)
		must(t, err)
		if check.Path != path || !check.Empty || check.Filesystem == "" {
			t.Fatal("incorrect directory preflight")
		}
		must(t, s.Bind(id, p, false))
		return p
	}
	pa := bind(a, "work a")
	pb := bind(b, "work b")
	for i := range 10 {
		must(t, os.WriteFile(filepath.Join(pa, fmt.Sprint(i)), []byte("sync data"), 0600))
	}
	t.Run("event-driven sync", func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		var workers sync.WaitGroup
		defer func() { stop(); workers.Wait() }()
		for _, device := range []*Workspace{a, b} {
			workers.Go(func() { device.runAutoSync(ctx) })
		}
		waitFile := func(device *Workspace, dir, name, want string) {
			t.Helper()
			deadline := time.Now().Add(10 * time.Second) // shorter than one fallback round
			for time.Now().Before(deadline) {
				device.mu.Lock()
				buf, err := os.ReadFile(filepath.Join(dir, name))
				device.mu.Unlock()
				if err == nil && string(buf) == want {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatalf("%s did not arrive before the fallback poll", name)
		}
		waitFile(b, pb, "0", "sync data")
		// The startup sync has completed on both ends. Neither edit calls sync manually.
		saved := time.Now()
		must(t, os.WriteFile(filepath.Join(pb, "0"), []byte("edited on B"), 0600))
		waitFile(a, pa, "0", "edited on B")
		t.Logf("local edit to second device: %s", time.Since(saved).Round(time.Millisecond))
		must(t, a.SetPaused(id, true))
		must(t, os.WriteFile(filepath.Join(pa, "0"), []byte("sync data"), 0600))
		last := a.Snapshot().Libraries[0].LastSuccess
		a.tick(ctx, time.Now().Add(fallbackSyncInterval))
		if a.Snapshot().Libraries[0].LastSuccess != last {
			t.Fatal("paused binding ran a sync")
		}
		must(t, a.SetPaused(id, false))
		waitFile(b, pb, "0", "sync data")
	})
	must(t, os.Remove(filepath.Join(pa, "0")))
	if err := a.RequestSync(id); err == nil {
		t.Fatal("protected deletion not stopped")
	}
	review, err := a.ReviewDeletion(id)
	must(t, err)
	if review.Deleted != 1 || review.Tracked != 10 {
		t.Fatal("incorrect deletion impact")
	}
	if err := a.Unbind(id); err == nil {
		t.Fatal("unbound a protected deletion")
	}
	must(t, b.RequestSync(id))
	_, err = os.Stat(filepath.Join(pb, "0"))
	must(t, err)
	wrong := *review
	wrong.Candidate = "000000000000"
	if err := a.ConfirmDeletion(wrong); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	a, err = newWorkspace(filepath.Join(root, "a"), cli, build.Version, build.Commit)
	must(t, err)
	if a.Snapshot().Libraries[0].Pending != "delete-confirmation" || len(a.Snapshot().Activity) == 0 {
		t.Fatal("lost pending state or activity on reopen")
	}
	t.Run("retention defaults after restart", func(t *testing.T) {
		before := a.Snapshot()
		if before.DefaultTrashDays != 0 || before.DefaultHistoryDays != 0 || before.CatalogFresh {
			t.Fatal("expected a not-yet-refreshed catalog after restart")
		}
		got, err := a.RetentionDefaults()
		must(t, err)
		if got != defaults {
			t.Fatalf("server defaults = %+v, want %+v", got, defaults)
		}
		if a.Snapshot() != before {
			t.Fatal("reading defaults changed the workspace view")
		}
	})
	review, err = a.ReviewDeletion(id)
	must(t, err)
	a.CancelReview()
	if a.ConfirmDeletion(*review) == nil {
		t.Fatal("cancelled review could be submitted")
	}
	review, err = a.ReviewDeletion(id)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(pa, "1"), []byte("changed after review"), 0600))
	if err := a.ConfirmDeletion(*review); err == nil || err.Error() != "stale_candidate" {
		t.Fatalf("changed worktree: %v", err)
	}
	if a.RequestSync(id) == nil {
		t.Fatal("new candidate did not require confirmation")
	}
	next, err := a.ReviewDeletion(id)
	must(t, err)
	if next.Candidate == review.Candidate {
		t.Fatal("changed candidate reused its approval")
	}
	must(t, a.ConfirmDeletion(*next))
	must(t, b.RequestSync(id))
	if _, err := os.Stat(filepath.Join(pb, "0")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmed deletion not received")
	}
	totals, err := a.LibraryStats(id)
	must(t, err)
	if !totals.Published || totals.Items != "9" {
		t.Fatalf("incorrect published item count: %s", totals.Items)
	}
	h, err := a.History(id, "")
	must(t, err)
	if len(h.Commits) < 2 {
		t.Fatal("missing mainline history")
	}
	// A historical preview must never authorize a different candidate, including after cancellation.
	old := h.Commits[1].CommitId
	files, err := a.Files(id, old, ".", 0)
	must(t, err)
	if files.CommitId != old || len(files.Entries) != 10 {
		t.Fatal("snapshot selection drifted")
	}
	preview, err := a.PreviewFile(id, old, "0")
	must(t, err)
	if preview.Media != "text/plain" || preview.Content != "sync data" {
		t.Fatal("preview does not match selected snapshot")
	}
	impact, err := a.PreviewRestore(id, old, "0")
	must(t, err)
	if impact == nil || impact.Created != 1 || impact.SourcePath != "0" {
		t.Fatal("missing restore impact")
	}
	must(t, a.FinishRestore(*impact, false))
	if a.FinishRestore(*impact, true) == nil {
		t.Fatal("cancelled restore was accepted")
	}
	impact, err = a.PreviewRestore(id, old, "0")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(pa, "1"), []byte("edit during preview"), 0600))
	if err := a.FinishRestore(*impact, true); !errors.Is(err, commandError("stale_candidate")) {
		t.Fatalf("changed restore: %v", err)
	}
	must(t, a.RequestSync(id))
	impact, err = a.PreviewRestore(id, old, "0")
	must(t, err)
	must(t, a.FinishRestore(*impact, true))
	must(t, b.RequestSync(id))
	if raw, err := os.ReadFile(filepath.Join(pb, "0")); err != nil || string(raw) != "sync data" {
		t.Fatal("restored file did not converge")
	}
	bin, err := a.Trash(id, 0)
	must(t, err)
	if len(bin.Entries) == 0 {
		t.Fatal("missing deleted file")
	}
	entry := bin.Entries[0]
	for target, want := range map[string]bool{"0": false, "0/child": false, "new/child": true} {
		got, err := a.CheckRestoreLocation(id, target)
		must(t, err)
		if got != want {
			t.Fatal("incorrect restore preflight")
		}
	}
	if err := a.RestoreTrash(id, entry.ID, "0"); err == nil {
		t.Fatal("trash restore replaced existing file")
	}
	must(t, a.RestoreTrash(id, entry.ID, "renamed"))
	free, err := a.CheckRestoreLocation(id, "RENAMED")
	must(t, err)
	if free {
		t.Fatal("case-folded collision was marked available")
	}
	policy := Policy{TrashDays: 30, HistoryDays: 1}
	change, err := a.Retention(id, policy, false, "")
	must(t, err)
	if change.Token == "" {
		t.Fatal("shortened retention without preview")
	}
	if _, err := a.Retention(id, policy, true, "wrong"); err == nil {
		t.Fatal("accepted stale retention approval")
	}
	_, err = a.Retention(id, policy, true, change.Token)
	must(t, err)
	// Compare only real, journal-reported conflict copies; a comparison never edits the worktree.
	must(t, a.RequestSync(id))
	must(t, b.RequestSync(id))
	must(t, os.WriteFile(filepath.Join(pa, "1"), []byte("remote text"), 0600))
	must(t, os.WriteFile(filepath.Join(pb, "1"), []byte("local text"), 0600))
	must(t, a.RequestSync(id))
	must(t, b.RequestSync(id))
	copies := b.Snapshot().Libraries[0].Conflicts
	if len(copies) != 1 {
		t.Fatalf("expected one preserved conflict, got %d", len(copies))
	}
	for side, want := range map[bool]string{false: "remote text", true: "local text"} {
		v, err := b.ConflictPreview(id, copies[0], side)
		must(t, err)
		if v.Content != want {
			t.Fatal("incorrect conflict side")
		}
	}
	must(t, b.DismissConflict(id, copies[0]))
	if _, err := os.Stat(filepath.Join(pb, filepath.FromSlash(copies[0].Target))); err != nil {
		t.Fatal("dismissing a comparison removed its file")
	}
	info, err := b.LibraryInfo(id)
	must(t, err)
	versions, err := b.FileVersions(id, info.HeadCommitId, "1", "")
	must(t, err)
	if len(versions.Versions) < 2 {
		t.Fatal("missing historical file version")
	}
	past := versions.Versions[len(versions.Versions)-1]
	newHead, err := b.RestoreVersion(id, past.CommitId, "1", info.HeadCommitId)
	must(t, err)
	if newHead == info.HeadCommitId {
		t.Fatal("file restore did not publish")
	}
	if _, err := b.RestoreVersion(id, past.CommitId, "1", info.HeadCommitId); err == nil {
		t.Fatal("accepted stale file-version anchor")
	}
	// Removing a connection keeps the folder. Reusing an identical folder must not publish a version.
	must(t, a.RequestSync(id))
	a.pick = func() (string, error) { return pa, nil }
	checkOriginal := func() *FolderMerge {
		t.Helper()
		p, err := a.PickDirectory(id)
		must(t, err)
		if p != pa {
			t.Fatal("selected another directory")
		}
		check, err := a.CheckDirectory(id)
		must(t, err)
		if check.Merge == nil {
			t.Fatal("no existing folder preview")
		}
		return check.Merge
	}
	must(t, a.Unbind(id))
	if !checkOriginal().Identical {
		t.Fatal("unchanged folder did not match")
	}
	if err := a.Bind(id, pa, false); err == nil {
		t.Fatal("merged without consent")
	}
	must(t, a.Bind(id, pa, true))
	unchanged, err := a.LibraryInfo(id)
	must(t, err)
	if unchanged.HeadCommitId != newHead {
		t.Fatal("unchanged folder published a version")
	}
	must(t, a.Unbind(id))
	must(t, b.RequestSync(id))
	originalRemote, err := os.ReadFile(filepath.Join(pb, "1"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(pb, "remote-only"), []byte("from remote"), 0600))
	must(t, b.RequestSync(id))
	must(t, os.WriteFile(filepath.Join(pa, "1"), []byte("from local"), 0600))
	must(t, os.WriteFile(filepath.Join(pa, "local-only"), []byte("new"), 0600))
	if merge := checkOriginal(); merge.Upload.Count != 1 || merge.Download.Count != 1 || merge.Conflict.Count != 1 {
		t.Fatalf("unexpected merge impact: %+v", merge)
	}
	must(t, os.WriteFile(filepath.Join(pa, "local-only"), []byte("revised"), 0600))
	if err := a.Bind(id, pa, true); !errors.Is(err, commandError("stale_candidate")) {
		t.Fatalf("accepted or misreported changed folder: %v", err)
	}
	if a.bound[id].Phase != "" {
		t.Fatal("stale preview established a binding")
	}
	checkOriginal()
	must(t, a.Bind(id, pa, true))
	must(t, b.RequestSync(id))
	pairs := a.Snapshot().Libraries[0].Conflicts
	if len(pairs) != 1 || pairs[0].Source != "1" {
		t.Fatalf("missing merge conflict: %+v", pairs)
	}
	for _, dir := range []string{pa, pb} {
		for name, want := range map[string]string{"1": string(originalRemote), "local-only": "revised", "remote-only": "from remote", pairs[0].Target: "from local"} {
			got, err := os.ReadFile(filepath.Join(dir, name))
			must(t, err)
			if string(got) != want {
				t.Fatalf("wrong merged content at %s", name)
			}
		}
	}
	must(t, a.SetPaused(id, true))
	must(t, a.Unbind(id))
	must(t, a.Logout())
	if a.Snapshot().SignedIn || len(a.Snapshot().Libraries) != 0 {
		t.Fatal("local data visible after logout")
	}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestLocalDocumentAndReveal(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	s := &Workspace{active: account{UserID: "owner"}, bound: map[string]library{"lib": {Phase: "bound", Worktree: dir}}}
	var opened string
	s.open = func(p string) error { opened = p; return nil }
	s.reveal = s.open
	for _, name := range []string{"note.md", "app.sh"} {
		must(t, os.WriteFile(filepath.Join(dir, name), []byte("test"), 0600))
	}
	must(t, s.OpenLocalDocument("lib", "note.md"))
	if opened != filepath.Join(dir, "note.md") {
		t.Fatal("wrong document")
	}
	if s.OpenLocalDocument("lib", "app.sh") == nil || s.RevealLocal("lib", "../outside") == nil {
		t.Fatal("accepted unsupported or external target")
	}
	must(t, s.RevealLocal("lib", "app.sh"))
	if opened != filepath.Join(dir, "app.sh") {
		t.Fatal("wrong reveal target")
	}
}

func TestActivityRetention(t *testing.T) {
	s := &Workspace{}
	for i := range 240 {
		s.activity("a", "completed", time.Unix(int64(i), 0))
	}
	if len(s.data.Activity) != 200 || s.data.Activity[0].Time != time.Unix(40, 0).UTC().Format(time.RFC3339Nano) {
		t.Fatal("activity retention is not bounded")
	}
}
