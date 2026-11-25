package webconsole_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/dav"
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
