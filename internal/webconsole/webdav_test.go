package webconsole_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/dav"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/object"
)

type drive struct {
	*fixture
	handler                dav.Handler
	lib, owner, id, secret string
}

func newDrive(t *testing.T) *drive {
	t.Helper()
	f := newFixture(t)
	f.login()
	lib, _ := auth.NewUserID(nil)
	if f.libraryAPI("PUT", "/"+lib, `{"Name":"Office"}`).Code != 201 {
		t.Fatal("create library")
	}
	u, err := f.db.FindUserByUsername(t.Context(), "observer")
	if err != nil {
		t.Fatal(err)
	}
	d := &drive{fixture: f, handler: dav.Handler{Origin: browserOrigin(t, origin), Store: f.db, Catalog: f.config.Catalog}, lib: lib, owner: u.ID}
	d.id, d.secret = d.issue()
	return d
}
func (d *drive) page() string { return "/libraries/" + d.lib + "/app-passwords" }
func (d *drive) issue() (string, string) {
	d.t.Helper()
	w := d.req("POST", d.page(), url.Values{"name": {"Laptop"}})
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		d.t.Fatalf("issue: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `<code id="issued-address">`+origin+"/dav/"+d.lib+"/</code>") {
		d.t.Fatal("mount address does not use the configured public origin")
	}
	m := regexp.MustCompile(`(?s)<code id="issued-username">([^<]+)</code>.*?<code id="issued-password">([^<]+)</code>`).FindStringSubmatch(w.Body.String())
	if m == nil {
		d.t.Fatal("missing setup instructions")
	}
	password := m[2]
	if !regexp.MustCompile(`^[A-Za-z0-9]{26}$`).MatchString(password) ||
		!strings.ContainsAny(password, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
		!strings.ContainsAny(password, "abcdefghijklmnopqrstuvwxyz") ||
		!strings.ContainsAny(password, "0123456789") {
		d.t.Fatal("issued password must contain 26 alphanumeric characters with all three character classes")
	}
	return m[1], password
}
func (d *drive) call(code int, method, file, body string, headers ...string) *httptest.ResponseRecorder {
	d.t.Helper()
	r := httptest.NewRequest(method, origin+"/dav/"+d.lib+"/"+file, strings.NewReader(body))
	r.SetBasicAuth(d.id, d.secret)
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	d.handler.ServeHTTP(w, r)
	if w.Code != code {
		d.t.Fatalf("%s: got %d, want %d", method, w.Code, code)
	}
	return w
}

const lockInfo = `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`

func TestDAVOfficeSave(t *testing.T) {
	d := newDrive(t)
	d.call(201, "MKCOL", "office/", "")
	options := d.call(200, "OPTIONS", "", "")
	if !strings.Contains(options.Header().Get("DAV"), "2") || !strings.Contains(options.Header().Get("Allow"), "LOCK") {
		t.Fatal("missing lock capability")
	}
	d.call(201, "PUT", "office/book.xlsx", "v1")
	first, err := d.db.GetLibrary(t.Context(), d.owner, d.lib)
	if err != nil {
		t.Fatal(err)
	}
	v1 := d.call(200, "HEAD", "office/book.xlsx", "").Header().Get("ETag")
	d.call(428, "PUT", "office/book.xlsx", "blind")
	d.call(412, "PUT", "office/book.xlsx", "stale", "If-Match", `"stale"`)
	d.call(204, "PUT", "office/book.xlsx", "v2", "If-Match", v1)
	l := d.call(200, "LOCK", "office/book.xlsx", lockInfo, "Depth", "0").Header().Get("Lock-Token")
	if l == "" {
		t.Fatal("no lock token")
	}
	d.call(423, "DELETE", "office/book.xlsx", "")
	d.call(200, "LOCK", "office/book.xlsx", "", "If", "("+l+")")
	d.call(201, "PUT", "office/temporary", "final")
	dest := origin + "/dav/" + d.lib + "/office/book.xlsx"
	d.call(412, "MOVE", "office/temporary", "", "Destination", dest, "Overwrite", "F")
	d.call(423, "MOVE", "office/temporary", "", "Destination", dest)
	d.call(204, "MOVE", "office/temporary", "", "Destination", dest, "If", "<"+dest+"> ("+l+")")
	d.call(404, "GET", "office/temporary", "")
	if w := d.call(206, "GET", "office/book.xlsx", "", "Range", "bytes=1-3"); w.Body.String() != "ina" {
		t.Fatal("wrong saved contents")
	}
	version, err := d.config.Catalog.OpenFileVersion(t.Context(), d.owner, library.VersionTarget{LibraryID: d.lib, Path: "office/book.xlsx", CommitID: *first.HeadCommitID})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := io.ReadAll(version.Content(t.Context()))
	if err != nil || string(prev) != "v1" {
		t.Fatal("old version was not preserved")
	}
	d.call(204, "UNLOCK", "office/book.xlsx", "", "Lock-Token", l)
	tag := d.call(200, "HEAD", "office/book.xlsx", "").Header().Get("ETag")
	d.call(304, "GET", "office/book.xlsx", "", "If-None-Match", tag)
	d.call(201, "COPY", "office/book.xlsx", "", "Destination", "/dav/"+d.lib+"/copy")
	d.call(204, "DELETE", "office/book.xlsx", "", "If-Match", tag)
	w := d.req("GET", "/libraries/"+d.lib+"/trash", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "book.xlsx") {
		t.Fatal("deleted file missing in trash")
	}
}

func TestDAVCredentialsAndLockLifecycle(t *testing.T) {
	d := newDrive(t)
	first, secret := d.id, d.secret
	second, other := d.issue()
	list := d.req("GET", d.page(), nil).Body.String()
	if strings.Contains(list, secret) || strings.Contains(list, other) {
		t.Fatal("credential list contains a secret")
	}
	for _, control := range []string{
		`class="entry-table credential-list"`, `commandfor="create-credential"`, `commandfor="view-` + first + `"`,
		`commandfor="delete-` + first + `"`, `<time datetime=`,
		`data-copy="username-` + first + `" aria-label="复制用户名" title="复制用户名"><svg`,
		`data-copy="address-` + first + `" aria-label="复制地址" title="复制地址"><svg`,
	} {
		if !strings.Contains(list, control) {
			t.Fatal("missing credential list control:", control)
		}
	}
	d.call(201, "LOCK", "new", lockInfo)
	d.call(200, "GET", "new", "")
	d.id, d.secret = second, other
	d.call(423, "PUT", "new", "other")
	w := d.call(207, "PROPFIND", "new", "", "Depth", "0")
	if strings.Contains(w.Body.String(), "urn:uuid:") {
		t.Fatal("foreign lock token exposed")
	}
	if d.req("POST", d.page()+"/"+first+"/revoke", url.Values{}).Code != 303 {
		t.Fatal("revoke failed")
	}
	list = d.req("GET", d.page(), nil).Body.String()
	if strings.Contains(list, first) || !strings.Contains(list, second) {
		t.Fatal("deleted credential must disappear without removing its sibling")
	}
	d.call(200, "LOCK", "new", lockInfo)
	d.id, d.secret = first, secret
	d.call(401, "GET", "new", "")
	d.id, d.secret = second, other
	d.call(200, "GET", "new", "")
	if err := d.db.ResetPassword(t.Context(), "observer", "unused", time.Now()); err != nil {
		t.Fatal(err)
	}
	d.call(401, "GET", "new", "")
	locks, err := d.db.DAVLocks(t.Context(), d.owner, d.lib, time.Now())
	if err != nil || len(locks) != 0 {
		t.Fatal("password reset did not clear locks")
	}
}

func TestDAVListsCompleteDirectoryAndPreservesInterruptedFile(t *testing.T) {
	d := newDrive(t)
	for i := range 51 {
		d.call(201, "PUT", fmt.Sprintf("doc-%02d", i), "")
	}
	w := d.call(207, "PROPFIND", "", `<propfind xmlns="DAV:"><prop><displayname/><unknown/></prop></propfind>`, "Depth", "1")
	var multi struct {
		Rows []struct {
			Href string `xml:"DAV: href"`
		} `xml:"DAV: response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &multi); err != nil || len(multi.Rows) != 52 || !strings.Contains(w.Body.String(), "404 Not Found") {
		t.Fatal("incomplete property response")
	}
	d.call(403, "PROPFIND", "", "", "Depth", "infinity")
	d.call(400, "PUT", "a%2Fb", "x")
	content := strings.Repeat("x", object.MaxBlockSize) + "end"
	d.call(201, "PUT", "large", content)
	if got := d.call(206, "GET", "large", "", "Range", fmt.Sprintf("bytes=%d-", object.MaxBlockSize-1)).Body.String(); got != "xend" {
		t.Fatal("cross-block range")
	}
	r := httptest.NewRequest("PUT", origin+"/dav/"+d.lib+"/incomplete", nil)
	r.Body = io.NopCloser(io.MultiReader(strings.NewReader("partial"), brokenBody{}))
	r.SetBasicAuth(d.id, d.secret)
	w = httptest.NewRecorder()
	d.handler.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatal("interrupted PUT succeeded")
	}
	d.call(404, "HEAD", "incomplete", "")
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDAVLockEnforcedByNativeAndWebPublication(t *testing.T) {
	d := newDrive(t)
	ctx := t.Context()
	d.call(201, "PUT", "doc", "one")
	lib, err := d.db.GetLibrary(ctx, d.owner, d.lib)
	if err != nil {
		t.Fatal(err)
	}
	root, err := d.config.Catalog.Snapshot(ctx, d.owner, d.lib, "", ".", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.call(201, "PUT", "sibling", "two")
	d.call(200, "LOCK", "doc", lockInfo)
	device, _ := auth.NewUserID(nil)
	var raw [32]byte
	rand.Read(raw[:])
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	if err = d.db.CreateSession(ctx, d.owner, hash, "native", d.now, d.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	publish := func(rootID string, code int) {
		t.Helper()
		lib, err = d.db.GetLibrary(ctx, d.owner, d.lib)
		if err != nil {
			t.Fatal(err)
		}
		commit := object.Commit{Root: rootID, Parents: []string{*lib.HeadCommitID}, AuthorUserID: d.owner, DeviceID: device, CreatedAt: d.now.Format(time.RFC3339), Message: "sync"}
		data := commit.Bytes()
		id := object.ID(data)
		if _, err = d.db.PutObject(ctx, d.owner, d.lib, "commits", id, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("PUT", "/v1/libraries/"+d.lib+"/head", strings.NewReader(`{"CommitId":"`+id+`"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("If-Match", fmt.Sprintf(`"head-version-%d"`, lib.HeadVersion))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.config.Catalog.ServeHTTP(w, r)
		if w.Code != code || code == 503 && !strings.Contains(w.Body.String(), "DAVLocked") {
			t.Fatalf("native publication = %d, want %d", w.Code, code)
		}
	}
	publish(root.ID, 200)
	empty := object.Directory{}.Bytes()
	if _, err = d.db.PutObject(ctx, d.owner, d.lib, "directories", object.ID(empty), bytes.NewReader(empty)); err != nil {
		t.Fatal(err)
	}
	publish(object.ID(empty), 503)
	w := d.req("POST", "/libraries/"+d.lib+"/trash/move", url.Values{"commit": {*lib.HeadCommitID}, "path": {"doc"}})
	if w.Code != 423 {
		t.Fatalf("web deletion = %d", w.Code)
	}
}
