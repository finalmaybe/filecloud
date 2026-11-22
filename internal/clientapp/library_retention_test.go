package clientapp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	libraryapi "github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestOfflineDeviceMergesAfterItsOldContentWasCollected(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	if err := storage.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	open := func() *storage.Store {
		s, err := storage.OpenForServe(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	if err := s.CreateUser(ctx, storage.User{ID: testClientUserID, Username: "offline", PasswordHash: "unused"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, testClientUserID, sha256.Sum256([]byte(token)), "test", now, now.AddDate(0, 0, 120)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateLibrary(ctx, storage.Library{ID: testClientLibraryID, OwnerUserID: testClientUserID, Name: "Offline"}, now); err != nil {
		t.Fatal(err)
	}
	var handler atomic.Pointer[libraryapi.Catalog]
	start := func(clock time.Time) {
		h, err := libraryapi.NewHandler(s, nil, libraryapi.Config{Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatal(err)
		}
		handler.Store(h)
	}
	start(now)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.Load().ServeHTTP(w, r) }))
	defer server.Close()
	a, at := newClientPaths(t)
	b, bt := newClientPaths(t)
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(at, "base"), "old bytes")
	bind := func(args []string) {
		if err := runTest(ctx, args, strings.NewReader(token+"\n"), io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	bind(append(bindArgs(a, server.URL, testClientLibraryID, at, testClientDeviceID), "--import-local"))
	bind(bindArgs(b, server.URL, testClientLibraryID, bt, testOtherDeviceID))
	write(filepath.Join(at, "base"), "remote edit")
	if err := syncTestWorktree(t, a, at); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(bt, "base"), "unpublished local edit")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	later := now.AddDate(0, 0, 65)
	g, err := storage.OpenGarbageCollector(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Collect(ctx, storage.GarbageCollectionOptions{Now: later, GracePeriod: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer s.Close()
	start(later)
	if exists, err := s.HasObject(ctx, testClientUserID, testClientLibraryID, "blocks", object.ID([]byte("old bytes"))); err != nil || exists {
		t.Fatal("original baseline content survived collection", err)
	}
	if err := syncTestWorktree(t, b, bt); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(bt, "base (Filecloud conflict *)"))
	if err != nil || len(matches) != 1 {
		t.Fatal(matches, err)
	}
	for path, want := range map[string]string{filepath.Join(bt, "base"): "remote edit", matches[0]: "unpublished local edit"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatal("offline changes not preserved", path, err)
		}
	}
}
