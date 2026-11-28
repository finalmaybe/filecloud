package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCatalogReadsDuringSync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/libraries/lib/files":
			fmt.Fprint(w, `{"Directory":{"CommitId":"head"}}`)
		case "/v1/libraries/lib/content":
			fmt.Fprint(w, "preview")
		case "/v1/libraries/lib/file-history":
			if r.URL.Query().Get("Path") == "expired" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"FileHistory":{"Versions":[{"CommitId":"prior"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := &Workspace{active: account{Server: server.URL, UserID: "owner"}, token: newUUID(), data: profile{Names: map[string]string{"lib": "Library"}}}
	s.publish()
	before := s.Snapshot()
	s.mu.Lock() // Held for the duration of a real sync subprocess.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if d, err := s.Files("lib", "", ".", 0); err != nil || d.CommitId != "head" {
			t.Errorf("directory = %+v, err = %v", d, err)
		}
		if h, err := s.FileVersions("lib", "head", "a.txt", ""); err != nil || len(h.Versions) != 1 || h.Versions[0].CommitId != "prior" {
			t.Errorf("file history = %+v, err = %v", h, err)
		}
		if v, err := s.PreviewFile("lib", "head", "a.txt"); err != nil || v.Content != "preview" {
			t.Errorf("preview = %+v, err = %v", v, err)
		}
		if _, err := s.FileVersions("lib", "head", "expired", ""); !errors.Is(err, commandError("auth_required")) {
			t.Errorf("expired session: %v", err)
		}
	}()
	select {
	case <-done:
		s.mu.Unlock()
	case <-time.After(2 * time.Second):
		s.mu.Unlock()
		<-done
		t.Fatal("cloud reads waited for the sync lock")
	}
	if !before.CatalogSession || s.Snapshot().CatalogSession || before.Revision >= s.Snapshot().Revision {
		t.Fatal("session expiration did not publish a new immutable view")
	}
	s.publish()
	if s.Snapshot().CatalogSession {
		t.Fatal("sync progress revived an expired session")
	}
	oldToken := s.token
	s.token = newUUID()
	s.publish()
	s.setCatalogAvailable(oldToken, false) // A late 401 from the earlier login.
	if !s.Snapshot().CatalogSession {
		t.Fatal("new session inherited an earlier expiration")
	}
	if _, err := s.Files("unlisted", "", ".", 0); !errors.Is(err, errSession) {
		t.Fatal("unlisted library admitted", err)
	}
	s.active.LogoutPending = true
	s.publish()
	if _, err := s.Files("lib", "", ".", 0); !errors.Is(err, errSession) {
		t.Fatal("pending logout admitted a catalog read", err)
	}
	if _, err := s.RestoreVersion("lib", strings.Repeat("a", 64), "a.txt", "head"); !errors.Is(err, errSession) {
		t.Fatal("pending logout admitted a catalog mutation", err)
	}
}
