package webconsole_test

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/storage"
	"github.com/mingming-cn/filecloud/internal/webconsole"
)

const origin = "https://console.example"

type fixture struct {
	t        *testing.T
	db       *storage.Store
	sessions *auth.Sessions
	config   webconsole.Config
	web      http.Handler
	now      time.Time
	pass     string
	cookie   *http.Cookie
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := filepath.Join(t.TempDir(), "data")
	if err := storage.Init(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenForServe(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{t: t, db: db, now: time.Now().UTC(), pass: rand.Text()}
	params := auth.Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}
	hash, err := auth.HashPassword([]byte(f.pass), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := auth.NewUserID(nil)
	if err = db.CreateUser(t.Context(), storage.User{ID: id, Username: "observer", PasswordHash: hash}, f.now); err != nil {
		t.Fatal(err)
	}
	f.sessions, err = auth.NewHandler(db, log.New(io.Discard, "", 0), auth.HandlerConfig{Now: func() time.Time { return f.now }, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := library.NewHandler(db, nil, library.Config{BrowserOrigin: browserOrigin(t, origin), Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	f.config = webconsole.Config{Store: db, Sessions: f.sessions, Catalog: catalog}
	f.mount()
	return f
}
func (f *fixture) mount() {
	f.t.Helper()
	var err error
	f.web, err = webconsole.NewHandler(browserOrigin(f.t, origin), f.config)
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) req(method, path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, origin+path, strings.NewReader(form.Encode()))
	if form != nil {
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if f.cookie != nil {
		r.AddCookie(f.cookie)
	}
	w := httptest.NewRecorder()
	f.web.ServeHTTP(w, r)
	return w
}
func (f *fixture) libraryAPI(method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, origin+"/v1/libraries"+path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(f.cookie)
	w := httptest.NewRecorder()
	f.config.Catalog.ServeHTTP(w, r)
	return w
}

func (f *fixture) login() {
	f.t.Helper()
	w := f.req("POST", "/web/login", url.Values{"username": {"observer"}, "password": {f.pass}})
	if w.Code != 303 {
		f.t.Fatalf("login status %d", w.Code)
	}
	f.cookie = w.Result().Cookies()[0]
}
func TestOnlineAccountsAndOwnerIsolation(t *testing.T) {
	f := newFixture(t)
	f.login()
	admin := f.cookie
	target := url.Values{"username": {"field-device"}, "password": {rand.Text()}}
	if f.req("POST", "/web/accounts/create", target).Code != 403 {
		t.Fatal("owner could create accounts")
	}
	f.config.AdminUsers = []string{"observer"}
	f.mount()
	if f.req("POST", "/web/accounts/create", target).Code != 303 || f.req("POST", "/web/accounts/create", target).Code != 409 {
		t.Fatal("create or duplicate")
	}
	id, _ := auth.NewUserID(nil)
	const lib = `{"Name":"Atlas"}`
	if f.libraryAPI("PUT", "/"+id, lib).Code != 201 || f.libraryAPI("PUT", "/"+id, lib).Code != 200 {
		t.Fatal("idempotent create")
	}
	id2, _ := auth.NewUserID(nil)
	duplicate := f.libraryAPI("PUT", "/"+id2, lib)
	if duplicate.Code != 409 || !strings.Contains(duplicate.Body.String(), `"RetCode":3000`) {
		t.Fatal("duplicate library name did not return AlreadyExists")
	}
	if f.libraryAPI("GET", "?PageToken=invalid", "").Code != 400 {
		t.Fatal("cursor error")
	}
	w := f.req("POST", "/web/login", target)
	if w.Code != 303 {
		t.Fatal("new login")
	}
	other := w.Result().Cookies()[0]
	f.cookie = other
	if f.req("GET", "/web/libraries/"+id, nil).Code != 404 {
		t.Fatal("foreign library visible")
	}
	if list := f.libraryAPI("GET", "", ""); list.Code != 200 || strings.Contains(list.Body.String(), id) {
		t.Fatal("expected an empty list without foreign library metadata")
	}
	if f.libraryAPI("GET", "/"+id, "").Code != 404 {
		t.Fatal("foreign library visible via API")
	}
	if f.libraryAPI("PUT", "/"+id, `{"Name":"Other"}`).Code != 201 {
		t.Fatal("same UUID in separate owner scope")
	}
	f.cookie = admin
	detail := f.req("GET", "/web/libraries/"+id, nil)
	if detail.Code != 200 || strings.Contains(detail.Body.String(), "Other") || !strings.Contains(detail.Body.String(), "尚未发布文件") {
		t.Fatal("expected the owner's empty file list")
	}
	info := f.libraryAPI("GET", "", "")
	if info.Code != 200 || !strings.Contains(info.Body.String(), "Atlas") || strings.Contains(info.Body.String(), "Other") || !strings.Contains(info.Body.String(), `"HeadCommitId":null`) {
		t.Fatal("expected only the owner's unpublished library metadata")
	}
	if f.req("POST", "/web/accounts/review", url.Values{"username": {"field-device"}}).Code != 200 {
		t.Fatal("reset review")
	}
	reset := url.Values{"username": {"field-device"}, "password": {rand.Text()}}
	if f.req("POST", "/web/accounts/reset", reset).Code != 400 {
		t.Fatal("missing confirmation")
	}
	reset.Set("confirm", "yes")
	if f.req("POST", "/web/accounts/reset", reset).Code != 303 {
		t.Fatal("reset failed")
	}
	f.cookie = other
	if f.req("GET", "/web/libraries", nil).Code != 303 || f.req("POST", "/web/login", target).Code != 401 || f.req("POST", "/web/login", reset).Code != 303 {
		t.Fatal("reset login semantics")
	}
}
func TestAdminGrantAndRevocation(t *testing.T) {
	f := newFixture(t)
	f.login()
	if f.req("GET", "/web/accounts", nil).Code != 403 {
		t.Fatal("implicit administrator")
	}
	f.config.AdminUsers = []string{"OBSERVER"}
	f.mount()
	if f.req("GET", "/web/accounts", nil).Code != 200 {
		t.Fatal("authorized admin rejected")
	}
	f.config.AdminUsers = nil
	f.mount()
	if f.req("GET", "/web/accounts", nil).Code != 403 || f.req("GET", "/web/libraries", nil).Code != 200 {
		t.Fatal("existing session did not lose only admin rights")
	}
	f.config.AdminUsers = []string{"missing"}
	if _, err := webconsole.NewHandler(browserOrigin(t, origin), f.config); err == nil {
		t.Fatal("unknown admin configured")
	}
}
func TestWorkspaceNavigationBySession(t *testing.T) {
	f := newFixture(t)
	login := f.req("GET", "/web/login", nil).Body.String()
	if !strings.Contains(login, `autocomplete="current-password"`) || strings.Contains(login, `<nav`) || strings.Contains(login, "工作空间") {
		t.Fatal("expected a standalone login form without workspace navigation")
	}
	f.login()
	entry := f.req("GET", "/web/", nil)
	if entry.Code != 303 || entry.Header().Get("Location") != "/web/libraries" {
		t.Fatal("signed-in entry did not lead to libraries")
	}
	libraries := f.req("GET", "/web/libraries", nil).Body.String()
	if !strings.Contains(libraries, `aria-label="主导航"`) || !strings.Contains(libraries, `>资料库</a>`) || strings.Contains(libraries, "控制台入口") {
		t.Fatal("expected libraries navigation without the redundant console entry")
	}
}

func TestSessionLifecycle(t *testing.T) {
	f := newFixture(t)
	bad := f.req("POST", "/web/login", url.Values{"username": {"observer"}, "password": {"invalid"}})
	if bad.Code != 401 || bad.Header().Get("Set-Cookie") != "" {
		t.Fatal("invalid login accepted")
	}
	f.login()
	if !f.cookie.Secure || !f.cookie.HttpOnly || f.cookie.Path != "/" || f.cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie policy")
	}
	if f.req("GET", "/web/libraries", nil).Code != 200 {
		t.Fatal("valid cookie rejected")
	}
	if f.req("POST", "/web/logout", nil).Code != 403 {
		t.Fatal("missing origin accepted")
	}
	if f.req("POST", "/web/logout", url.Values{}).Code != 303 || f.req("GET", "/web/libraries", nil).Code != 303 {
		t.Fatal("logout failed to revoke cookie")
	}
	f.login()
	f.now = f.now.Add(31 * 24 * time.Hour)
	if f.req("GET", "/web/libraries", nil).Code != 303 {
		t.Fatal("expired cookie accepted")
	}
}
func TestSessionNamespacesAndPasswordReset(t *testing.T) {
	f := newFixture(t)
	f.login()
	payload, _ := json.Marshal(map[string]string{"Username": "observer", "Password": f.pass, "DeviceName": "cli"})
	r := httptest.NewRequest("POST", "/v1/sessions", strings.NewReader(string(payload)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.sessions.ServeHTTP(w, r)
	var login struct{ Session auth.Session }
	if json.Unmarshal(w.Body.Bytes(), &login) != nil || login.Session.AccessToken == "" {
		t.Fatal("API login failed")
	}
	check := func(token string) int {
		r := httptest.NewRequest("GET", origin+"/v1/libraries", nil)
		r.AddCookie(f.cookie)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		f.config.Catalog.ServeHTTP(w, r)
		return w.Code
	}
	if check("") != 200 || check(f.cookie.Value) != 401 || check(login.Session.AccessToken) != 200 {
		t.Fatal("API credential isolation")
	}
	b := f.cookie
	f.cookie = &http.Cookie{Name: b.Name, Value: login.Session.AccessToken}
	if check("") != 401 || f.req("GET", "/web/libraries", nil).Code != 303 {
		t.Fatal("CLI token accepted as cookie")
	}
	f.cookie = b
	if err := f.db.ResetPassword(t.Context(), "observer", "replaced", f.now); err != nil {
		t.Fatal(err)
	}
	if check(login.Session.AccessToken) != 401 || check("") != 401 || f.req("GET", "/web/libraries", nil).Code != 303 {
		t.Fatal("password reset failed to revoke both sessions")
	}
}
