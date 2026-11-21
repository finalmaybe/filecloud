package library

import (
	"context"
	"net/http"
	"net/url"
	"path"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

type FileRevision struct {
	CommitID   string `json:"CommitId"`
	FileID     string `json:"FileId"`
	ModifiedAt string
}

type FileVersion struct {
	FileRevision
	Time            string `json:"CreatedAt"`
	Message, Change string
}

type FileHistory struct {
	LibraryID string `json:"LibraryId"`
	Anchor    string `json:"AnchorCommitId"`
	Path      string
	Parent    string        `json:"-"`
	Next      string        `json:"NextPageToken"`
	Current   *FileRevision // Only on the first page.
	Versions  []FileVersion
}

func (c *Catalog) getFileHistory(w http.ResponseWriter, r *http.Request) {
	h := c.core
	owner, _ := auth.UserID(r.Context())
	lib := r.PathValue("LibraryId")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !validUUID(lib) || !object.ValidID(q.Get("CommitId")) {
		h.invalid(w)
		return
	}
	for key, values := range q {
		if (key != "CommitId" && key != "Path" && key != "PageToken") || len(values) != 1 || values[0] == "" {
			h.invalid(w)
			return
		}
	}
	page, err := c.FileHistory(r.Context(), owner, lib, q.Get("CommitId"), q.Get("Path"), q.Get("PageToken"))
	if err != nil {
		h.writeBrowseError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, struct {
		RetCode     int
		Message     string
		FileHistory FileHistory
	}{0, "success", page})
}

// FileHistory lists mainline commits that introduced a file state at the given path.
func (c *Catalog) FileHistory(ctx context.Context, owner, lib, anchor, filePath, cursor string) (FileHistory, error) {
	result := FileHistory{LibraryID: lib, Anchor: anchor, Path: filePath, Parent: path.Dir(filePath), Versions: []FileVersion{}}
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
		if i == 0 && cursor == "" {
			if entries[i].Type != "File" {
				return result, storage.ErrLibraryNotFound
			}
			result.Current = &FileRevision{CommitID: anchor, FileID: entries[i].ID, ModifiedAt: entries[i].ModifiedAt}
		}
	}
	for i, commit := range page.Commits {
		if i == pageSize {
			result.Next, err = c.core.encodeHistoryToken(owner, lib, anchor, commit.CommitID)
			return result, err
		}
		current, parent := entries[i], entries[i+1]
		if current.Type != "File" || current == parent {
			continue
		}
		change := "modified-time"
		switch {
		case parent.Type != "File":
			change = "added"
		case current.ID != parent.ID:
			change = "content"
		}
		result.Versions = append(result.Versions, FileVersion{
			FileRevision: FileRevision{CommitID: commit.CommitID, FileID: current.ID, ModifiedAt: current.ModifiedAt},
			Time:         commit.CreatedAt, Message: commit.Message, Change: change,
		})
	}
	return result, nil
}
