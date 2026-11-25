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

// BrowserOrigin is the configured public origin shared by the Web console and WebDAV.
type BrowserOrigin struct {
	scheme, host, alternate string
}

func NewBrowserOrigin(raw, listen string) (*BrowserOrigin, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || strings.ContainsAny(raw, "?#") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("use http:// or https:// followed by a host and optional port (e.g. http://127.0.0.1:8080), without credentials, path, query, or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, errors.New("invalid origin port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("empty origin port")
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || !loopback(host) {
		return nil, errors.New("the public origin requires a loopback listen address")
	}
	port := "443"
	if u.Scheme == "http" {
		if !loopback(u.Hostname()) {
			return nil, errors.New("origin must use HTTPS except on loopback")
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

func (o *BrowserOrigin) String() string { return o.scheme + "://" + o.host }

func (o *BrowserOrigin) Secure() bool { return o.scheme == "https" }

func (o *BrowserOrigin) MatchesHost(host string) bool {
	return strings.EqualFold(host, o.host) || strings.EqualFold(host, o.alternate)
}
func (o *BrowserOrigin) MatchesOrigin(origin string) bool {
	return strings.EqualFold(origin, o.scheme+"://"+o.host) || strings.EqualFold(origin, o.scheme+"://"+o.alternate)
}

// RequestStatus returns zero for an allowed request, or the HTTP rejection status.
func (o *BrowserOrigin) RequestStatus(r *http.Request) int {
	if !o.MatchesHost(r.Host) {
		return http.StatusMisdirectedRequest
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		v := r.Header.Values("Origin")
		if len(v) != 1 || !o.MatchesOrigin(v[0]) {
			return http.StatusForbidden
		}
	}
	return 0
}
