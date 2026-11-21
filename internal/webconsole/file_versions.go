package webconsole

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/mingming-cn/filecloud/internal/library"
)

type versionEntry struct {
	library.FileVersion
	LibraryID, Path, Anchor string
}

func versionRow(h library.FileHistory, v library.FileVersion) versionEntry {
	return versionEntry{v, h.LibraryID, h.Path, h.Anchor}
}

func versionTarget(r *http.Request, values url.Values) library.VersionTarget {
	return library.VersionTarget{LibraryID: r.PathValue("library"), Path: values.Get("path"), CommitID: values.Get("commit")}
}

func (c *console) downloadVersion(w http.ResponseWriter, r *http.Request, who identity) {
	v := versionTarget(r, r.URL.Query())
	file, err := c.Catalog.OpenFileVersion(r.Context(), who.user, v)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(v.Path)}))
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	if r.Method != http.MethodHead {
		if _, err := io.Copy(w, file.Content(r.Context())); err != nil {
			// A partial attachment must fail as a download, never end with an HTML error page.
			panic(http.ErrAbortHandler)
		}
	}
}

func (c *console) restoreVersion(w http.ResponseWriter, r *http.Request, who identity) {
	v := versionTarget(r, r.PostForm)
	anchor := r.PostForm.Get("anchor")
	head, err := c.Catalog.RestoreFileVersion(r.Context(), who.user, v, anchor)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	result := "restored"
	if head == anchor {
		result = "unchanged"
	}
	redirectVersionHistory(w, r, v, head, result)
}

func (c *console) deleteVersion(w http.ResponseWriter, r *http.Request, who identity) {
	v := versionTarget(r, r.PostForm)
	anchor := r.PostForm.Get("anchor")
	if err := c.Catalog.DeleteFileVersion(r.Context(), who.user, v, anchor); err != nil {
		c.problem(w, r, who, err)
		return
	}
	redirectVersionHistory(w, r, v, anchor, "deleted")
}

func redirectVersionHistory(w http.ResponseWriter, r *http.Request, v library.VersionTarget, anchor, result string) {
	q := url.Values{"commit": {anchor}, "path": {v.Path}, "result": {result}}
	http.Redirect(w, r, "/web/libraries/"+v.LibraryID+"/file-history?"+q.Encode(), http.StatusSeeOther)
}
