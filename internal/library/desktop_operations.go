package library

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func (c *Catalog) registerDesktopOperations(mux *http.ServeMux) {
	// These endpoints accept API sessions only; the console retains its cookie/form handlers.
	for pattern, handle := range map[string]http.HandlerFunc{
		"GET /v1/libraries/{LibraryId}/content":                          c.snapshotContent,
		"POST /v1/libraries/{LibraryId}/file-history/{CommitId}/restore": c.restoreVersionAPI,
		"GET /v1/libraries/{LibraryId}/trash":                            c.trashPage,
		"GET /v1/libraries/{LibraryId}/restore-location":                 c.restoreLocation,
		"POST /v1/libraries/{LibraryId}/trash/{id}/restore":              c.restoreTrashAPI,
		"POST /v1/libraries/{LibraryId}/trash/delete":                    c.deleteTrashAPI,
		"POST /v1/libraries/{LibraryId}/retention":                       c.retentionAPI,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !validUUID(r.PathValue("LibraryId")) {
				c.core.invalid(w)
				return
			}
			handle(w, r)
		})
	}
}

func (c *Catalog) snapshotContent(w http.ResponseWriter, r *http.Request) {
	owner, _ := auth.UserID(r.Context())
	q := r.URL.Query()
	file, err := c.OpenSnapshotFile(r.Context(), owner, VersionTarget{LibraryID: r.PathValue("LibraryId"), CommitID: q.Get("CommitId"), Path: q.Get("Path")})
	if err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, file.Content(r.Context()))
}

func (c *Catalog) restoreVersionAPI(w http.ResponseWriter, r *http.Request) {
	owner, _ := auth.UserID(r.Context())
	q := r.URL.Query()
	anchor := q.Get("AnchorCommitId")
	if !object.ValidID(anchor) {
		c.core.invalid(w)
		return
	}
	head, err := c.RestoreFileVersion(r.Context(), owner, VersionTarget{LibraryID: r.PathValue("LibraryId"), CommitID: r.PathValue("CommitId"), Path: q.Get("Path")}, anchor)
	if err != nil {
		c.changeError(w, err)
		return
	}
	c.core.writeJSON(w, 200, struct {
		RetCode       int
		Message, Head string
	}{0, "success", head})
}

func (c *Catalog) trashPage(w http.ResponseWriter, r *http.Request) {
	owner, _ := auth.UserID(r.Context())
	var before int64
	if v := r.URL.Query().Get("Before"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n < 1 {
			c.core.invalid(w)
			return
		}
		before = n
	}
	lib := r.PathValue("LibraryId")
	if _, err := c.core.store.GetLibrary(r.Context(), owner, lib); err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	rows, err := c.ListTrash(r.Context(), owner, lib, before)
	if err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	type item struct {
		ID                   int64
		Path, Kind           string
		DeletedAt, ExpiresAt time.Time
	}
	page := struct {
		RetCode int
		Message string
		Entries []item
		Next    int64
	}{Message: "success", Entries: []item{}}
	for i, v := range rows {
		if i == storage.TrashPageSize {
			page.Next = rows[i-1].ID
			break
		}
		page.Entries = append(page.Entries, item{v.ID, v.Path, v.Kind, v.DeletedAt, v.ExpiresAt})
	}
	c.core.writeJSON(w, 200, page)
}

func (c *Catalog) restoreLocation(w http.ResponseWriter, r *http.Request) {
	dest := r.URL.Query().Get("Path")
	if !object.ValidPath(dest) || dest == "." {
		c.core.invalid(w)
		return
	}
	owner, _ := auth.UserID(r.Context())
	lib := r.PathValue("LibraryId")
	ctx, release, err := c.admit(r.Context(), owner)
	if err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	defer release()
	done := c.core.store.UseLibrary(owner, lib)
	defer done()
	item, err := c.core.store.GetLibrary(ctx, owner, lib)
	if err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	if item.HeadCommitID == nil {
		c.core.writeBrowseError(w, storage.ErrLibraryNotFound)
		return
	}
	commit, err := c.core.readHistoryCommitObject(ctx, owner, lib, *item.HeadCommitID)
	if err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	_, err = c.placeTrashPath(ctx, owner, lib, commit.Root, strings.Split(dest, "/"), object.DirectoryEntry{}, c.core.now(), true)
	if err != nil && !errors.Is(err, ErrRestoreOccupied) {
		c.changeError(w, err)
		return
	}
	c.core.writeJSON(w, 200, struct {
		RetCode   int
		Message   string
		Available bool
	}{0, "success", err == nil})
}

func (c *Catalog) restoreTrashAPI(w http.ResponseWriter, r *http.Request) {
	owner, _ := auth.UserID(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		c.core.invalid(w)
		return
	}
	if err = c.RestoreTrash(r.Context(), owner, r.PathValue("LibraryId"), id, r.URL.Query().Get("Destination")); err != nil {
		c.changeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Catalog) deleteTrashAPI(w http.ResponseWriter, r *http.Request) {
	ids := decodeTrashDeleteIDs(http.MaxBytesReader(w, r.Body, _maxRequestBody))
	owner, _ := auth.UserID(r.Context())
	err := c.DeleteTrash(r.Context(), owner, r.PathValue("LibraryId"), ids)
	switch {
	case errors.Is(err, storage.ErrInvalidTrashIDs):
		c.core.invalid(w)
	case errors.Is(err, storage.ErrLibraryNotFound):
		c.core.writeError(w, http.StatusNotFound, 2000, "library or trash record not found; no records deleted")
	case err != nil:
		c.core.writeBrowseError(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Accept exactly one list, so a repeated or unknown field cannot replace the
// IDs the caller intended to delete. A nil result fails the store's validation.
func decodeTrashDeleteIDs(r io.Reader) []int64 {
	d := json.NewDecoder(r)
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	if token, err := d.Token(); err != nil || token != "IDs" {
		return nil
	}
	var ids []int64
	if err := d.Decode(&ids); err != nil {
		return nil
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil
	}
	return ids
}

func (c *Catalog) retentionAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		storage.Retention
		Confirm string
		Apply   bool
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, _maxRequestBody)).Decode(&req); err != nil || req.TrashDays < 0 || req.TrashDays > 36500 || req.HistoryDays < -1 || req.HistoryDays > 36500 {
		c.core.invalid(w)
		return
	}
	owner, _ := auth.UserID(r.Context())
	lib, err := c.core.store.GetLibrary(r.Context(), owner, r.PathValue("LibraryId"))
	if err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	impact, err := c.PreviewRetention(r.Context(), lib, req.Retention)
	if err != nil {
		c.core.writeBrowseError(w, err)
		return
	}
	if req.Apply {
		if req.Confirm != impact.Token {
			c.core.writeError(w, 412, 3002, "retention preview changed")
			return
		}
		if err = c.SetRetention(r.Context(), lib, req.Retention); err != nil {
			c.changeError(w, err)
			return
		}
	}
	c.core.writeJSON(w, 200, struct {
		RetCode int
		Message string
		storage.RetentionPreview
	}{0, "success", impact})
}

func (c *Catalog) changeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRestoreOccupied):
		c.core.writeError(w, 409, 3008, "destination occupied")
	case errors.Is(err, storage.ErrHeadConflict), errors.Is(err, ErrCurrentVersion):
		c.core.writeError(w, 412, 3002, "library changed")
	case errors.Is(err, storage.ErrDAVLocked):
		c.core.writeDAVLocked(w)
	default:
		c.core.writeBrowseError(w, err)
	}
}
