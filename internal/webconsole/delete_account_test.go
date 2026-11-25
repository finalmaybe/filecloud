package webconsole_test

import (
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestDeleteAccountIncludesOnlyConfirmedOwnersData(t *testing.T) {
	d := newDrive(t)
	victimCookie := d.cookie
	d.call(201, "PUT", "example", "old version")
	tag := d.call(200, "HEAD", "example", "").Header().Get("ETag")
	d.call(204, "DELETE", "example", "", "If-Match", tag)
	path := "/accounts/" + d.owner + "/delete"
	confirm := url.Values{"confirm": {"yes"}, "libraries": {"1"}}
	if got := d.req("POST", path, confirm).Code; got != 403 {
		t.Fatalf("non-admin deletion: %d", got)
	}

	adminPass := rand.Text()
	if err := d.sessions.SetAccountPassword(t.Context(), "operator", adminPass, "127.0.0.1:10000", true); err != nil {
		t.Fatal(err)
	}
	d.config.AdminUsers = []string{"operator"}
	d.mount()
	login := d.req("POST", "/login", url.Values{"username": {"operator"}, "password": {adminPass}})
	if login.Code != 303 {
		t.Fatal("admin login failed")
	}
	d.cookie = login.Result().Cookies()[0]
	admin, err := d.db.FindUserByUsername(t.Context(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	if d.libraryAPI("PUT", "/"+d.lib, `{"Name":"Keep"}`).Code != 201 {
		t.Fatal("create library with the same ID for another owner")
	}
	keep := &drive{fixture: d.fixture, handler: d.handler, lib: d.lib}
	keep.id, keep.secret = keep.issue()
	keep.call(201, "PUT", "example", "keep")
	if d.req("POST", "/accounts/"+admin.ID+"/delete", confirm).Code != 403 ||
		d.req("POST", path, url.Values{"libraries": {"1"}}).Code != 400 ||
		d.req("POST", path, url.Values{"confirm": {"yes"}, "libraries": {"0"}}).Code != 409 {
		t.Fatal("admin, unconfirmed or stale deletion was not rejected")
	}
	d.call(200, "OPTIONS", "", "")
	if w := d.req("POST", path, confirm); w.Code != 303 || w.Header().Get("Location") != "/accounts?done=delete" {
		t.Fatalf("delete account: %d", w.Code)
	}
	if _, err := d.db.FindUserByUsername(t.Context(), "observer"); !errors.Is(err, storage.ErrUserNotFound) {
		t.Fatal("target account still exists")
	}
	if _, err := d.db.GetLibrary(t.Context(), d.owner, d.lib); !errors.Is(err, storage.ErrLibraryNotFound) {
		t.Fatal("target library still exists")
	}
	if _, err := os.Stat(filepath.Join(d.db.ObjectsDir(), d.owner)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("target object directory was not removed")
	}
	d.call(401, "OPTIONS", "", "")
	if keep.call(200, "GET", "example", "").Body.String() != "keep" {
		t.Fatal("another owner's library was affected")
	}
	d.cookie = victimCookie
	if d.req("GET", "/libraries", nil).Code != 303 {
		t.Fatal("deleted user's browser session still works")
	}
}
