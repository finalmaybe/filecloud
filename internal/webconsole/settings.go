package webconsole

import "net/http"

func (c *console) settings(w http.ResponseWriter, r *http.Request, who identity) {
	enabled, err := c.Store.WebDAVEnabled(r.Context())
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, http.StatusOK, "管理设置", "settings", enabled, "")
}

func (c *console) setWebDAV(w http.ResponseWriter, r *http.Request, who identity) {
	value := r.PostForm.Get("enabled")
	if value != "true" && value != "false" {
		http.Error(w, "请选择开启或关闭 WebDAV", http.StatusBadRequest)
		return
	}
	if err := c.Store.SetWebDAVEnabled(r.Context(), value == "true"); err != nil {
		c.problem(w, r, who, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
