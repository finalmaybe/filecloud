package webconsole

import (
	"errors"
	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/storage"
	"net/http"
)

func (c *console) accounts(w http.ResponseWriter, r *http.Request, who identity) {
	msg := ""
	switch r.URL.Query().Get("done") {
	case "create":
		msg = "账号已创建。"
	case "reset":
		msg = "密码已重置，目标账号的已有会话均已撤销。"
	}
	c.show(w, r, who, 200, "账号管理", "accounts", nil, msg)
}
func (c *console) reviewReset(w http.ResponseWriter, r *http.Request, who identity) {
	name, err := auth.ValidateUsername(r.PostForm.Get("username"))
	if err != nil {
		c.show(w, r, who, 400, "账号管理", "accounts", nil, "请输入目标用户名。")
		return
	}
	c.show(w, r, who, 200, "核对重置影响", "confirm", name, "")
}
func (c *console) saveAccount(w http.ResponseWriter, r *http.Request, who identity) {
	create := r.URL.Path == "/web/accounts/create"
	if !create && r.PostForm.Get("confirm") != "yes" {
		c.show(w, r, who, 400, "核对重置影响", "confirm", r.PostForm.Get("username"), "请先确认会话撤销的影响。")
		return
	}
	err := c.Sessions.SetAccountPassword(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"), r.RemoteAddr, create)
	if err != nil {
		code, msg := 503, "操作未完成，请稍后再试。"
		switch {
		case errors.Is(err, auth.ErrInvalidLogin):
			code, msg = 400, "请检查用户名及密码长度。"
		case errors.Is(err, auth.ErrKDFBusy):
			code, msg = 429, "服务繁忙，请稍后重试。"
			w.Header().Set("Retry-After", "1")
		case errors.Is(err, storage.ErrUsernameExists):
			code, msg = 409, "用户名已存在。"
		case errors.Is(err, storage.ErrUserNotFound):
			code, msg = 404, "目标账号不存在。"
		}
		c.show(w, r, who, code, "账号管理", "accounts", nil, msg)
		return
	}
	done := "reset"
	if create {
		done = "create"
	}
	http.Redirect(w, r, "/web/accounts?done="+done, 303)
}
