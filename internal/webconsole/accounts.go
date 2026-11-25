package webconsole

import (
	"errors"
	"net/http"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func (c *console) accounts(w http.ResponseWriter, r *http.Request, who identity) {
	msg := ""
	switch r.URL.Query().Get("done") {
	case "create":
		msg = "账号已创建。"
	case "reset":
		msg = "密码已重置，目标账号的已有会话均已撤销。"
	}
	c.showAccounts(w, r, who, http.StatusOK, msg)
}

func (c *console) showAccounts(w http.ResponseWriter, r *http.Request, who identity, status int, msg string) {
	accounts, err := c.Store.ListAccounts(r.Context())
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	data := struct {
		Accounts []storage.AccountSummary
		Admins   map[string]bool
		Failed   bool
	}{accounts, c.admins, status >= 400}
	c.show(w, r, who, status, "账号管理", "accounts", data, msg)
}

func (c *console) saveAccount(w http.ResponseWriter, r *http.Request, who identity) {
	create := r.URL.Path == "/accounts/create"
	inlineReset := !create && r.Header.Get("Accept") == "text/plain"
	if !create && r.PostForm.Get("confirm") != "yes" {
		http.Error(w, "请先核对账号并确认重置密码。", http.StatusBadRequest)
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
		if inlineReset {
			http.Error(w, msg, code)
		} else {
			c.showAccounts(w, r, who, code, msg)
		}
		return
	}
	if inlineReset {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	done := "reset"
	if create {
		done = "create"
	}
	http.Redirect(w, r, "/accounts?done="+done, 303)
}
