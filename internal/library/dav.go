package library

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"slices"
	"sort"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
	"golang.org/x/text/cases"
)

var ErrDAVConflict = errors.New("incompatible path operation")

// DAVTree resolves names against a single published Head, including a not-yet-initialized library.
type DAVTree struct {
	c       *Catalog
	Library storage.Library
	root    string
}

func (c *Catalog) DAVTree(ctx context.Context, owner, lib string) (DAVTree, error) {
	l, err := c.core.store.GetLibrary(ctx, owner, lib)
	t := DAVTree{c: c, Library: l}
	if err == nil && l.HeadCommitID != nil {
		var commit object.Commit
		commit, err = c.core.readHistoryCommitObject(ctx, owner, lib, *l.HeadCommitID)
		t.root = commit.Root
	}
	return t, err
}
func (t DAVTree) Entry(ctx context.Context, p string) (object.DirectoryEntry, error) {
	if t.root == "" {
		if p == "." {
			return object.DirectoryEntry{Type: "Directory"}, nil
		}
		return object.DirectoryEntry{}, nil
	}
	return t.c.pathEntry(ctx, t.Library.OwnerUserID, t.Library.ID, t.root, p)
}
func (t DAVTree) Directory(ctx context.Context, id string) (object.Directory, error) {
	if id == "" {
		return object.Directory{}, nil
	}
	return t.c.directory(ctx, t.Library.OwnerUserID, t.Library.ID, id)
}
func (t DAVTree) File(ctx context.Context, e object.DirectoryEntry) (VersionFile, error) {
	return t.c.openEntryFile(ctx, t.Library.OwnerUserID, t.Library.ID, e)
}
func (t DAVTree) put(ctx context.Context, kind string, b []byte) (string, error) {
	return t.c.putVersionObject(ctx, t.Library.OwnerUserID, t.Library.ID, kind, b)
}
func (t DAVTree) Upload(ctx context.Context, r io.Reader, expectedSize int64) (string, error) {
	s := t.c.core.store
	owner, lib := t.Library.OwnerUserID, t.Library.ID
	release, err := s.AcquireObjectUpload(owner)
	if err != nil {
		return "", err
	}
	defer release()
	var f object.File
	var buf bytes.Buffer
	for {
		buf.Reset()
		// CopyN preserves a transport's unexpected EOF; only a clean EOF ends a file.
		n, err := io.CopyN(&buf, r, object.MaxBlockSize)
		if err != nil && err != io.EOF {
			return "", err
		}
		if n > 0 {
			if len(f.Blocks) >= object.MaxFileBlocks {
				return "", object.ErrPayloadTooLarge
			}
			id := object.ID(buf.Bytes())
			done, resErr := s.ReserveBlockUpload(ctx, owner, lib, id, int64(n))
			if resErr != nil {
				return "", resErr
			}
			_, writeErr := s.PutObjectSized(ctx, owner, lib, "blocks", id, bytes.NewReader(buf.Bytes()), n)
			done()
			if writeErr != nil {
				return "", writeErr
			}
			f.Blocks = append(f.Blocks, id)
			f.Size += int64(n)
		}
		if err != nil {
			break
		}
	}
	if expectedSize >= 0 && f.Size != expectedSize {
		return "", io.ErrUnexpectedEOF
	}
	return t.put(ctx, "files", f.Bytes())
}

// patch rebuilds only directories on this path. Missing parents are not created.
func (t DAVTree) patch(ctx context.Context, root, p string, value object.DirectoryEntry) (string, error) {
	// Remember positions rather than retaining every ancestor's directory in memory.
	type ancestor struct {
		id    string
		index int
	}
	var chain []ancestor
	parentID := root
	if parent := path.Dir(p); parent != "." {
		if root == "" {
			return "", ErrDAVConflict
		}
		entry, err := t.c.walkPath(ctx, t.Library.OwnerUserID, t.Library.ID, root, parent, func(id string, index int) { chain = append(chain, ancestor{id, index}) })
		if err != nil {
			return "", err
		}
		if entry.Type != "Directory" {
			return "", ErrDAVConflict
		}
		parentID = entry.ID
	}
	dir, err := t.Directory(ctx, parentID)
	if err != nil {
		return "", err
	}
	name := path.Base(p)
	i := sort.Search(len(dir.Entries), func(i int) bool { return dir.Entries[i].Name >= name })
	found := i < len(dir.Entries) && dir.Entries[i].Name == name
	if value.Type == "" {
		if !found {
			return "", storage.ErrLibraryNotFound
		}
		dir.Entries = slices.Delete(dir.Entries, i, i+1)
	} else {
		value.Name = name
		if found {
			dir.Entries[i] = value
		} else {
			if len(dir.Entries) >= object.MaxDirectoryEntries {
				return "", object.ErrPayloadTooLarge
			}
			fold := cases.Fold()
			key := fold.String(name)
			for _, e := range dir.Entries {
				if fold.String(e.Name) == key {
					return "", ErrDAVConflict
				}
			}
			dir.Entries = slices.Insert(dir.Entries, i, value)
		}
	}
	id, err := t.put(ctx, "directories", dir.Bytes())
	for _, a := range slices.Backward(chain) {
		if err != nil {
			return "", err
		}
		dir, err = t.Directory(ctx, a.id)
		if err != nil {
			return "", err
		}
		dir.Entries[a.index].ID = id
		id, err = t.put(ctx, "directories", dir.Bytes())
	}
	return id, err
}

