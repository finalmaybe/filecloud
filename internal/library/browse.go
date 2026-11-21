package library

import (
	"context"
	"errors"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

var ErrHistoryBusy = errors.New("history traversal busy")

type HistoryRow struct {
	ID, Message, Device, Time, Role string
	Merge                           bool
}
type BrowserHistory struct {
	Rows                                                []HistoryRow
	Anchor, Next, Selected, Expanded, Cursor, LibraryID string
	Detail                                              historyCommitDetailResponse
}

func (h *handler) historyDetail(ctx context.Context, owner, lib, id string) (historyCommitDetailResponse, error) {
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
func (c *Catalog) admit(ctx context.Context, owner string) (context.Context, func(), error) {
	release, ok := c.core.historyLimiter.tryAcquire(owner)
	if !ok {
		return ctx, nil, ErrHistoryBusy
	}
	ctx, cancel := context.WithTimeout(ctx, c.core.history.RequestTimeout)
	return ctx, func() { cancel(); release() }, nil
}
func (c *Catalog) History(ctx context.Context, owner, lib, cursor, selected, expanded string) (BrowserHistory, error) {
	result := BrowserHistory{Selected: selected, Expanded: expanded, Cursor: cursor, LibraryID: lib}
	ctx, done, err := c.admit(ctx, owner)
	if err != nil {
		return result, err
	}
	defer done()
	var token historyPageToken
	if cursor != "" {
		token, err = c.core.decodeHistoryToken(cursor, owner)
		if err != nil || token.LibraryID != lib {
			return result, ErrInvalidPage
		}
	}
	p, _, err := c.core.historyPage(ctx, owner, lib, token, 50)
	if err != nil {
		return result, err
	}
	if p.AnchorCommitID != nil {
		result.Anchor = *p.AnchorCommitID
	}
	result.Next = p.NextPageToken
	for _, commit := range p.Commits {
		merge := len(commit.Parents) == 2
		result.Rows = append(result.Rows, HistoryRow{commit.CommitID, commit.Message, commit.DeviceID, commit.CreatedAt, "主线", merge})
		if !merge || commit.CommitID != expanded {
			continue
		}
		seen := map[string]bool{}
		id := commit.Parents[1]
		for {
			if seen[id] || len(seen) >= 1024 {
				return result, errHistoryCorrupt
			}
			seen[id] = true
			source, err := c.core.historyDetail(ctx, owner, lib, id)
			if err != nil {
				return result, err
			}
			if source.Role != _historyRoleMergeSource || source.MainlineCommitID != expanded || len(source.Parents) == 0 {
				return result, errHistoryCorrupt
			}
			result.Rows = append(result.Rows, HistoryRow{id, source.Message, source.DeviceID, source.CreatedAt, "合并来源", false})
			if len(source.Parents) == 1 {
				break
			}
			id = source.Parents[1]
		}
	}
	if result.Selected == "" && len(result.Rows) > 0 {
		result.Selected = result.Rows[0].ID
	}
	if result.Selected != "" {
		result.Detail, err = c.core.historyDetail(ctx, owner, lib, result.Selected)
	}
	return result, err
}
