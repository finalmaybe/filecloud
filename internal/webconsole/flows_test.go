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
	catalog, err := library.NewHandler(db, nil, library.Config{})
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
	f.web, err = webconsole.NewHandler(origin, "127.0.0.1:0", f.config)
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
func (f *fixture) login() {
	f.t.Helper()
	w := f.req("POST", "/web/login", url.Values{"username": {"observer"}, "password": {f.pass}})
	if w.Code != 303 {
		f.t.Fatalf("login status %d", w.Code)
	}
	f.cookie = w.Result().Cookies()[0]
}
func TestSessionLifecycle(t *testing.T) {
	f := newFixture(t)
	bad := f.req("POST", "/web/login", url.Values{"username": {"observer"}, "password": {"invalid"}})
	if bad.Code != 401 || bad.Header().Get("Set-Cookie") != "" {
		t.Fatal("invalid login accepted")
	}
	f.login()
	if !f.cookie.Secure || !f.cookie.HttpOnly || f.cookie.Path != "/web/" || f.cookie.SameSite != http.SameSiteLaxMode {
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
	api := auth.RequireSession(f.db, func() time.Time { return f.now }, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	check := func(token string) int {
		r := httptest.NewRequest("GET", "/v1/libraries", nil)
		r.AddCookie(f.cookie)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w.Code
	}
	if check("") != 401 || check(f.cookie.Value) != 401 || check(login.Session.AccessToken) != 204 {
		t.Fatal("API credential isolation")
	}
	b := f.cookie
	f.cookie = &http.Cookie{Name: b.Name, Value: login.Session.AccessToken}
	if f.req("GET", "/web/libraries", nil).Code != 303 {
		t.Fatal("CLI token accepted as cookie")
	}
	f.cookie = b
	if err := f.db.ResetPassword(t.Context(), "observer", "replaced", f.now); err != nil {
		t.Fatal(err)
	}
	if check(login.Session.AccessToken) != 401 || f.req("GET", "/web/libraries", nil).Code != 303 {
		t.Fatal("password reset failed to revoke both sessions")
	}
}
