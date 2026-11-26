package library

import (
	"math/big"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/object"
)

type directoryListing struct {
	CommitID     *string `json:"CommitId"`
	Path, Parent string
	Entries      []SnapshotEntry
	NextOffset   int
}

func (c *Catalog) listFiles(w http.ResponseWriter, r *http.Request) {
	h := c.core
	lib := r.PathValue("LibraryId")
	commit, path, offset, err := parseDirectoryQuery(r.URL.RawQuery)
	if err != nil || !validUUID(lib) {
		h.invalid(w)
		return
	}
	owner, _ := auth.UserID(r.Context())
	page, err := c.Snapshot(r.Context(), owner, lib, commit, path, offset)
	if err != nil {
		h.writeBrowseError(w, err)
		return
	}
	if page.Kind != "Directory" {
		h.invalid(w)
		return
	}
	listing := directoryListing{Path: page.Path, Parent: page.Parent, Entries: page.Entries, NextOffset: page.Next}
	if page.CommitID != "" {
		listing.CommitID = &page.CommitID
	}
	h.writeJSON(w, http.StatusOK, struct {
		RetCode   int
		Message   string
		Directory directoryListing
	}{0, "success", listing})
}

func (c *Catalog) getLibrarySize(w http.ResponseWriter, r *http.Request) {
	h := c.core
	lib := r.PathValue("LibraryId")
	q, err := url.ParseQuery(r.URL.RawQuery)
	commit := q.Get("CommitId")
	if err != nil || !validUUID(lib) || len(q) != 1 || len(q["CommitId"]) != 1 || !object.ValidID(commit) {
		h.invalid(w)
		return
	}
	owner, _ := auth.UserID(r.Context())
	ctx, release, err := c.admit(r.Context(), owner)
	if err != nil {
		h.writeBrowseError(w, err)
		return
	}
	defer release()
	done := h.store.UseLibrary(owner, lib)
	defer done()
	root, err := c.publishedEntry(ctx, owner, lib, commit, ".")
	if err != nil {
		h.writeBrowseError(w, err)
		return
	}
	size, err := c.measureEntry(ctx, owner, lib, root, make(map[string]entryTotals))
	if err != nil {
		h.writeBrowseError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, struct {
		RetCode     int
		Message     string
		Size, Items string
	}{0, "success", size.bytes.String(), new(big.Int).Sub(size.items, big.NewInt(1)).String()})
}

func parseDirectoryQuery(raw string) (commit, path string, offset int, err error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return "", "", 0, err
	}
	path = "."
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return "", "", 0, ErrInvalidPage
		}
		switch key {
		case "CommitId":
			commit = values[0]
			if !object.ValidID(commit) {
				return "", "", 0, ErrInvalidPage
			}
		case "Path":
			path = values[0]
		case "Offset":
			offset, err = strconv.Atoi(values[0])
			if err != nil || strconv.Itoa(offset) != values[0] {
				return "", "", 0, ErrInvalidPage
			}
		default:
			return "", "", 0, ErrInvalidPage
		}
	}
	return commit, path, offset, nil
}
