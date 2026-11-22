package webconsole

import (
	"errors"
	"net/http"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/storage"
)

type identity struct {
	user  string
	admin bool
}
type privatePage func(http.ResponseWriter, *http.Request, identity)

func (c *console) current(r *http.Request) (identity, error) {
	cookie, err := r.Cookie(auth.BrowserCookieName)
	if err != nil {
		return identity{}, storage.ErrSessionNotFound
	}
	id, err := c.Sessions.BrowserUser(r.Context(), cookie.Value)
	return identity{user: id, admin: c.admins[id]}, err
}
func (c *console) private(next privatePage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		who, err := c.current(r)
		if errors.Is(err, storage.ErrSessionNotFound) {
			if r.Method == http.MethodPost {
				http.Error(w, "请重新登录", 401)
			} else {
				http.Redirect(w, r, "/web/login", 303)
			}
			return
		}
		if err != nil {
			http.Error(w, "会话暂不可用", 503)
			return
		}
		if lib := r.PathValue("library"); lib != "" {
			done := c.Store.UseLibrary(who.user, lib)
			defer done()
		}
		next(w, r, who)
	}
}
func (c *console) admin(next privatePage) http.HandlerFunc {
	return c.private(func(w http.ResponseWriter, r *http.Request, who identity) {
		if !who.admin {
			c.show(w, r, who, 403, "需要管理员权限", "error", nil, "此操作仅对服务器本机授权的管理员开放。")
			return
		}
		next(w, r, who)
	})
}
func (c *console) cookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{Name: auth.BrowserCookieName, Value: value, Path: "/", HttpOnly: true, Secure: c.origin.Secure(), SameSite: http.SameSiteLaxMode, Expires: expires}
}
func (c *console) login(w http.ResponseWriter, r *http.Request) {
	session, err := c.Sessions.LoginBrowser(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"), r.RemoteAddr)
	if err != nil {
		status, msg := 503, "登录服务暂不可用"
		switch {
		case errors.Is(err, auth.ErrInvalidLogin):
			status, msg = 400, "请检查用户名和密码格式"
		case errors.Is(err, auth.ErrCredentials):
			status, msg = 401, "用户名或密码错误"
		case errors.Is(err, auth.ErrKDFBusy):
			status, msg = 429, "登录繁忙，请稍后再试"
			w.Header().Set("Retry-After", "1")
		}
		c.render(w, r, status, page{Title: "账号登录", View: "login", Message: msg})
		return
	}
	http.SetCookie(w, c.cookie(session.AccessToken, session.ExpiresAt))
	c.clearLegacyCookie(w)
	http.Redirect(w, r, "/web/libraries", 303)
}
func (c *console) logout(w http.ResponseWriter, r *http.Request, who identity) {
	cookie, _ := r.Cookie(auth.BrowserCookieName)
	if err := c.Sessions.LogoutBrowser(r.Context(), cookie.Value); err != nil {
		http.Error(w, "注销未完成，请重试", 503)
		return
	}
	expired := c.cookie("", time.Unix(1, 0))
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	c.clearLegacyCookie(w)
	http.Redirect(w, r, "/web/login", 303)
}
func (c *console) clearLegacyCookie(w http.ResponseWriter) {
	old := c.cookie("", time.Unix(1, 0))
	old.Path, old.MaxAge = "/web/", -1
	http.SetCookie(w, old)
}
func (c *console) overview(w http.ResponseWriter, r *http.Request, who identity) {
	ready := "已就绪"
	if err := c.Store.CheckReady(r.Context()); err != nil {
		ready = "未就绪"
	}
	c.show(w, r, who, 200, "服务概览", "overview", struct {
		Build       Build
		Ready, Time string
	}{c.Build, ready, time.Now().UTC().Format(time.RFC3339)}, "")
}
