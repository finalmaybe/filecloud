package library

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

const _headChangesTimeout = 20 * time.Second

func (h *handler) getHeadChanges(w http.ResponseWriter, r *http.Request) {
	libraryID := r.PathValue("LibraryId")
	owner, authenticated := auth.UserID(r.Context())
	query, err := url.ParseQuery(r.URL.RawQuery)
	after := query.Get("after")
	if !authenticated || !validUUID(libraryID) || err != nil || (after != "" && !object.ValidID(after)) {
		h.invalid(w)
		return
	}
	for key, values := range query {
		if key != "after" || len(values) != 1 {
			h.invalid(w)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), _headChangesTimeout)
	defer cancel()
	if h.headChangesShutdown != nil {
		stop := context.AfterFunc(h.headChangesShutdown, cancel)
		defer stop()
	}
	commitID, err := h.store.WaitForHeadChange(ctx, owner, libraryID, after)
	switch {
	case r.Context().Err() != nil:
		return
	case err == nil:
		h.writeJSON(w, http.StatusOK, struct {
			CommitID string `json:"CommitId"`
		}{CommitID: commitID})
	case errors.Is(err, storage.ErrLibraryNotFound):
		h.writeError(w, http.StatusNotFound, 2000, "library not found")
	case ctx.Err() == context.DeadlineExceeded:
		w.WriteHeader(http.StatusNoContent)
	case ctx.Err() == context.Canceled:
		h.writeError(w, http.StatusServiceUnavailable, 5001, "head changes unavailable")
	default:
		h.internal(w, "wait for library head change", err)
	}
}
