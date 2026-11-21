package library

import (
	"context"
	"errors"
	"github.com/mingming-cn/filecloud/internal/storage"
	"time"
)

var ErrInvalidPage = errors.New("invalid or expired page token")
var ErrInvalidLibrary = errors.New("invalid library ID or name")

func (c *Catalog) Create(ctx context.Context, owner, id, name string) (storage.Library, error) {
	display, err := validateName(name)
	if !validUUID(id) || err != nil {
		return storage.Library{}, ErrInvalidLibrary
	}
	item, _, err := c.core.store.CreateLibrary(ctx, storage.Library{ID: id, OwnerUserID: owner, Name: display}, c.core.now().UTC().Truncate(time.Second))
	return item, err
}
func (c *Catalog) List(ctx context.Context, owner, token string) ([]storage.Library, string, error) {
	return c.core.listPage(ctx, owner, token, 50)
}
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
