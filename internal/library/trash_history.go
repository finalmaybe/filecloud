package library

import (
	"context"
	"path"
	"sort"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

// Keep the file revisions visible at deletion with the bin item. Compare each pair
// of snapshots once, skipping unchanged subtrees even for a large directory deletion.
func (c *Catalog) archiveRemovedHistory(ctx context.Context, owner, lib, head string, removed []storage.TrashEntry) error {
	if len(removed) == 0 {
		return nil
	}
	files := map[string]int{}
	dirs := map[string]bool{".": true}
	var collect func(string, object.DirectoryEntry, int) error
	collect = func(p string, e object.DirectoryEntry, index int) error {
		if e.Type == "File" {
			files[p] = index
			return nil
		}
		dirs[p] = true
		d, err := c.directory(ctx, owner, lib, e.ID)
		if err != nil {
			return err
		}
		for _, child := range d.Entries {
			if err := collect(path.Join(p, child.Name), child, index); err != nil {
				return err
			}
		}
		return nil
	}
	for i, t := range removed {
		for p := path.Dir(t.Path); p != "." && !dirs[p]; p = path.Dir(p) {
			dirs[p] = true
		}
		if err := collect(t.Path, t.Entry, i); err != nil {
			return err
		}
	}
	if len(files) == 0 {
		return nil
	}
	seen := map[string]bool{}
	now := c.core.now()
	for id := head; id != ""; {
		cur, err := c.core.readHistoryCommit(ctx, owner, lib, id)
		if err != nil {
			return err
		}
		expired, err := c.core.store.SnapshotExpired(ctx, owner, lib, id, now)
		if err != nil {
			return err
		}
		if expired && len(seen) == len(files) {
			break
		}
		parent, base := "", ""
		if len(cur.Parents) > 0 {
			parent = cur.Parents[0]
			prev, err := c.core.readHistoryCommitObject(ctx, owner, lib, parent)
			if err != nil {
				return err
			}
			base = prev.Root
		}
		var walk func(string, string, string) error
		walk = func(prefix, a, b string) error {
			if a == b {
				return nil
			}
			d, err := c.directory(ctx, owner, lib, a)
			if err != nil {
				return err
			}
			var prev object.Directory
			if b != "" {
				prev, err = c.directory(ctx, owner, lib, b)
				if err != nil {
					return err
				}
			}
			for _, entry := range d.Entries {
				p := path.Join(prefix, entry.Name)
				idx, tracked := files[p]
				if !tracked && !dirs[p] {
					continue
				}
				var old object.DirectoryEntry
				j := sort.Search(len(prev.Entries), func(i int) bool { return prev.Entries[i].Name >= entry.Name })
				if j < len(prev.Entries) && prev.Entries[j].Name == entry.Name {
					old = prev.Entries[j]
				}
				if entry == old {
					continue
				}
				if entry.Type == "Directory" && dirs[p] {
					base := ""
					if old.Type == "Directory" {
						base = old.ID
					}
					if err := walk(p, entry.ID, base); err != nil {
						return err
					}
					continue
				}
				if entry.Type != "File" || !tracked || (expired && seen[p]) {
					continue
				}
				change := "added"
				if old.Type == "File" {
					change = "content"
					if old.ID == entry.ID {
						change = "modified-time"
					}
				}
				removed[idx].Versions = append(removed[idx].Versions, storage.ArchivedVersion{Path: p, Source: id, FileID: entry.ID, Modified: entry.ModifiedAt, Change: change})
				seen[p] = true
			}
			return nil
		}
		if err := walk(".", cur.Root, base); err != nil {
			return err
		}
		id = parent
	}
	return nil
}
