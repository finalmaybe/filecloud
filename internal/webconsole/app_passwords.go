package webconsole

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/storage"
)

type appPasswordPage struct {
	LibraryID, LibraryName, Address, Username, Secret string
	Credentials                                       []storage.DAVCredential
	Now                                               time.Time
}

func (c *console) appPasswords(w http.ResponseWriter, r *http.Request, who identity) {
	ctx := r.Context()
	lib, err := c.Store.GetLibrary(ctx, who.user, r.PathValue("library"))
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	data := appPasswordPage{LibraryID: lib.ID, LibraryName: lib.Name, Now: time.Now().UTC()}
	if c.DAVOrigin != "" {
		data.Address = c.DAVOrigin + "/dav/" + lib.ID + "/"
	}
	status := 200
	if r.Method == "POST" {
		if data.Address == "" {
			http.Error(w, "WebDAV 未启用", 403)
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 128 {
			http.Error(w, "请输入不超过 128 个字符的凭据名称", 400)
			return
		}
		data.Username, err = auth.NewUserID(nil)
		if err == nil {
			data.Secret, err = c.Store.CreateDAVCredential(ctx, who.user, lib.ID, data.Username, name, data.Now)
		}
		if err != nil {
			c.problem(w, r, who, err)
			return
		}
		status = 201
	}
	data.Credentials, err = c.Store.DAVCredentials(ctx, who.user, lib.ID)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, status, "应用密码", "app-passwords", data, "")
}
func (c *console) revokePassword(w http.ResponseWriter, r *http.Request, who identity) {
	if err := c.Store.RevokeDAV(r.Context(), who.user, r.PathValue("library"), r.PathValue("credential")); err != nil {
		c.problem(w, r, who, err)
		return
	}
	http.Redirect(w, r, "/web/libraries/"+r.PathValue("library")+"/app-passwords", http.StatusSeeOther)
}
