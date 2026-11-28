package library

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

const headChangesPath = "/v1/libraries/" + _headLibraryID + "/head/changes?after="

func TestHeadChangesImmediateResponseIsOwnerScoped(t *testing.T) {
	handler, store, _ := newTestHandler(t)
	defer closeStore(t, store)
	createHeadLibrary(t, handler)

	assertStatusCode(t, serve(handler, http.MethodGet, headChangesPath, "", ""), 401, 1001)
	assertStatusCode(t, serve(handler, http.MethodGet, headChangesPath, "", _otherToken), 404, 2000)
	for _, query := range []string{"invalid", "&after=", "&extra=", "%ZZ"} {
		assertStatusCode(t, serve(handler, http.MethodGet, headChangesPath+query, "", _ownerToken), 400, 1000)
	}

	root := putMetadata(t, store, "directories", string(object.Directory{}.Bytes()))
	commit := putCommit(t, store, _ownerID, nil, root)
	assertHeadChanged(t, serve(handler, http.MethodGet, headChangesPath+commit, "", _ownerToken), "")
	publishHead(t, handler, commit, headETag(0), 200, 0)
	assertHeadChanged(t, serve(handler, http.MethodGet, headChangesPath, "", _ownerToken), commit)
	assertHeadChanged(t, serve(handler, http.MethodGet, headChangesPath+object.ID([]byte("stale")), "", _ownerToken), commit)
}

func TestHeadChangesWakesOnNativeWebAndDAVPublications(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, store, now := newTestHandlerWithConfig(t, Config{
			AfterHeadUpdate: func() error { return errors.New("response failed after publication") },
		})
		defer closeStore(t, store)
		createHeadLibrary(t, handler)
		catalog := handler.(*Catalog)
		root := putMetadata(t, store, "directories", string(object.Directory{}.Bytes()))
		commit := putCommit(t, store, _ownerID, nil, root)

		first := startHeadChangesPoll(t, t.Context(), handler, "")
		second := startHeadChangesPoll(t, t.Context(), handler, "")
		publishHead(t, handler, commit, headETag(0), 500, 5000)
		assertHeadChanged(t, receiveHeadChangesPoll(t, first, time.Second), commit)
		assertHeadChanged(t, receiveHeadChangesPoll(t, second, time.Second), commit)

		const credential = "01234567-89ab-4def-8123-456789abcdee"
		if _, err := store.CreateDAVCredential(t.Context(), _ownerID, _headLibraryID, credential, "test", now); err != nil {
			t.Fatal(err)
		}
		tree, err := catalog.DAVTree(t.Context(), _ownerID, _headLibraryID)
		if err != nil {
			t.Fatal(err)
		}
		davPoll := startHeadChangesPoll(t, t.Context(), handler, commit)
		if err := tree.Mutate(t.Context(), "MKCOL", "folder", "", "", false, storage.DAVChange{Credential: credential}); err != nil {
			t.Fatal(err)
		}
		current, err := store.GetLibrary(t.Context(), _ownerID, _headLibraryID)
		if err != nil || current.HeadCommitID == nil || *current.HeadCommitID == commit {
			t.Fatalf("DAV publication: %+v, %v", current, err)
		}
		assertHeadChanged(t, receiveHeadChangesPoll(t, davPoll, time.Second), *current.HeadCommitID)

		webPoll := startHeadChangesPoll(t, t.Context(), handler, *current.HeadCommitID)
		if err := catalog.MoveToTrash(t.Context(), _ownerID, VersionTarget{LibraryID: _headLibraryID, Path: "folder", CommitID: *current.HeadCommitID}); err != nil {
			t.Fatal(err)
		}
		updated, err := store.GetLibrary(t.Context(), _ownerID, _headLibraryID)
		if err != nil || updated.HeadCommitID == nil || *updated.HeadCommitID == *current.HeadCommitID {
			t.Fatalf("web publication: %+v, %v", updated, err)
		}
		assertHeadChanged(t, receiveHeadChangesPoll(t, webPoll, time.Second), *updated.HeadCommitID)
	})
}

func TestHeadChangesTimeoutAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shutdown, stop := context.WithCancel(t.Context())
		defer stop()
		handler, store, _ := newTestHandlerWithConfig(t, Config{HeadChangesShutdown: shutdown})
		defer closeStore(t, store)
		createHeadLibrary(t, handler)

		start := time.Now()
		poll := startHeadChangesPoll(t, t.Context(), handler, "")
		time.Sleep(19 * time.Second)
		synctest.Wait()
		select {
		case <-poll:
			t.Fatal("poll returned early with unchanged Head")
		default:
		}
		response := receiveHeadChangesPoll(t, poll, 2*time.Second)
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 || time.Since(start) != 20*time.Second {
			t.Fatalf("after %s, status = %d, body = %q", time.Since(start), response.Code, response.Body.String())
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		canceled := startHeadChangesPoll(t, ctx, handler, "")
		cancel()
		if response := receiveHeadChangesPoll(t, canceled, time.Second); response.Body.Len() != 0 {
			t.Fatalf("canceled request wrote %q", response.Body.String())
		}

		draining := startHeadChangesPoll(t, t.Context(), handler, "")
		stop()
		assertStatusCode(t, receiveHeadChangesPoll(t, draining, time.Second), 503, 5001)
	})
}

func assertHeadChanged(t *testing.T, response *httptest.ResponseRecorder, commit string) {
	t.Helper()
	if response.Code != http.StatusOK || response.Body.String() != `{"CommitId":"`+commit+"\"}\n" ||
		response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Head change = %d %q %v, want 200 and CommitId %q", response.Code, response.Body.String(), response.Header(), commit)
	}
}

func startHeadChangesPoll(t *testing.T, ctx context.Context, handler http.Handler, after string) <-chan *httptest.ResponseRecorder {
	t.Helper()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, headChangesPath+after, nil)
		request.Header.Set("Authorization", "Bearer "+_ownerToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- response
	}()
	synctest.Wait()
	select {
	case response := <-result:
		t.Fatalf("unchanged Head poll returned without waiting: %d %q", response.Code, response.Body.String())
	default:
	}
	return result
}

func receiveHeadChangesPoll(t *testing.T, result <-chan *httptest.ResponseRecorder, maxWait time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-result:
		return response
	case <-time.After(maxWait):
		t.Fatal("Head poll did not complete in time")
		return nil
	}
}
