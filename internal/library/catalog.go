package library

import (
	"context"
	"errors"
	"time"

	"github.com/mingming-cn/filecloud/internal/storage"
)

var ErrInvalidPage = errors.New("invalid or expired page token")

func (h *handler) listPage(ctx context.Context, owner, token string, size int) ([]storage.Library, string, error) {
	var start time.Time
	var cursor string
	var err error
	if token != "" {
		start, cursor, err = h.decodePageToken(token, owner)
		if err != nil {
			return nil, "", ErrInvalidPage
		}
	}
	items, err := h.store.ListLibraries(ctx, owner, start, cursor, size+1)
	if err != nil || len(items) <= size {
		return items, "", err
	}
	next, err := h.encodePageToken(owner, items[size-1])
	return items[:size], next, err
}
