package webconsole

import (
	"errors"
	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/storage"
	"net/http"
)

func (c *console) show(w http.ResponseWriter, r *http.Request, who identity, status int, title, view string, data any, msg string) {
	c.render(w, r, status, page{Title: title, View: view, Data: data, SignedIn: true, Admin: who.admin, Message: msg})
}
func (c *console) problem(w http.ResponseWriter, r *http.Request, who identity, err error) {
	code, msg := 503, "服务暂不可用，请稍后重试。"
	switch {
	case errors.Is(err, library.ErrHistoryBusy):
		code, msg = 429, "历史读取繁忙，请稍后再试。"
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, library.ErrInvalidPage):
		code, msg = 400, "分页链接已失效，请重新打开第一页。"
	case errors.Is(err, storage.ErrLibraryNotFound):
		code, msg = 404, "找不到该资料库或版本。"
	}
	c.show(w, r, who, code, "无法打开页面", "error", nil, msg)
}

type libraryList struct {
	Items          []storage.Library
	Next, ID, Name string
}

func (c *console) libraries(w http.ResponseWriter, r *http.Request, who identity) {
	id, err := auth.NewUserID(nil)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.list(w, r, who, id, "", "", 200)
}
func (c *console) list(w http.ResponseWriter, r *http.Request, who identity, id, name, msg string, status int) {
	items, next, err := c.Catalog.List(r.Context(), who.user, r.URL.Query().Get("cursor"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, status, "我的资料库", "libraries", libraryList{items, next, id, name}, msg)
}
func (c *console) createLibrary(w http.ResponseWriter, r *http.Request, who identity) {
	id, name := r.PostForm.Get("id"), r.PostForm.Get("name")
	item, err := c.Catalog.Create(r.Context(), who.user, id, name)
	switch {
	case errors.Is(err, storage.ErrLibraryExists):
		c.list(w, r, who, id, name, "已有同名资料库，请换一个名称。", 409)
	case errors.Is(err, storage.ErrLibraryObjectConflict):
		c.list(w, r, who, id, name, "此创建请求已用于其他名称，请重新打开列表。", 409)
	case errors.Is(err, library.ErrInvalidLibrary):
		c.list(w, r, who, id, name, "请使用有效的资料库标识和 1–128 字的名称。", 400)
	case err != nil:
		c.problem(w, r, who, err)
	default:
		http.Redirect(w, r, "/web/libraries/"+item.ID, 303)
	}
}
func (c *console) history(w http.ResponseWriter, r *http.Request, who identity) {
	q := r.URL.Query()
	data, err := c.Catalog.History(r.Context(), who.user, r.PathValue("library"), q.Get("cursor"), q.Get("commit"), q.Get("merge"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, 200, "主线历史", "history", data, "")
}
func (c *console) libraryDetail(w http.ResponseWriter, r *http.Request, who identity) {
	item, err := c.Store.GetLibrary(r.Context(), who.user, r.PathValue("library"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, 200, item.Name, "library", item, "")
}
