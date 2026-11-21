// Package webconsole serves the optional, same-origin browser interface.
package webconsole

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/storage"
)

//go:embed *.html assets
var files embed.FS

type Build struct{ Version, Commit, BuildDate string }
type Config struct {
	Build      Build
	AdminUsers []string
	Catalog    *library.Catalog
	Sessions   *auth.Sessions
	Store      *storage.Store
}
type console struct {
	admins map[string]bool
	Config
	shell  *template.Template
	origin *auth.BrowserOrigin
}
type page struct {
	Title, View, Message string
	SignedIn, Admin      bool
	Data                 any
}

// NewHandler builds the Web routes for one configured public origin.
// The server itself speaks HTTP; remote HTTPS terminates at a local reverse proxy.
func NewHandler(origin *auth.BrowserOrigin, config Config) (http.Handler, error) {
	shell, err := template.New("shell.html").ParseFS(files, "*.html")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /web", http.RedirectHandler("/web/", http.StatusMovedPermanently))
	c := &console{Config: config, shell: shell, origin: origin, admins: map[string]bool{}}
	for _, name := range config.AdminUsers {
		if _, err := auth.ValidateUsername(name); err != nil {
			return nil, errors.New("invalid configured Web administrator")
		}
		u, err := config.Store.FindUserByUsername(context.Background(), name)
		if err != nil {
			return nil, errors.New("configured Web administrator must already exist")
		}
		c.admins[u.ID] = true
	}
	mux.HandleFunc("GET /web/{$}", c.private(func(w http.ResponseWriter, r *http.Request, _ identity) {
		http.Redirect(w, r, "/web/libraries", http.StatusSeeOther)
	}))
	mux.HandleFunc("GET /web/login", func(w http.ResponseWriter, r *http.Request) {
		c.render(w, r, 200, page{Title: "账号登录", View: "login"})
	})
	mux.HandleFunc("POST /web/login", c.login)
	mux.HandleFunc("POST /web/logout", c.private(c.logout))
	mux.HandleFunc("GET /web/libraries", c.private(func(w http.ResponseWriter, r *http.Request, who identity) {
		c.show(w, r, who, 200, "我的资料库", "libraries", nil, "")
	}))
	mux.HandleFunc("GET /web/libraries/{library}", c.private(c.libraryDetail))
	mux.HandleFunc("GET /web/libraries/{library}/file-history", c.private(c.fileHistory))
	mux.HandleFunc("GET /web/overview", c.admin(c.overview))
	mux.HandleFunc("GET /web/accounts", c.admin(c.accounts))
	mux.HandleFunc("POST /web/accounts/review", c.admin(c.reviewReset))
	mux.HandleFunc("POST /web/accounts/create", c.admin(c.saveAccount))
	mux.HandleFunc("POST /web/accounts/reset", c.admin(c.saveAccount))
	for _, name := range []string{"assets/console.css", "assets/atlas.svg", "assets/console.js", "assets/libraries.js", "assets/files.js"} {
		mux.HandleFunc("GET /web/"+name, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, files, name)
		})
	}
	mux.HandleFunc("GET /web/", func(w http.ResponseWriter, r *http.Request) {
		c.render(w, r, 404, page{Title: "页面不存在", View: "missing"})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		if status := origin.RequestStatus(r); status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "无效表单", 400)
				return
			}
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func (c *console) render(w http.ResponseWriter, r *http.Request, status int, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	var body bytes.Buffer
	if err := c.shell.ExecuteTemplate(&body, "shell.html", p); err != nil {
		http.Error(w, "页面暂不可用", 500)
		return
	}
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}
