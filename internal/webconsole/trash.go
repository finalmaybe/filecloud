package webconsole

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func (c *console) moveTrash(w http.ResponseWriter, r *http.Request, who identity) {
	v := versionTarget(r, r.PostForm)
	if err := c.Catalog.MoveToTrash(r.Context(), who.user, v); err != nil {
		c.problem(w, r, who, err)
		return
	}
	directory := "/libraries/" + v.LibraryID
	if parent := path.Dir(v.Path); parent != "." {
		directory += "?path=" + url.QueryEscape(parent)
	}
	http.Redirect(w, r, directory, http.StatusSeeOther)
}

type trashPage struct {
	LibraryID, LibraryName, Filter string
	Entries                        []storage.TrashEntry
	Before, Next                   int64
}

func trashBefore(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	before, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || before < 1 {
		return 0, library.ErrInvalidPage
	}
	return before, nil
}

func (c *console) trash(w http.ResponseWriter, r *http.Request, who identity) {
	q := r.URL.Query()
	before, err := trashBefore(q.Get("before"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	msg := ""
	if q.Get("deleted") == "1" {
		msg = "已永久删除所选回收站条目。"
	} else if q.Get("restored") == "1" {
		msg = "已恢复所选条目。"
	}
	c.showTrash(w, r, who, before, q.Get("filter"), http.StatusOK, msg)
}

func (c *console) showTrash(w http.ResponseWriter, r *http.Request, who identity, before int64, filter string, status int, msg string) {
	lib, err := c.Store.GetLibrary(r.Context(), who.user, r.PathValue("library"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	items, err := c.Catalog.ListTrash(r.Context(), who.user, lib.ID, before)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	data := trashPage{LibraryID: lib.ID, LibraryName: lib.Name, Filter: filter, Before: before, Entries: items}
	if len(items) > storage.TrashPageSize {
		data.Entries = items[:storage.TrashPageSize]
		data.Next = data.Entries[len(data.Entries)-1].ID
	}
	c.show(w, r, who, status, "回收站", "trash", data, msg)
}

func (c *console) restoreTrash(w http.ResponseWriter, r *http.Request, who identity) {
	id, err := strconv.ParseInt(r.PostForm.Get("id"), 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "无效的回收站条目", 400)
		return
	}
	lib := r.PathValue("library")
	if err := c.Catalog.RestoreTrash(r.Context(), who.user, lib, id, r.PostForm.Get("destination")); err != nil {
		c.problem(w, r, who, err)
		return
	}
	http.Redirect(w, r, "/libraries/"+lib+"/trash?restored=1", 303)
}

func (c *console) deleteTrash(w http.ResponseWriter, r *http.Request, who identity) {
	raw := r.PostForm["id"]
	if len(raw) == 0 || len(raw) > storage.TrashPageSize {
		http.Error(w, "无效的回收站条目", http.StatusBadRequest)
		return
	}
	ids := make([]int64, 0, len(raw))
	for _, v := range raw {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "无效的回收站条目", http.StatusBadRequest)
			return
		}
		ids = append(ids, id)
	}
	before, err := trashBefore(r.PostForm.Get("before"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	filter := r.PostForm.Get("filter")
	lib := r.PathValue("library")
	err = c.Catalog.DeleteTrash(r.Context(), who.user, lib, ids)
	switch {
	case errors.Is(err, storage.ErrInvalidTrashIDs):
		http.Error(w, "无效的回收站条目", http.StatusBadRequest)
	case errors.Is(err, storage.ErrLibraryNotFound):
		c.showTrash(w, r, who, before, filter, http.StatusNotFound, "所选条目中有记录已不在回收站；本次未删除任何条目。请重新选择。")
	case err != nil:
		c.problem(w, r, who, err)
	default:
		q := url.Values{"deleted": {"1"}}
		if before != 0 {
			q.Set("before", strconv.FormatInt(before, 10))
		}
		if filter != "" {
			q.Set("filter", filter)
		}
		http.Redirect(w, r, "/libraries/"+lib+"/trash?"+q.Encode(), http.StatusSeeOther)
	}
}

type retentionPage struct {
	LibraryID        string
	Policy, Defaults storage.Retention
	Preview          storage.RetentionPreview
}

func (c *console) retention(w http.ResponseWriter, r *http.Request, who identity) {
	ctx := r.Context()
	lib, err := c.Store.GetLibrary(ctx, who.user, r.PathValue("library"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	defaults, err := c.Store.Defaults(ctx)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	data := retentionPage{LibraryID: lib.ID, Policy: lib.Retention, Defaults: defaults}
	msg := ""
	if r.URL.Query().Get("saved") == "1" {
		msg = "保留设置已保存。"
	}
	if r.Method == "POST" {
		next := storage.Retention{}
		for _, item := range []struct {
			name  string
			value *int
		}{{"trash", &next.TrashDays}, {"history", &next.HistoryDays}} {
			s := r.PostForm.Get(item.name)
			if s == "" {
				continue
			}
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 36500 {
				http.Error(w, "天数须为 1–36500，或留空跟随全局默认。", 400)
				return
			}
			*item.value = n
		}
		if r.PostForm.Get("permanent") == "on" {
			next.HistoryDays = -1
		}
		data.Policy = next
		data.Preview, err = c.Catalog.PreviewRetention(ctx, lib, next)
		if err != nil {
			c.problem(w, r, who, err)
			return
		}
		if data.Preview.Token == "" || data.Preview.Token == r.PostForm.Get("confirm") {
			if err := c.Catalog.SetRetention(ctx, lib, next); err != nil {
				c.problem(w, r, who, err)
				return
			}
			http.Redirect(w, r, r.URL.Path+"?saved=1", 303)
			return
		}
		msg = "请核对将过期的快照，再确认缩短保留期。"
	}
	c.show(w, r, who, 200, "保留设置", "retention", data, msg)
}
