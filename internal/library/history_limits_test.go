package library

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestSetHistoryConcurrencyUpdatesSharedLimiterWithoutInterruptingActiveHistory(t *testing.T) {
	resumeCtx, resume := context.WithCancel(t.Context())
	defer resume()
	entered := make(chan struct{})
	h, store, _ := newTestHandlerWithConfig(t, Config{
		History: HistoryConfig{GlobalConcurrency: 2, UserConcurrency: 1},
		afterHistoryAdmit: func(ctx context.Context) {
			close(entered)
			select {
			case <-resumeCtx.Done():
			case <-ctx.Done():
			}
		},
	})
	defer closeStore(t, store)
	catalog := h.(*Catalog)
	createHeadLibrary(t, h)
	root := putMetadata(t, store, "directories", `{"Entries":[],"Type":"Directory","Version":1}`)
	commit := putCommit(t, store, _ownerID, nil, root)
	publishHead(t, h, commit, `"head-version-0"`, http.StatusOK, 0)

	var response *httptest.ResponseRecorder
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response = serve(catalog, http.MethodGet, "/v1/libraries/"+_headLibraryID+"/history", "", _ownerToken)
	}()
	defer func() { resume(); <-finished }()
	select {
	case <-entered:
	case <-finished:
		t.Fatalf("history did not enter admission gate: status %d", response.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for history to enter admission gate")
	}

	acquire := func(owner string) func() {
		t.Helper()
		_, release, err := catalog.admit(t.Context(), owner)
		if err != nil {
			t.Fatalf("shared read for %s was refused: %v", owner, err)
		}
		return release
	}
	busy := func(owner string) {
		t.Helper()
		_, release, err := catalog.admit(t.Context(), owner)
		if release != nil {
			release()
		}
		if !errors.Is(err, ErrHistoryBusy) {
			t.Fatalf("shared read for %s should be limited, got %v", owner, err)
		}
	}
	set := func(limits storage.HistoryConcurrency) {
		t.Helper()
		if err := catalog.SetHistoryConcurrency(t.Context(), limits); err != nil {
			t.Fatalf("save limits %+v: %v", limits, err)
		}
	}

	busy(_ownerID) // The machine history request occupies the same account slot as Catalog reads.
	releaseOther := acquire(_otherID)
	defer releaseOther()
	busy("third-account")

	set(storage.HistoryConcurrency{Global: 3, User: 2})
	releaseOwner := acquire(_ownerID)
	defer releaseOwner()
	busy("third-account")

	lowered := storage.HistoryConcurrency{Global: 1, User: 1}
	set(lowered)
	releaseOther()
	releaseOwner()
	busy(_otherID) // The lowered limit still counts the history request admitted before saving.
	resume()
	<-finished
	assertStatusCode(t, response, http.StatusOK, 0)
	acquire(_otherID)()

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := catalog.SetHistoryConcurrency(canceled, storage.HistoryConcurrency{Global: 2, User: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("save using a canceled context = %v, want cancellation", err)
	}
	if got := catalog.HistoryConcurrency(); got != lowered {
		t.Fatalf("failed save changed live limits to %+v, want %+v", got, lowered)
	}
}
