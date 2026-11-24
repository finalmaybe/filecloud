package webconsole

import (
	"net/http"
	"net/url"
	"path"
	"strconv"

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
	LibraryID, LibraryName string
	Entries                []storage.TrashEntry
	Next                   int64
}

func (c *console) trash(w http.ResponseWriter, r *http.Request, who identity) {
	lib, err := c.Store.GetLibrary(r.Context(), who.user, r.PathValue("library"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	var before int64
	if s := r.URL.Query().Get("before"); s != "" {
		before, err = strconv.ParseInt(s, 10, 64)
		if err != nil || before <= 0 {
			http.Error(w, "无效的分页参数", 400)
			return
		}
	}
	items, err := c.Catalog.ListTrash(r.Context(), who.user, lib.ID, before)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	data := trashPage{LibraryID: lib.ID, LibraryName: lib.Name, Entries: items}
	if len(items) > 100 {
		data.Next = items[99].ID
		data.Entries = items[:100]
	}
	msg := ""
	if r.URL.Query().Get("restored") == "1" {
		msg = "已恢复所选条目。"
	}
	c.show(w, r, who, 200, "回收站", "trash", data, msg)
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
