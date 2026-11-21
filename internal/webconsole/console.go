// Package webconsole serves the optional, same-origin browser interface.
package webconsole

import (
	"embed"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

//go:embed shell.html assets
var files embed.FS

// NewHandler builds the Web routes for one configured public origin.
// The server itself speaks HTTP; remote HTTPS terminates at a local reverse proxy.
func NewHandler(rawOrigin, listen string) (http.Handler, error) {
	origin, err := parseOrigin(rawOrigin, listen)
	if err != nil {
		return nil, err
	}
	shell, err := template.ParseFS(files, "shell.html")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /web", http.RedirectHandler("/web/", http.StatusMovedPermanently))
	mux.Handle("GET /web/{$}", shellPage(shell, http.StatusOK, "控制台入口", "home"))
	mux.Handle("GET /web/login", shellPage(shell, http.StatusOK, "登录与访问", "login"))
	for _, path := range []string{"libraries", "libraries/{library}", "libraries/{library}/history", "overview", "accounts"} {
		mux.Handle("GET /web/"+path, http.RedirectHandler("/web/login", http.StatusSeeOther))
	}
	for _, name := range []string{"assets/console.css", "assets/atlas.svg"} {
		mux.HandleFunc("GET /web/"+name, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, files, name)
		})
	}
	mux.Handle("GET /web/", shellPage(shell, http.StatusNotFound, "页面不存在", "missing"))

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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !strings.EqualFold(r.Host, origin.Host) && !strings.EqualFold(r.Host, alternate) {
			http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func shellPage(shell *template.Template, status int, title, view string) http.Handler {
	page := struct{ Title, View string }{title, view}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_ = shell.Execute(w, page)
		}
	})
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
