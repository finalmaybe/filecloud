package library

import (
	"context"
	"errors"
	"net/http"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

var ErrSnapshotExpired = errors.New("snapshot retention expired")
var ErrHistoryBusy = errors.New("history traversal busy")

func (h *handler) historyDetail(ctx context.Context, owner, lib, id string) (historyCommitDetailResponse, error) {
	commit, err := h.commitDetail(ctx, owner, lib, id)
	if err != nil {
		return historyCommitDetailResponse{}, err
	}
	expired, err := h.store.SnapshotExpired(ctx, owner, lib, id, h.now())
	if err != nil {
		return historyCommitDetailResponse{}, err
	}
	if expired {
		return historyCommitDetailResponse{}, ErrSnapshotExpired
	}
	return commit, nil
}

func (h *handler) commitDetail(ctx context.Context, owner, lib, id string) (historyCommitDetailResponse, error) {
	var result historyCommitDetailResponse
	role, found, err := h.store.GetPublishedCommitRole(ctx, owner, lib, id)
	if err != nil {
		return result, errHistoryUnavailable
	}
	if !found {
		return result, storage.ErrLibraryNotFound
	}
	if role.CommitID != id || !object.ValidID(role.MainlineCommitID) {
		return result, errHistoryCorrupt
	}
	switch role.Role {
	case _historyRoleMainline:
		if role.MainlineCommitID != id {
			return result, errHistoryCorrupt
		}
	case _historyRoleMergeSource:
		parent, ok, err := h.store.GetPublishedCommitRole(ctx, owner, lib, role.MainlineCommitID)
		if err != nil {
			return result, errHistoryUnavailable
		}
		if !ok || parent.Role != _historyRoleMainline || parent.CommitID != role.MainlineCommitID || parent.MainlineCommitID != parent.CommitID || id == parent.CommitID {
			return result, errHistoryCorrupt
		}
	default:
		return result, errHistoryCorrupt
	}
	commit, err := h.readHistoryCommitObject(ctx, owner, lib, id)
	if err != nil {
		return result, err
	}
	return historyCommitDetailResponse{CommitID: id, MainlineCommitID: role.MainlineCommitID, Role: role.Role, Root: commit.Root, DeviceID: commit.DeviceID, Message: commit.Message, AuthorUserID: commit.AuthorUserID, CreatedAt: commit.CreatedAt, Parents: append([]string{}, commit.Parents...)}, nil
}
func (h *handler) writeBrowseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSnapshotExpired):
		h.writeError(w, http.StatusGone, 2001, "snapshot expired")
	case errors.Is(err, ErrInvalidPath), errors.Is(err, ErrInvalidPage):
		h.invalid(w)
	case errors.Is(err, storage.ErrLibraryNotFound):
		h.writeError(w, http.StatusNotFound, 2000, "library or published path not found")
	case errors.Is(err, ErrHistoryBusy):
		w.Header().Set("Retry-After", "1")
		h.writeError(w, http.StatusTooManyRequests, 4000, "history busy")
	default:
		h.historyFailure(w, err, "")
	}
}

func (c *Catalog) admit(ctx context.Context, owner string) (context.Context, func(), error) {
	release, ok := c.core.historyLimiter.tryAcquire(owner)
	if !ok {
		return ctx, nil, ErrHistoryBusy
	}
	ctx, cancel := context.WithTimeout(ctx, c.core.history.RequestTimeout)
	return ctx, func() { cancel(); release() }, nil
}
