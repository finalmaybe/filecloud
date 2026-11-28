package webconsole_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/dav"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestGlobalWebDAVSetting(t *testing.T) {
	f := newFixture(t)
	if w := f.req("GET", "/settings", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatal("settings must require a browser session")
	}
	f.login()
	d := &drive{fixture: f, handler: dav.Handler{Origin: browserOrigin(t, origin), Store: f.db, Catalog: f.config.Catalog}, lib: "ce9a3a1f-0d30-4e5d-bdaa-db53a8207c42"}
	if f.libraryAPI("PUT", "/"+d.lib, `{"Name":"Settings"}`).Code != 201 {
		t.Fatal("create library")
	}
	set := func(value string) int {
		return f.req("POST", "/settings/webdav", url.Values{"enabled": {value}}).Code
	}
	if w := f.req("GET", "/settings", nil); w.Code != 403 {
		t.Fatal("ordinary user could open admin settings")
	}
	if set("true") != 403 {
		t.Fatal("ordinary user changed a global setting")
	}
	if w := d.call(404, "OPTIONS", "", ""); w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("disabled endpoint issued a challenge")
	}
	f.config.AdminUsers = []string{"observer"}
	f.mount()
	if w := f.req("GET", "/settings", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `aria-checked="false"`) {
		t.Fatal("admin should see WebDAV off by default")
	}
	if set("invalid") != 400 || set("true") != 303 {
		t.Fatal("expected an explicit administrator choice")
	}
	d.id, d.secret = d.issue()
	d.call(200, "OPTIONS", "", "")
	if set("false") != 303 {
		t.Fatal("could not disable WebDAV")
	}
	d.call(404, "OPTIONS", "", "")
	if w := f.req("POST", d.page(), url.Values{"name": {"device"}}); w.Code != 409 {
		t.Fatal("issued a password while WebDAV was off")
	}
	if set("true") != 303 {
		t.Fatal("could not re-enable WebDAV")
	}
	d.call(200, "OPTIONS", "", "")
}

func TestGlobalHistoryConcurrencySetting(t *testing.T) {
	f := newFixture(t)
	const endpoint = "/settings/history-concurrency"
	valid := url.Values{"global": {"60"}, "user": {"25"}}
	if w := f.req("POST", endpoint, valid); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous save status = %d, want 401", w.Code)
	}
	f.login()
	if w := f.req("POST", endpoint, valid); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin save status = %d, want 403", w.Code)
	}
	f.config.AdminUsers = []string{"observer"}
	f.mount()

	assertLimits := func(want storage.HistoryConcurrency) {
		t.Helper()
		if got := f.config.Catalog.HistoryConcurrency(); got != want {
			t.Fatalf("live limits = %+v, want %+v", got, want)
		}
		if got, err := f.db.HistoryConcurrency(t.Context()); err != nil || got != want {
			t.Fatalf("saved limits = %+v, %v; want %+v", got, err, want)
		}
	}
	assertPage := func(path string, markers ...string) {
		t.Helper()
		w := f.req("GET", path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("settings status = %d, want 200", w.Code)
		}
		for _, marker := range markers {
			if !strings.Contains(w.Body.String(), marker) {
				t.Errorf("settings page does not contain %q", marker)
			}
		}
	}
	assertPage("/settings", `action="`+endpoint+`"`, `required value="50"`, `required value="20"`)
	for _, source := range []string{"", "null", "https://other.example"} {
		r := httptest.NewRequest("POST", origin+endpoint, strings.NewReader(valid.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if source != "" {
			r.Header.Set("Origin", source)
		}
		r.AddCookie(f.cookie)
		w := httptest.NewRecorder()
		f.web.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("admin POST with Origin %q returned %d, want 403", source, w.Code)
		}
	}
	for _, form := range []url.Values{
		{"global": {"60"}},
		{"global": {"60", "60"}, "user": {"25"}},
		{"global": {"nonnumeric"}, "user": {"25"}},
		{"global": {"1.5"}, "user": {"25"}},
		{"global": {"0"}, "user": {"25"}},
		{"global": {"60"}, "user": {"-1"}},
		{"global": {"60"}, "user": {"1001"}},
		{"global": {"1000000000000000000000000"}, "user": {"25"}},
	} {
		if w := f.req("POST", endpoint, form); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `role="alert">并发上限须为`) {
			t.Errorf("invalid form %v: status = %d; want 400 with validation message", form, w.Code)
		}
	}
	assertLimits(storage.HistoryConcurrency{Global: 50, User: 20})
	w := f.req("POST", endpoint, valid)
	location := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || location != "/settings?saved=1" {
		t.Fatalf("save status = %d, redirect = %q; want 303 to saved settings", w.Code, location)
	}
	assertLimits(storage.HistoryConcurrency{Global: 60, User: 25})
	assertPage(location, `required value="60"`, `required value="25"`, `role="status">并发设置已保存。`)
}
