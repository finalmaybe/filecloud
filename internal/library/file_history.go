package library

import (
	"context"
	"errors"
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
	IsCurrent       bool
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

func (c *Catalog) deleteFileVersion(w http.ResponseWriter, r *http.Request) {
	h := c.core
	q, err := url.ParseQuery(r.URL.RawQuery)
	v := VersionTarget{LibraryID: r.PathValue("LibraryId"), CommitID: r.PathValue("CommitId"), Path: q.Get("Path")}
	anchor := q.Get("AnchorCommitId")
	if err != nil || !validUUID(v.LibraryID) || len(q) != 2 || len(q["Path"]) != 1 || len(q["AnchorCommitId"]) != 1 || !object.ValidID(anchor) {
		h.invalid(w)
		return
	}
	owner, _ := auth.UserID(r.Context())
	switch err := c.DeleteFileVersion(r.Context(), owner, v, anchor); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, storage.ErrHeadConflict):
		h.writeError(w, http.StatusPreconditionFailed, 3002, "library head changed")
	case errors.Is(err, ErrCurrentVersion):
		h.writeError(w, http.StatusConflict, 3007, "cannot delete the current file version")
	default:
		h.writeBrowseError(w, err)
	}
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

	pathHash := object.ID([]byte(filePath))
	token := historyPageToken{AnchorCommitID: anchor, NextCommitID: anchor, FilePathHash: pathHash}
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
		if err != nil || token.LibraryID != lib || token.AnchorCommitID != anchor || token.FilePathHash != pathHash {
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
	ids := make([]string, 0, len(page.Commits))
	for _, commit := range page.Commits {
		ids = append(ids, commit.CommitID)
	}
	deleted, err := c.core.store.DeletedFileVersions(ctx, owner, lib, filePath, ids)
	if err != nil {
		return result, err
	}
	for i, commit := range page.Commits {
		if i == pageSize {
			token.NextCommitID = commit.CommitID
			result.Next, err = c.core.encodeHistoryToken(owner, lib, token)
			return result, err
		}
		current, parent := entries[i], entries[i+1]
		if current.Type != "File" || current == parent {
			continue
		}
		// The newest change at this path owns the current version, even if its row was hidden.
		isCurrent := !token.PastCurrent
		token.PastCurrent = true
		if deleted[commit.CommitID] {
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
			Time:         commit.CreatedAt, Message: commit.Message, Change: change, IsCurrent: isCurrent,
		})
	}
	return result, nil
}