// Mutate builds one candidate for both ends of MOVE. Callers must have evaluated
// all resource preconditions on this tree; the final transaction checks Head again.
func (t DAVTree) Mutate(ctx context.Context, method, src, dst, fileID string, shallow bool, d storage.DAVChange) error {
	c := t.c
	release, ok := c.core.headLimiter.tryAcquire(t.Library.OwnerUserID, t.Library.ID)
	if !ok {
		return ErrHistoryBusy
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, c.core.headValidation.RequestTimeout)
	defer cancel()
	root := t.root
	old, err := t.Entry(ctx, src)
	if err != nil {
		return err
	}
	stamp := c.core.now().UTC().Truncate(time.Second).Format(time.RFC3339)
	switch method {
	case "PUT", "LOCK":
		if old.Type == "Directory" {
			return ErrDAVConflict
		}
		if old.ID != fileID {
			root, err = t.patch(ctx, root, src, object.DirectoryEntry{Type: "File", ID: fileID, ModifiedAt: stamp})
		}
	case "MKCOL":
		if old.Type != "" {
			return ErrDAVConflict
		}
		var id string
		id, err = t.put(ctx, "directories", object.Directory{}.Bytes())
		if err == nil {
			root, err = t.patch(ctx, root, src, object.DirectoryEntry{Type: "Directory", ID: id, ModifiedAt: stamp})
		}
	case "DELETE":
		root, err = t.patch(ctx, root, src, object.DirectoryEntry{})
	case "MOVE", "COPY":
		if old.Type == "" {
			return storage.ErrLibraryNotFound
		}
		if src == dst || storage.DAVDescendant(src, dst) || storage.DAVDescendant(dst, src) {
			return ErrDAVConflict
		}
		if method == "MOVE" {
			root, err = t.patch(ctx, root, src, object.DirectoryEntry{})
		} else if shallow && old.Type == "Directory" {
			old.ID, err = t.put(ctx, "directories", object.Directory{}.Bytes())
		}
		if err == nil {
			root, err = t.patch(ctx, root, dst, old)
		}
	default:
		return ErrDAVConflict
	}
	if err != nil {
		return err
	}
	if t.root == root {
		return c.core.store.ApplyDAV(ctx, t.Library, d, c.core.now())
	}
	_, err = c.publishRoot(ctx, t.Library, root, "WebDAV "+method, d.Credential, storage.HeadChange{DAV: d})
	if errors.Is(err, errInvalidSnapshot) {
		return ErrDAVConflict
	}
	return err
}

// ChangeDAVLock orders grants, refreshes and releases with Head publication on this library.
func (t DAVTree) ChangeDAVLock(ctx context.Context, d storage.DAVChange) error {
	release, ok := t.c.core.headLimiter.tryAcquire(t.Library.OwnerUserID, t.Library.ID)
	if !ok {
		return ErrHistoryBusy
	}
	defer release()
	return t.c.core.store.ApplyDAV(ctx, t.Library, d, t.c.core.now())
}

// AdmitDAVListing shares the owner/global traversal budget with snapshot browsing.
func (c *Catalog) AdmitDAVListing(ctx context.Context, owner string) (context.Context, func(), error) {
	return c.admit(ctx, owner)
}

func (c *Catalog) nativeLockCheck(ctx context.Context, owner, lib string, before *string, after string) (storage.DAVChange, error) {
	locks, err := c.core.store.DAVLocks(ctx, owner, lib, c.core.now())
	d := storage.DAVChange{Checked: locks}
	if err != nil || len(locks) == 0 {
		return d, err
	}
	a := DAVTree{c: c, Library: storage.Library{ID: lib, OwnerUserID: owner}}
	b := a
	if before != nil {
		v, err := c.core.readHistoryCommitObject(ctx, owner, lib, *before)
		if err != nil {
			return d, err
		}
		a.root = v.Root
	}
	v, err := c.core.readHistoryCommitObject(ctx, owner, lib, after)
	if err != nil {
		return d, err
	}
	b.root = v.Root
	for _, l := range locks {
		x, err := a.Entry(ctx, l.Path)
		if err != nil {
			return d, err
		}
		y, err := b.Entry(ctx, l.Path)
		if err != nil {
			return d, err
		}
		if x == y {
			continue
		}
		if l.Deep || x.Type != "Directory" || y.Type != x.Type || x.ModifiedAt != y.ModifiedAt {
			return d, storage.ErrDAVLocked
		}
		left, err := a.Directory(ctx, x.ID)
		if err != nil {
			return d, err
		}
		right, err := b.Directory(ctx, y.ID)
		if err != nil {
			return d, err
		}
		if !slices.EqualFunc(left.Entries, right.Entries, func(a, b object.DirectoryEntry) bool { return a.Name == b.Name && a.Type == b.Type }) {
			return d, storage.ErrDAVLocked
		}
	}
	return d, nil
}

// DAVTouched is the namespace affected by the request, including child locks on
// a removed subtree and a depth-zero parent's membership on create/delete.
func DAVTouched(l storage.DAVLock, p string, membership, remove bool) bool {
	return l.Covers(p) || membership && p != "." && l.Path == path.Dir(p) || remove && storage.DAVDescendant(l.Path, p)
}
