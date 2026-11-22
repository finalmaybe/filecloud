package library

import (
	"context"
	"errors"
	"time"

	"github.com/mingming-cn/filecloud/internal/storage"
)

func (c *Catalog) PreviewRetention(ctx context.Context, lib storage.Library, next storage.Retention) (storage.RetentionPreview, error) {
	ctx, done, err := c.admit(ctx, lib.OwnerUserID)
	if err != nil {
		return storage.RetentionPreview{}, err
	}
	defer done()
	return c.core.store.PreviewRetention(ctx, lib, next, c.core.now())
}

func (c *Catalog) SetRetention(ctx context.Context, lib storage.Library, next storage.Retention) error {
	return c.core.store.SetRetention(ctx, lib, next, c.core.now())
}

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
