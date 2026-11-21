package webconsole

import (
	"net/http"
	"strconv"

	"github.com/mingming-cn/filecloud/internal/library"
)

func (c *console) requestedSnapshot(r *http.Request, who identity, commit string) (library.Snapshot, error) {
	q := r.URL.Query()
	path := q.Get("path")
	if path == "" {
		path = "."
	}
	offset := 0
	if q.Has("offset") {
		var err error
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil {
			return library.Snapshot{}, library.ErrInvalidPage
		}
	}
	return c.Catalog.Snapshot(r.Context(), who.user, r.PathValue("library"), commit, path, offset)
}
