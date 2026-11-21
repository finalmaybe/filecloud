package auth

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const BrowserCookieName = "filecloud_web"

// BrowserOrigin is the configured public origin for requests using a Web session.
type BrowserOrigin struct {
	scheme, host, alternate string
}

func NewBrowserOrigin(raw, listen string) (*BrowserOrigin, error) {
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
	port := "443"
	if u.Scheme == "http" {
		if !loopback(u.Hostname()) {
			return nil, errors.New("web-origin must use HTTPS except on loopback")
		}
		port = "80"
	}
	// Browsers omit the default port even when it is explicit in the configured origin.
	alternate := u.Host
	if u.Port() == "" {
		alternate += ":" + port
	} else if u.Port() == port {
		alternate = strings.TrimSuffix(u.Host, ":"+port)
	}
	return &BrowserOrigin{u.Scheme, u.Host, alternate}, nil
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func (o *BrowserOrigin) Secure() bool { return o.scheme == "https" }

// RequestStatus returns zero for an allowed request, or the HTTP rejection status.
func (o *BrowserOrigin) RequestStatus(r *http.Request) int {
	if !strings.EqualFold(r.Host, o.host) && !strings.EqualFold(r.Host, o.alternate) {
		return http.StatusMisdirectedRequest
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		v := r.Header.Values("Origin")
		if len(v) != 1 || !(strings.EqualFold(v[0], o.scheme+"://"+o.host) || strings.EqualFold(v[0], o.scheme+"://"+o.alternate)) {
			return http.StatusForbidden
		}
	}
	return 0
}
