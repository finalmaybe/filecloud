package webconsole_test

import (
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestDAVCredentialBoundaries(t *testing.T) {
	d := newDrive(t)
	ctx := t.Context()
	otherLib, _ := auth.NewUserID(nil)
	if d.libraryAPI("PUT", "/"+otherLib, `{"Name":"Other"}`).Code != 201 {
		t.Fatal("library setup")
	}
	if d.req("POST", "/libraries/"+otherLib+"/app-passwords/"+d.id+"/revoke", url.Values{}).Code != 404 {
		t.Fatal("cross-library revocation")
	}
	otherOwner, _ := auth.NewUserID(nil)
	if err := d.db.CreateUser(ctx, storage.User{ID: otherOwner, Username: "other-test-owner", PasswordHash: "unused"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.db.CreateLibrary(ctx, storage.Library{ID: d.lib, OwnerUserID: otherOwner, Name: "private"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	otherID, _ := auth.NewUserID(nil)
	otherPW, err := d.db.CreateDAVCredential(ctx, otherOwner, d.lib, otherID, "pc", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.call(201, "PUT", "visible", "first")
	d.id, d.secret = otherID, otherPW
	d.call(404, "GET", "visible", "")
	d.call(201, "PUT", "visible", "second")
	d.lib = otherLib
	d.call(404, "GET", "visible", "")
	r := httptest.NewRequest("POST", origin+d.page(), strings.NewReader("name=x"))
	r.AddCookie(d.cookie)
	r.Header.Set("Origin", "https://elsewhere.invalid")
	w := httptest.NewRecorder()
	d.web.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin management accepted")
	}
}
func TestDAVCollectionConditions(t *testing.T) {
	d := newDrive(t)
	d.call(201, "MKCOL", "docs", "")
	d.call(201, "PUT", "docs/a", "old")
	tag := d.call(200, "HEAD", "docs/a", "").Header().Get("ETag")
	lock := d.call(200, "LOCK", "docs", lockInfo, "Depth", "0").Header().Get("Lock-Token")
	d.call(204, "PUT", "docs/a", "new", "If-Match", tag)
	d.call(423, "PUT", "docs/b", "new")
	dir := origin + "/dav/" + d.lib + "/docs"
	d.call(201, "PUT", "docs/b", "new", "If", "<"+dir+"> ("+lock+")")
	d.call(204, "UNLOCK", "docs", "", "Lock-Token", lock)
	lock = d.call(200, "LOCK", "docs", lockInfo, "Depth", "infinity").Header().Get("Lock-Token")
	d.call(423, "LOCK", "docs/a", lockInfo)
	d.call(412, "PUT", "docs/a", "unknown", "If", "(Not "+lock+")")
	d.call(204, "PUT", "docs/a", "last", "If", "([\"obsolete\"]) ("+lock+")")
	d.call(201, "COPY", "docs", "", "Destination", "/dav/"+d.lib+"/shallow", "Depth", "0")
	d.call(404, "HEAD", "shallow/a", "")
	d.call(204, "DELETE", "docs", "", "If", "("+lock+")")
	d.call(409, "UNLOCK", "docs", "", "Lock-Token", lock)
	d.call(201, "PUT", "Case", "payload")
	d.call(201, "MOVE", "Case", "", "Destination", "/dav/"+d.lib+"/case")
	d.call(200, "HEAD", "case", "")
	d.call(404, "HEAD", "Case", "")
	d.call(201, "PUT", "other", "other")
	d.call(428, "MOVE", "other", "", "Destination", "/dav/"+d.lib+"/case", "If-Match", d.call(200, "HEAD", "other", "").Header().Get("ETag"))
	d.call(200, "HEAD", "other", "")
}

type duringRead struct {
	run func()
	io.Reader
}

func (b *duringRead) Read(p []byte) (int, error) {
	if b.run != nil {
		b.run()
		b.run = nil
	}
	return b.Reader.Read(p)
}
func TestDAVChangesDuringUpload(t *testing.T) {
	d := newDrive(t)
	upload := func(name string, update func(), want int) {
		t.Helper()
		r := httptest.NewRequest("PUT", origin+"/dav/"+d.lib+"/"+name, nil)
		r.SetBasicAuth(d.id, d.secret)
		r.ContentLength = -1
		r.Body = io.NopCloser(&duringRead{update, strings.NewReader("draft")})
		w := httptest.NewRecorder()
		d.handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("concurrent upload = %d, want %d", w.Code, want)
		}
	}
	upload("a", func() { d.call(201, "PUT", "b", "sibling") }, 201)
	upload("c", func() { d.call(201, "PUT", "c", "winner") }, 409)
	if result := d.call(200, "GET", "c", "").Body.String(); result != "winner" {
		t.Fatal("concurrent creation overwritten")
	}
	upload("d", func() {
		if err := d.db.RevokeDAV(t.Context(), d.owner, d.lib, d.id); err != nil {
			t.Fatal(err)
		}
	}, 401)
	d.id, d.secret = d.issue()
	d.call(404, "GET", "d", "")
}
