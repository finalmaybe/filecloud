package library

import (
	"context"
	"io"
	"path"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

// VersionTarget identifies a file by its library, published commit and original path.
type VersionTarget struct{ LibraryID, Path, CommitID string }

type VersionFile struct {
	object.File
	ModifiedAt string
	catalog    *Catalog
	owner, lib string
}

func (c *Catalog) OpenFileVersion(ctx context.Context, owner string, target VersionTarget) (VersionFile, error) {
	ctx, done, err := c.admit(ctx, owner)
	if err != nil {
		return VersionFile{}, err
	}
	defer done()
	entry, err := c.versionEntry(ctx, owner, target)
	if err != nil {
		return VersionFile{}, err
	}
	return c.openEntryFile(ctx, owner, target.LibraryID, entry)
}

// OpenSnapshotFile reads the listed snapshot, including files unchanged in that commit.
func (c *Catalog) OpenSnapshotFile(ctx context.Context, owner string, target VersionTarget) (VersionFile, error) {
	if !object.ValidID(target.CommitID) || !object.ValidPath(target.Path) || target.Path == "." {
		return VersionFile{}, ErrInvalidPath
	}
	ctx, done, err := c.admit(ctx, owner)
	if err != nil {
		return VersionFile{}, err
	}
	defer done()
	entry, err := c.publishedEntry(ctx, owner, target.LibraryID, target.CommitID, target.Path)
	if err != nil {
		return VersionFile{}, err
	}
	if entry.Type != "File" {
		return VersionFile{}, storage.ErrLibraryNotFound
	}
	return c.openEntryFile(ctx, owner, target.LibraryID, entry)
}

func (c *Catalog) openEntryFile(ctx context.Context, owner, lib string, entry object.DirectoryEntry) (VersionFile, error) {
	data, err := c.metadata(ctx, owner, lib, "files", entry.ID, object.MaxFileObjectSize)
	if err != nil {
		return VersionFile{}, err
	}
	file, err := object.VerifyFile(data, entry.ID)
	return VersionFile{File: file, ModifiedAt: entry.ModifiedAt, catalog: c, owner: owner, lib: lib}, err
}

// Content reads only the requested blocks and verifies each before exposing its bytes.
func (f VersionFile) Content(ctx context.Context) *io.SectionReader {
	return io.NewSectionReader(&fileBlocks{file: f, ctx: ctx, index: -1}, 0, f.Size)
}

type fileBlocks struct {
	file  VersionFile
	ctx   context.Context
	index int64
	data  []byte
}

func (b *fileBlocks) ReadAt(p []byte, off int64) (int, error) {
	f := b.file
	n := 0
	for n < len(p) && off < f.Size {
		if err := b.ctx.Err(); err != nil {
			return n, err
		}
		index := off / object.MaxBlockSize
		if b.index != index {
			id := f.Blocks[index]
			size := min(f.Size-index*object.MaxBlockSize, object.MaxBlockSize)
			data, err := f.catalog.metadata(b.ctx, f.owner, f.lib, "blocks", id, size)
			if err != nil {
				return n, err
			}
			if int64(len(data)) != size || object.ID(data) != id {
				return n, errHistoryCorrupt
			}
			b.index, b.data = index, data
		}
		count := copy(p[n:], b.data[off%object.MaxBlockSize:])
		n += count
		off += int64(count)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (c *Catalog) versionEntry(ctx context.Context, owner string, v VersionTarget) (object.DirectoryEntry, error) {
	if !object.ValidPath(v.Path) || v.Path == "." || !object.ValidID(v.CommitID) {
		return object.DirectoryEntry{}, ErrInvalidPath
	}
	commit, err := c.core.commitDetail(ctx, owner, v.LibraryID, v.CommitID)
	if err != nil {
		return object.DirectoryEntry{}, err
	}
	if commit.Role != _historyRoleMainline {
		return object.DirectoryEntry{}, storage.ErrLibraryNotFound
	}
	deleted, err := c.core.store.DeletedFileVersions(ctx, owner, v.LibraryID, v.Path, []string{v.CommitID})
	if err != nil {
		return object.DirectoryEntry{}, err
	}
	if deleted[v.CommitID] {
		return object.DirectoryEntry{}, storage.ErrLibraryNotFound
	}
	archived, err := c.core.store.ArchivedAtPath(ctx, owner, v.LibraryID, v.Path, []string{v.CommitID}, c.core.now())
	if err != nil {
		return object.DirectoryEntry{}, err
	}
	if row, ok := archived[v.CommitID]; ok {
		return object.DirectoryEntry{Name: path.Base(v.Path), Type: "File", ID: row.FileID, ModifiedAt: row.Modified}, nil
	}
	entry, err := c.pathEntry(ctx, owner, v.LibraryID, commit.Root, v.Path)
	if err != nil {
		return entry, err
	}
	if entry.Type != "File" {
		return entry, storage.ErrLibraryNotFound
	}
	expired, err := c.core.store.SnapshotExpired(ctx, owner, v.LibraryID, v.CommitID, c.core.now())
	if err != nil {
		return entry, err
	}
	if expired {

		lib, err := c.core.store.GetLibrary(ctx, owner, v.LibraryID)
		if err != nil {
			return entry, err
		}
		current, err := c.publishedEntry(ctx, owner, v.LibraryID, *lib.HeadCommitID, v.Path)
		if err != nil {
			return entry, err
		}
		if current != entry {
			return entry, storage.ErrLibraryNotFound
		}
	}
	// An unchanged snapshot is not a separate timeline record.
	if len(commit.Parents) > 0 {
		parent, err := c.core.readHistoryCommitObject(ctx, owner, v.LibraryID, commit.Parents[0])
		if err != nil {
			return entry, err
		}
		previous, err := c.pathEntry(ctx, owner, v.LibraryID, parent.Root, v.Path)
		if err != nil {
			return entry, err
		}
		if entry == previous {
			return entry, storage.ErrLibraryNotFound
		}
	}
	return entry, nil
}
