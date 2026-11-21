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

func (c *console) snapshot(w http.ResponseWriter, r *http.Request, who identity) {
	q := r.URL.Query()
	v, err := c.requestedSnapshot(r, who, r.PathValue("commit"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	if q.Has("shell") {
		shell := q.Get("shell")
		if shell != "sh" && shell != "ps" {
			c.problem(w, r, who, library.ErrInvalidPath)
			return
		}
		c.show(w, r, who, 200, "交给本机预览", "handoff", guide(v, shell == "ps"), "")
		return
	}
	c.show(w, r, who, 200, "快照检查", "snapshot", v, "")
}
