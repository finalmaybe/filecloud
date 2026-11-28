package library

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
	"golang.org/x/text/cases"
)

var ErrRestoreOccupied = errors.New("restore destination is occupied")

// Compare only changed directory pairs. A removed subtree is one entry, regardless of its size.
func (c *Catalog) removedPaths(ctx context.Context, owner, lib string, old *string, next string) ([]storage.TrashEntry, error) {
	if old == nil {
		return nil, nil
	}
	before, err := c.core.readHistoryCommitObject(ctx, owner, lib, *old)
	if err != nil {
		return nil, err
	}
	after, err := c.core.readHistoryCommitObject(ctx, owner, lib, next)
	if err != nil {
		return nil, err
	}
	var removed []storage.TrashEntry
	var diff func(string, string, string) error
	diff = func(prefix, a, b string) error {
		if a == b {
			return nil
		}
		prev, err := c.directory(ctx, owner, lib, a)
		if err != nil {
			return err
		}
		cur, err := c.directory(ctx, owner, lib, b)
		if err != nil {
			return err
		}
		j := 0
		for _, entry := range prev.Entries {
			for j < len(cur.Entries) && cur.Entries[j].Name < entry.Name {
				j++
			}
			name := path.Join(prefix, entry.Name)
			if j == len(cur.Entries) || entry.Name != cur.Entries[j].Name || entry.Type != cur.Entries[j].Type {
				removed = append(removed, storage.TrashEntry{Kind: entry.Type, Source: *old, Path: name, Entry: entry})
				continue
			}
			if entry.Type == "Directory" {
				if err := diff(name, entry.ID, cur.Entries[j].ID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := diff("", before.Root, after.Root); err != nil {
		return nil, err
	}
	return removed, c.archiveRemovedHistory(ctx, owner, lib, *old, removed)
}

func (c *Catalog) MoveToTrash(ctx context.Context, owner string, v VersionTarget) error {
	done := c.core.store.UseLibrary(owner, v.LibraryID)
	defer done()
	if !object.ValidID(v.CommitID) || v.Path == "." || !object.ValidPath(v.Path) {
		return ErrInvalidPath
	}
	h := c.core
	release, ok := h.headLimiter.tryAcquire(owner, v.LibraryID)
	if !ok {
		return ErrHistoryBusy
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, h.headValidation.RequestTimeout)
	defer cancel()
	l, err := h.store.GetLibrary(ctx, owner, v.LibraryID)
	if err != nil {
		return err
	}
	if l.HeadCommitID == nil || *l.HeadCommitID != v.CommitID {
		return storage.ErrHeadConflict
	}
	commit, err := h.readHistoryCommitObject(ctx, owner, v.LibraryID, v.CommitID)
	if err != nil {
		return err
	}
	entry, err := c.pathEntry(ctx, owner, v.LibraryID, commit.Root, v.Path)
	if err != nil {
		return err
	}
	root, err := c.replacePathEntry(ctx, owner, v, commit.Root, object.DirectoryEntry{})
	if err != nil {
		return err
	}
	removed := []storage.TrashEntry{{Path: v.Path, Source: v.CommitID, Kind: entry.Type, Entry: entry}}
	if err := c.archiveRemovedHistory(ctx, owner, v.LibraryID, v.CommitID, removed); err != nil {
		return err
	}
	_, err = c.publishWebRoot(ctx, owner, l, root, "移入回收站", storage.HeadChange{Removed: removed})
	return err
}

func (c *Catalog) ListTrash(ctx context.Context, owner, lib string, before int64) ([]storage.TrashEntry, error) {
	return c.core.store.Trash(ctx, owner, lib, c.core.now(), before)
}

func (c *Catalog) DeleteTrash(ctx context.Context, owner, lib string, ids []int64) error {
	return c.core.store.DeleteTrash(ctx, owner, lib, ids, c.core.now())
}

func (c *Catalog) RestoreTrash(ctx context.Context, owner, lib string, id int64, destination string) error {
	done := c.core.store.UseLibrary(owner, lib)
	defer done()
	h := c.core
	release, ok := h.headLimiter.tryAcquire(owner, lib)
	if !ok {
		return ErrHistoryBusy
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, h.headValidation.RequestTimeout)
	defer cancel()
	now := h.now()
	t, err := h.store.TrashItem(ctx, owner, lib, id, now)
	if err != nil {
		return err
	}
	if t.Kind == "Version" {
		return h.store.RestoreVersionRecord(ctx, owner, lib, t, now)
	}
	if destination == "" {
		destination = t.Path
	}
	if !object.ValidPath(destination) || destination == "." {
		return ErrInvalidPath
	}
	current, err := h.store.GetLibrary(ctx, owner, lib)
	if err != nil {
		return err
	}
	if current.HeadCommitID == nil {
		return storage.ErrLibraryNotFound
	}
	commit, err := h.readHistoryCommitObject(ctx, owner, lib, *current.HeadCommitID)
	if err != nil {
		return err
	}
	root, err := c.placeTrashPath(ctx, owner, lib, commit.Root, strings.Split(destination, "/"), t.Entry, now, false)
	if err != nil {
		return err
	}
	_, err = c.publishWebRoot(ctx, owner, current, root, "从回收站恢复", storage.HeadChange{RestoreID: id, Destination: destination})
	return err
}

// A preflight walks the same case-folded path as restore, without writing directory objects.
func (c *Catalog) placeTrashPath(ctx context.Context, owner, lib, root string, parts []string, entry object.DirectoryEntry, now time.Time, checkOnly bool) (string, error) {
	var dir object.Directory
	var err error
	if root != "" {
		dir, err = c.directory(ctx, owner, lib, root)
		if err != nil {
			return "", err
		}
	}
	name := parts[0]
	index := sort.Search(len(dir.Entries), func(i int) bool { return dir.Entries[i].Name >= name })
	exists := index < len(dir.Entries) && dir.Entries[index].Name == name
	if !exists {
		fold := cases.Fold()
		key := fold.String(name)
		for _, e := range dir.Entries {
			if fold.String(e.Name) == key {
				return "", ErrRestoreOccupied
			}
		}
	}
	child := entry
	child.Name = name
	if len(parts) > 1 {
		child = object.DirectoryEntry{Name: name, Type: "Directory", ModifiedAt: now.UTC().Truncate(time.Second).Format(time.RFC3339)}
		if exists {
			child = dir.Entries[index]
			if child.Type != "Directory" {
				return "", ErrRestoreOccupied
			}
		}
		child.ID, err = c.placeTrashPath(ctx, owner, lib, child.ID, parts[1:], entry, now, checkOnly)
		if err != nil {
			return "", err
		}
	} else if exists {
		return "", ErrRestoreOccupied
	}
	if checkOnly {
		return "", nil
	}
	if !exists {
		dir.Entries = append(dir.Entries, object.DirectoryEntry{})
		copy(dir.Entries[index+1:], dir.Entries[index:])
	}
	dir.Entries[index] = child
	return c.putVersionObject(ctx, owner, lib, "directories", dir.Bytes())
}
