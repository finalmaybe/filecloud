package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	process := exec.Command(server, "serve", "--data-dir", data, "--listen", "127.0.0.1:0")
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
		must(t, s.Bind(id, p, false))
		return p
	}
	pa := bind(a, "work a")
	pb := bind(b, "work b")
	for i := range 10 {
		must(t, os.WriteFile(filepath.Join(pa, fmt.Sprint(i)), []byte("sync data"), 0600))
	}
	a.tick(time.Now())
	must(t, b.RequestSync(id))
	if buf, err := os.ReadFile(filepath.Join(pb, "0")); err != nil || string(buf) != "sync data" {
		t.Fatal("second device did not converge")
	}
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
	h, err := a.History(id, "")
	must(t, err)
	if len(h.Commits) < 2 {
		t.Fatal("missing mainline history")
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
func TestActivityRetention(t *testing.T) {
	s := &Workspace{}
	for i := range 240 {
		s.activity("a", "completed", time.Unix(int64(i), 0))
	}
	if len(s.data.Activity) != 200 || s.data.Activity[0].Time != time.Unix(40, 0).UTC().Format(time.RFC3339Nano) {
		t.Fatal("activity retention is not bounded")
	}
}
