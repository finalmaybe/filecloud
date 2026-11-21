// Package webconsole serves the optional, same-origin browser interface.
package webconsole

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/storage"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	origin *url.URL
}
type page struct {
	Title, View, Message string
	SignedIn, Admin      bool
	Data                 any
}

// NewHandler builds the Web routes for one configured public origin.
// The server itself speaks HTTP; remote HTTPS terminates at a local reverse proxy.
func NewHandler(rawOrigin, listen string, config Config) (http.Handler, error) {
	origin, err := parseOrigin(rawOrigin, listen)
	if err != nil {
		return nil, err
	}
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
	mux.HandleFunc("GET /web/libraries", c.private(c.libraries))
	mux.HandleFunc("POST /web/libraries", c.private(c.createLibrary))
	mux.HandleFunc("GET /web/libraries/{library}", c.private(c.libraryDetail))
	mux.HandleFunc("GET /web/libraries/{library}/snapshot/{commit}", c.private(c.snapshot))
	mux.HandleFunc("GET /web/libraries/{library}/history", c.private(c.history))
	mux.HandleFunc("GET /web/libraries/{library}/file-history", c.private(c.fileHistory))
	mux.HandleFunc("GET /web/overview", c.admin(c.overview))
	mux.HandleFunc("GET /web/accounts", c.admin(c.accounts))
	mux.HandleFunc("POST /web/accounts/review", c.admin(c.reviewReset))
	mux.HandleFunc("POST /web/accounts/create", c.admin(c.saveAccount))
	mux.HandleFunc("POST /web/accounts/reset", c.admin(c.saveAccount))
	for _, name := range []string{"assets/console.css", "assets/atlas.svg", "assets/console.js"} {
		mux.HandleFunc("GET /web/"+name, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, files, name)
		})
	}
	mux.HandleFunc("GET /web/", func(w http.ResponseWriter, r *http.Request) {
		c.render(w, r, 404, page{Title: "页面不存在", View: "missing"})
	})

	// Browsers omit the default port even when it is explicit in the configured origin.
	port := "443"
	if origin.Scheme == "http" {
		port = "80"
	}
	alternate := origin.Host
	if origin.Port() == "" {
		alternate += ":" + port
	} else if origin.Port() == port {
		alternate = strings.TrimSuffix(origin.Host, ":"+port)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		if !strings.EqualFold(r.Host, origin.Host) && !strings.EqualFold(r.Host, alternate) {
			http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
			return
		}
		if r.Method == http.MethodPost {
			o := r.Header.Values("Origin")
			if len(o) != 1 || !(strings.EqualFold(o[0], origin.Scheme+"://"+origin.Host) || strings.EqualFold(o[0], origin.Scheme+"://"+alternate)) {
				http.Error(w, "请求来源不匹配", http.StatusForbidden)
				return
			}
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

func parseOrigin(raw, listen string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || strings.ContainsAny(raw, "?#") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("web-origin must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, errors.New("invalid web-origin port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("empty web-origin port")
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || !loopback(host) {
		return nil, errors.New("web console requires a loopback listen address")
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, errors.New("web-origin must use HTTPS except on loopback")
	}
	return u, nil
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}
