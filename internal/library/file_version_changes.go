package library

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

var ErrCurrentVersion = errors.New("selected version is current")

func (c *Catalog) DeleteFileVersion(ctx context.Context, owner string, v VersionTarget, anchor string) error {
	done := c.core.store.UseLibrary(owner, v.LibraryID)
	defer done()
	ctx, done, err := c.admit(ctx, owner)
	if err != nil {
		return err
	}
	defer done()
	entry, _, err := c.versionChange(ctx, owner, v, anchor)
	if err != nil {
		return err
	}
	return c.core.store.DeleteFileVersion(ctx, owner, v.LibraryID, storage.TrashEntry{Kind: "Version", Path: v.Path, Source: v.CommitID, Entry: entry, Versions: []storage.ArchivedVersion{{Path: v.Path, Source: v.CommitID, FileID: entry.ID, Modified: entry.ModifiedAt, Change: "content"}}}, anchor, c.core.now())
}

// RestoreFileVersion replaces a single existing file in the displayed Head and publishes forward.
func (c *Catalog) RestoreFileVersion(ctx context.Context, owner string, v VersionTarget, anchor string) (string, error) {
	done := c.core.store.UseLibrary(owner, v.LibraryID)
	defer done()
	h := c.core
	release, ok := h.headLimiter.tryAcquire(owner, v.LibraryID)
	if !ok {
		return "", ErrHistoryBusy
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, h.headValidation.RequestTimeout)
	defer cancel()
	source, current, err := c.versionChange(ctx, owner, v, anchor)
	if err != nil {
		return "", err
	}
	head, err := h.readHistoryCommitObject(ctx, owner, v.LibraryID, anchor)
	if err != nil {
		return "", err
	}
	root, err := c.replacePathEntry(ctx, owner, v, head.Root, source)
	if err != nil {
		return "", err
	}
	if root == head.Root {
		return anchor, nil
	}

	return c.publishWebRoot(ctx, owner, current, root, "恢复文件历史版本", storage.HeadChange{})
}

func (c *Catalog) publishWebRoot(ctx context.Context, owner string, current storage.Library, root, message string, change storage.HeadChange) (string, error) {
	h := c.core
	anchor := *current.HeadCommitID
	lib := current.ID
	now := h.now().UTC()
	// One stable logical device identifies publications made by the Web console.
	const webDevice = "0db7e081-c11d-4afa-86ed-93cdba397d53"
	commit := object.Commit{Root: root, Parents: []string{anchor}, AuthorUserID: owner, DeviceID: webDevice,
		CreatedAt: now.Format(time.RFC3339), Message: message}
	id, err := c.putVersionObject(ctx, owner, lib, "commits", commit.Bytes())
	if err != nil {
		return "", err
	}
	missing, roles, err := h.validateCandidate(ctx, owner, lib, &anchor, id)
	if errors.Is(err, errCorruptObject) || len(missing.ids) != 0 {
		return "", errHistoryCorrupt
	}
	if err != nil {
		return "", err
	}
	_, err = h.store.PublishHead(ctx, owner, lib, &anchor, current.HeadVersion, id, roles, now, change)
	return id, err
}

func (c *Catalog) versionChange(ctx context.Context, owner string, v VersionTarget, anchor string) (object.DirectoryEntry, storage.Library, error) {
	entry, err := c.versionEntry(ctx, owner, v)
	if err != nil {
		return entry, storage.Library{}, err
	}
	current, err := c.core.store.GetLibrary(ctx, owner, v.LibraryID)
	if err != nil {
		return entry, current, err
	}
	if current.HeadCommitID == nil || *current.HeadCommitID != anchor {
		return object.DirectoryEntry{}, current, storage.ErrHeadConflict
	}
	return entry, current, c.requireHistoricalVersion(ctx, owner, v, anchor, entry)
}

// A repeated file state (e.g. after a restore) can also belong to an older record.
// Follow the first parent only while the file still matches the selected entry.
func (c *Catalog) requireHistoricalVersion(ctx context.Context, owner string, v VersionTarget, head string, selected object.DirectoryEntry) error {
	var lastRoot string
	for id := head; id != v.CommitID; {
		commit, err := c.core.readHistoryCommit(ctx, owner, v.LibraryID, id)
		if err != nil {
			return err
		}
		if commit.Root != lastRoot {
			entry, err := c.pathEntry(ctx, owner, v.LibraryID, commit.Root, v.Path)
			if err != nil {
				return err
			}
			if entry != selected {
				return nil
			}
			lastRoot = commit.Root
		}
		if len(commit.Parents) == 0 {
			return storage.ErrLibraryNotFound
		}
		id = commit.Parents[0]
	}
	return ErrCurrentVersion
}

// An empty source removes the leaf; neither operation changes sibling entries.
func (c *Catalog) replacePathEntry(ctx context.Context, owner string, v VersionTarget, root string, source object.DirectoryEntry) (string, error) {
	// Keep only ancestor IDs and entry positions; a deep path may contain large directories.
	type ancestor struct {
		id    string
		index int
	}
	var ancestors []ancestor
	entry, err := c.walkPath(ctx, owner, v.LibraryID, root, v.Path, func(id string, index int) {
		ancestors = append(ancestors, ancestor{id, index})
	})
	if err != nil {
		return "", err
	}
	if entry.Type == "" || (source.Type != "" && entry.Type != "File") {
		return "", storage.ErrLibraryNotFound
	}
	if entry == source {
		return root, nil
	}
	id := source.ID
	for i := len(ancestors) - 1; i >= 0; i-- {
		parent := ancestors[i]
		dir, err := c.directory(ctx, owner, v.LibraryID, parent.id)
		if err != nil {
			return "", err
		}
		if i == len(ancestors)-1 && source.Type == "" {
			dir.Entries = slices.Delete(dir.Entries, parent.index, parent.index+1)
		} else {
			dir.Entries[parent.index].ID = id
			if i == len(ancestors)-1 {
				dir.Entries[parent.index].ModifiedAt = source.ModifiedAt
			}
		}
		id, err = c.putVersionObject(ctx, owner, v.LibraryID, "directories", dir.Bytes())
		if err != nil {
			return "", err
		}
	}
	return id, nil
}

func (c *Catalog) putVersionObject(ctx context.Context, owner, lib, kind string, data []byte) (string, error) {
	id := object.ID(data)
	_, err := c.core.store.PutObject(ctx, owner, lib, kind, id, bytes.NewReader(data))
	return id, err
}
