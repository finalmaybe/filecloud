package library

import (
	"context"
	"errors"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
	"io"
	"sort"
	"strings"
)

var ErrInvalidPath = errors.New("invalid snapshot path")

type Snapshot struct {
	LibraryID, CommitID, Path, Parent, Kind, ID, ModifiedAt string
	Size                                                    int64
	Blocks                                                  int
	Entries                                                 []object.DirectoryEntry
	Next                                                    int
}

// Snapshot resolves from a verified published root; callers cannot select object IDs or disk paths.
func (c *Catalog) Snapshot(ctx context.Context, owner, lib, commit, path string, offset int) (Snapshot, error) {
	v := Snapshot{LibraryID: lib, CommitID: commit, Path: path, Parent: ".", Kind: "Directory"}
	if !object.ValidPath(path) || offset < 0 {
		return v, ErrInvalidPath
	}
	ctx, done, err := c.admit(ctx, owner)
	if err != nil {
		return v, err
	}
	defer done()
	detail, err := c.core.historyDetail(ctx, owner, lib, commit)
	if err != nil {
		return v, err
	}
	v.ID = detail.Root
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		v.Parent = path[:i]
	}
	rest := path
	for {
		if err := ctx.Err(); err != nil {
			return v, err
		}
		if v.Kind == "File" {
			if rest != "." {
				return v, storage.ErrLibraryNotFound
			}
			if offset != 0 {
				return v, ErrInvalidPage
			}
			data, err := c.metadata(ctx, owner, lib, "files", v.ID, object.MaxFileObjectSize)
			if err != nil {
				return v, err
			}
			f, err := object.VerifyFile(data, v.ID)
			if err != nil {
				return v, errHistoryCorrupt
			}
			v.Size, v.Blocks = f.Size, len(f.Blocks)
			return v, nil
		}
		data, err := c.metadata(ctx, owner, lib, "directories", v.ID, object.MaxDirectoryObjectSize)
		if err != nil {
			return v, err
		}
		d, err := object.VerifyDirectory(data, v.ID)
		if err != nil {
			return v, errHistoryCorrupt
		}
		if rest == "." {
			if offset > len(d.Entries) {
				return v, ErrInvalidPage
			}
			end := min(offset+50, len(d.Entries))
			v.Entries = d.Entries[offset:end]
			if end < len(d.Entries) {
				v.Next = end
			}
			return v, nil
		}
		name, tail, more := strings.Cut(rest, "/")
		i := sort.Search(len(d.Entries), func(i int) bool { return d.Entries[i].Name >= name })
		if i == len(d.Entries) || d.Entries[i].Name != name {
			return v, storage.ErrLibraryNotFound
		}
		entry := d.Entries[i]
		v.Kind, v.ID, v.ModifiedAt = entry.Type, entry.ID, entry.ModifiedAt
		rest = tail
		if !more {
			rest = "."
		}
	}
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
