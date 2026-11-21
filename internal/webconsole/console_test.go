package webconsole_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/webconsole"
)

func TestOriginConfiguration(t *testing.T) {
	for _, tc := range []struct {
		origin string
		listen string
		valid  bool
	}{
		{"https://console.example", "127.0.0.1:8080", true},
		{"https://console.example:8443", "localhost:8080", true},
		{"http://127.0.0.1:8080", "127.0.0.1:8080", true},
		{"http://[::1]:8080", "[::1]:8080", true},
		{"http://localhost:8080", "localhost:8080", true},
		{"http://console.example", "127.0.0.1:8080", false},
		{"http://localhost", ":8080", false},
		{"https://console.example", "0.0.0.0:8080", false},
		{"//console.example", "localhost:8080", false},
		{"https://", "localhost:8080", false},
		{"ftp://console.example", "localhost:8080", false},
		{"https://user@console.example", "localhost:8080", false},
		{"https://console.example/", "localhost:8080", false},
		{"https://console.example/web", "localhost:8080", false},
		{"https://console.example?", "localhost:8080", false},
		{"https://console.example/#part", "localhost:8080", false},
		{"https://console.example:65536", "localhost:8080", false},
	} {
		_, err := webconsole.NewHandler(tc.origin, tc.listen, webconsole.Config{})
		if (err == nil) != tc.valid {
			t.Errorf("NewHandler(%q, %q) error = %v, want valid=%v", tc.origin, tc.listen, err, tc.valid)
		}
	}
}

func TestWebHostBinding(t *testing.T) {
	for _, tc := range []struct {
		origin string
		host   string
		status int
	}{
		{"https://console.example:443", "CONSOLE.EXAMPLE", 200},
		{"https://console.example", "console.example:443", 200},
		{"http://[::1]:80", "[::1]", 200},
		{"https://console.example:8443", "console.example:8443", 200},
		{"https://console.example:8443", "console.example", 421},
		{"https://console.example", "other.example", 421},
	} {
		h, err := webconsole.NewHandler(tc.origin, "127.0.0.1:8080", webconsole.Config{})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/web/login", nil)
		r.Host = tc.host
		r.Header.Set("X-Forwarded-Host", "console.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("origin %q, host %q: status = %d, want %d", tc.origin, tc.host, w.Code, tc.status)
		}
	}
}

func TestEmbeddedRoutes(t *testing.T) {
	h, err := webconsole.NewHandler("https://console.example", "127.0.0.1:8080", webconsole.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		location     string
		media, text  string
	}{
		{"GET", "/web", 301, "/web/", "", ""},
		{"GET", "/web/", 200, "", "text/html", `href="#content"`},
		{"HEAD", "/web/", 200, "", "text/html", ""},
		{"GET", "/web/login", 200, "", "text/html", `autocomplete="current-password"`},
		{"GET", "/web/libraries", 303, "/web/login", "", ""},
		{"GET", "/web/libraries/example/history", 303, "/web/login", "", ""},
		{"GET", "/web/overview", 303, "/web/login", "", ""},
		{"GET", "/web/accounts", 303, "/web/login", "", ""},
		{"GET", "/web/not-a-page", 404, "", "text/html", "页面不存在"},
		{"GET", "/web/assets/", 404, "", "text/html", "页面不存在"},
		{"GET", "/web/shell.html", 404, "", "text/html", "页面不存在"},
		{"GET", "/web/assets/console.css", 200, "", "text/css", ".skip"},
		{"GET", "/web/assets/atlas.svg", 200, "", "image/svg+xml", "<svg"},
		{"POST", "/web/login", 403, "", "", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://console.example"+tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Location") != tc.location || !strings.HasPrefix(w.Header().Get("Content-Type"), tc.media) || !strings.Contains(w.Body.String(), tc.text) {
				t.Fatalf("status=%d, location=%q, type=%q, expected marker present=%t", w.Code, w.Header().Get("Location"), w.Header().Get("Content-Type"), strings.Contains(w.Body.String(), tc.text))
			}
			if tc.method == "HEAD" && w.Body.Len() != 0 {
				t.Error("HEAD returned a body")
			}
			csp := w.Header().Get("Content-Security-Policy")
			if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "style-src 'self'") || strings.Contains(csp, "unsafe-inline") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing Web content policy")
			}
		})
	}
}
