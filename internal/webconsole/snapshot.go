package webconsole

import (
	"github.com/mingming-cn/filecloud/internal/library"
	"net/http"
	"strconv"
)

func (c *console) snapshot(w http.ResponseWriter, r *http.Request, who identity) {
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
			c.problem(w, r, who, library.ErrInvalidPage)
			return
		}
	}
	v, err := c.Catalog.Snapshot(r.Context(), who.user, r.PathValue("library"), r.PathValue("commit"), path, offset)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, 200, "快照检查", "snapshot", v, "")
}
