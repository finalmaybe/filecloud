package webconsole

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/mingming-cn/filecloud/internal/storage"
)

type settingsPage struct {
	WebDAVEnabled         bool
	HistoryConcurrency    storage.HistoryConcurrency
	MaxHistoryConcurrency int
	HistorySaved          bool
}

func (c *console) settings(w http.ResponseWriter, r *http.Request, who identity) {
	c.showSettings(w, r, who, http.StatusOK, "")
}

func (c *console) showSettings(w http.ResponseWriter, r *http.Request, who identity, status int, message string) {
	enabled, err := c.Store.WebDAVEnabled(r.Context())
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	c.show(w, r, who, status, "管理设置", "settings", settingsPage{
		WebDAVEnabled:         enabled,
		HistoryConcurrency:    c.Catalog.HistoryConcurrency(),
		MaxHistoryConcurrency: storage.MaxSavedHistoryConcurrency,
		HistorySaved:          status == http.StatusOK && r.URL.Query().Get("saved") == "1",
	}, message)
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

func (c *console) setHistoryConcurrency(w http.ResponseWriter, r *http.Request, who identity) {
	invalid := func() {
		c.showSettings(w, r, who, http.StatusBadRequest, fmt.Sprintf("并发上限须为 1–%d 的整数。", storage.MaxSavedHistoryConcurrency))
	}
	global, globalOK := parseHistoryConcurrency(r.PostForm["global"])
	user, userOK := parseHistoryConcurrency(r.PostForm["user"])
	if !globalOK || !userOK {
		invalid()
		return
	}
	if err := c.Catalog.SetHistoryConcurrency(r.Context(), storage.HistoryConcurrency{Global: global, User: user}); err != nil {
		if errors.Is(err, storage.ErrInvalidHistoryConcurrency) {
			invalid()
			return
		}
		c.problem(w, r, who, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func parseHistoryConcurrency(values []string) (int, bool) {
	if len(values) != 1 {
		return 0, false
	}
	n, err := strconv.ParseUint(values[0], 10, strconv.IntSize-1)
	return int(n), err == nil
}
