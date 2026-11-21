package webconsole

import (
	"errors"
	"net/http"

	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func (c *console) show(w http.ResponseWriter, r *http.Request, who identity, status int, title, view string, data any, msg string) {
	c.render(w, r, status, page{Title: title, View: view, Data: data, SignedIn: true, Admin: who.admin, Message: msg})
}
func (c *console) problem(w http.ResponseWriter, r *http.Request, who identity, err error) {
	code, msg := 503, "服务暂不可用，请稍后重试。"
	switch {
	case errors.Is(err, object.ErrInvalidPersistedObject):
		code, msg = 500, "已发布的对象无法通过完整性检查，请联系管理员停服检查。"
	case errors.Is(err, library.ErrHistoryBusy):
		code, msg = 429, "历史读取繁忙，请稍后再试。"
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, library.ErrInvalidPath):
		code, msg = 400, "请选择快照中的规范相对路径。"
	case errors.Is(err, library.ErrInvalidPage):
		code, msg = 400, "分页链接已失效，请重新打开第一页。"
	case errors.Is(err, storage.ErrLibraryNotFound):
		code, msg = 404, "找不到该资料库或版本。"
	}
	c.show(w, r, who, code, "无法打开页面", "error", nil, msg)
}

func (c *console) fileHistory(w http.ResponseWriter, r *http.Request, who identity) {
	q := r.URL.Query()
	data, err := c.Catalog.FileHistory(r.Context(), who.user, r.PathValue("library"), q.Get("commit"), q.Get("path"), q.Get("cursor"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, 200, "文件历史版本", "file-history", data, "")
}

func (c *console) libraryDetail(w http.ResponseWriter, r *http.Request, who identity) {
	item, err := c.Store.GetLibrary(r.Context(), who.user, r.PathValue("library"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, 200, item.Name, "library", item, "")
}
