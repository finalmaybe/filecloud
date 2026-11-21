package library

import (
	"context"
	"path"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

type FileVersion struct {
	CommitID, Time, Message string
}

type FileHistory struct {
	LibraryID, Anchor, Path, Parent, Next string
	Versions                              []FileVersion
}

// FileHistory lists mainline commits that introduced a file state at the given path.
func (c *Catalog) FileHistory(ctx context.Context, owner, lib, anchor, filePath, cursor string) (FileHistory, error) {
	result := FileHistory{LibraryID: lib, Anchor: anchor, Path: filePath, Parent: path.Dir(filePath)}
	if !object.ValidPath(filePath) || filePath == "." {
		return result, ErrInvalidPath
	}
	ctx, done, err := c.admit(ctx, owner)
	if err != nil {
		return result, err
	}
	defer done()

	token := historyPageToken{AnchorCommitID: anchor, NextCommitID: anchor}
	if cursor == "" {
		role, found, err := c.core.store.GetPublishedCommitRole(ctx, owner, lib, anchor)
		if err != nil {
			return result, err
		}
		if !found || role.Role != _historyRoleMainline {
			return result, storage.ErrLibraryNotFound
		}
	} else {
		token, err = c.core.decodeHistoryToken(cursor, owner)
		if err != nil || token.LibraryID != lib || token.AnchorCommitID != anchor {
			return result, ErrInvalidPage
		}
	}

	// Look one commit past the page so an unchanged file at the boundary is not listed twice.
	const pageSize = 50
	page, _, err := c.core.historyPage(ctx, owner, lib, token, pageSize+1)
	if err != nil {
		return result, err
	}
	entries := make([]object.DirectoryEntry, len(page.Commits)+1)
	for i, commit := range page.Commits {
		if i > 0 && commit.Root == page.Commits[i-1].Root {
			entries[i] = entries[i-1]
		} else {
			entries[i], err = c.pathEntry(ctx, owner, lib, commit.Root, filePath)
			if err != nil {
				return result, err
			}
		}
		if i == 0 && cursor == "" && entries[i].Type != "File" {
			return result, storage.ErrLibraryNotFound
		}
	}
	for i, commit := range page.Commits {
		if i == pageSize {
			result.Next, err = c.core.encodeHistoryToken(owner, lib, anchor, commit.CommitID)
			return result, err
		}
		if entries[i].Type == "File" && entries[i] != entries[i+1] {
			result.Versions = append(result.Versions, FileVersion{commit.CommitID, commit.CreatedAt, commit.Message})
		}
	}
	return result, nil
}
