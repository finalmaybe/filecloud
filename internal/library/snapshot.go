package library

import (
	"context"
	"errors"
	"io"
	"math/big"
	"path"
	"sort"
	"strings"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

var ErrInvalidPath = errors.New("invalid snapshot path")

type Snapshot struct {
	LibraryID, CommitID, Path, Parent, Kind, ID, ModifiedAt string
	Size                                                    int64
	Blocks                                                  int
	Entries                                                 []SnapshotEntry
	Next                                                    int
}

type SnapshotEntry struct {
	Name, Type, ModifiedAt string
	Size                   string // Logical bytes, including all descendants for a directory.
}

// Snapshot resolves from a verified published root; callers cannot select object IDs or disk paths.
func (c *Catalog) Snapshot(ctx context.Context, owner, lib, commit, filePath string, offset int) (Snapshot, error) {
	v := Snapshot{LibraryID: lib, CommitID: commit, Path: filePath, Parent: path.Dir(filePath)}
	if !object.ValidPath(filePath) || offset < 0 {
		return v, ErrInvalidPath
	}
	ctx, done, err := c.admit(ctx, owner)
	if err != nil {
		return v, err
	}
	defer done()
	if commit == "" {
		item, err := c.core.store.GetLibrary(ctx, owner, lib)
		if err != nil {
			return v, err
		}
		if item.HeadCommitID == nil {
			if filePath != "." {
				return v, storage.ErrLibraryNotFound
			}
			if offset != 0 {
				return v, ErrInvalidPage
			}
			v.Kind = "Directory"
			v.Entries = []SnapshotEntry{}
			return v, nil
		}
		v.CommitID = *item.HeadCommitID
	}
	entry, err := c.publishedEntry(ctx, owner, lib, v.CommitID, filePath)
	if err != nil {
		return v, err
	}
	if entry.Type == "" {
		return v, storage.ErrLibraryNotFound
	}
	v.Kind, v.ID, v.ModifiedAt = entry.Type, entry.ID, entry.ModifiedAt
	if v.Kind == "File" {
		if offset != 0 {
			return v, ErrInvalidPage
		}
		f, err := c.openEntryFile(ctx, owner, lib, entry)
		if err != nil {
			return v, err
		}
		v.Size, v.Blocks = f.Size, len(f.Blocks)
		return v, nil
	}
	d, err := c.directory(ctx, owner, lib, v.ID)
	if err != nil {
		return v, err
	}
	if offset > len(d.Entries) {
		return v, ErrInvalidPage
	}
	end := min(offset+50, len(d.Entries))
	v.Entries = make([]SnapshotEntry, 0, end-offset)
	sizes := make(map[string]*big.Int)
	for _, entry := range d.Entries[offset:end] {
		size, err := c.entrySize(ctx, owner, lib, entry, sizes)
		if err != nil {
			return v, err
		}
		v.Entries = append(v.Entries, SnapshotEntry{Name: entry.Name, Type: entry.Type, ModifiedAt: entry.ModifiedAt, Size: size.String()})
	}
	if end < len(d.Entries) {
		v.Next = end
	}
	return v, nil
}

// Shared objects need only one read per page, but contribute once for each path.
func (c *Catalog) entrySize(ctx context.Context, owner, lib string, entry object.DirectoryEntry, sizes map[string]*big.Int) (*big.Int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if size := sizes[entry.ID]; size != nil {
		return size, nil
	}
	size := new(big.Int)
	if entry.Type == "File" {
		file, err := c.openEntryFile(ctx, owner, lib, entry)
		if err != nil {
			return nil, err
		}
		size.SetInt64(file.Size)
	} else {
		dir, err := c.directory(ctx, owner, lib, entry.ID)
		if err != nil {
			return nil, err
		}
		for _, child := range dir.Entries {
			childSize, err := c.entrySize(ctx, owner, lib, child, sizes)
			if err != nil {
				return nil, err
			}
			size.Add(size, childSize)
		}
	}
	sizes[entry.ID] = size
	return size, nil
}

func (c *Catalog) publishedEntry(ctx context.Context, owner, lib, commit, filePath string) (object.DirectoryEntry, error) {
	detail, err := c.core.historyDetail(ctx, owner, lib, commit)
	if err != nil {
		return object.DirectoryEntry{}, err
	}
	return c.pathEntry(ctx, owner, lib, detail.Root, filePath)
}

// pathEntry returns a zero entry when the path did not exist in this root.
func (c *Catalog) pathEntry(ctx context.Context, owner, lib, root, path string) (object.DirectoryEntry, error) {
	return c.walkPath(ctx, owner, lib, root, path, nil)
}

func (c *Catalog) walkPath(ctx context.Context, owner, lib, root, path string, visit func(dirID string, index int)) (object.DirectoryEntry, error) {
	entry := object.DirectoryEntry{Type: "Directory", ID: root}
	if path == "." {
		return entry, nil
	}
	for name := range strings.SplitSeq(path, "/") {
		if entry.Type != "Directory" {
			return object.DirectoryEntry{}, nil
		}
		d, err := c.directory(ctx, owner, lib, entry.ID)
		if err != nil {
			return object.DirectoryEntry{}, err
		}
		i := sort.Search(len(d.Entries), func(i int) bool { return d.Entries[i].Name >= name })
		if i == len(d.Entries) || d.Entries[i].Name != name {
			return object.DirectoryEntry{}, nil
		}
		if visit != nil {
			visit(entry.ID, i)
		}
		entry = d.Entries[i]
	}
	return entry, nil
}

func (c *Catalog) directory(ctx context.Context, owner, lib, id string) (object.Directory, error) {
	if err := ctx.Err(); err != nil {
		return object.Directory{}, err
	}
	data, err := c.metadata(ctx, owner, lib, "directories", id, object.MaxDirectoryObjectSize)
	if err != nil {
		return object.Directory{}, err
	}
	d, err := object.VerifyDirectory(data, id)
	if err != nil {
		return object.Directory{}, errHistoryCorrupt
	}
	return d, nil
}

func (c *Catalog) metadata(ctx context.Context, owner, lib, kind, id string, maxSize int64) ([]byte, error) {
	f, n, err := c.core.store.GetObject(ctx, owner, lib, kind, id)
	if err != nil {
		return nil, errHistoryCorrupt
	}
	defer f.Close()
	if n > maxSize {
		return nil, errHistoryCorrupt
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil || int64(len(data)) != n {
		return nil, errHistoryCorrupt
	}
	return data, nil
}
