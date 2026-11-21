package webconsole_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/webconsole"
)

func TestSharedLibraryAPIWithCookieJar(t *testing.T) {
	f := newFixture(t)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	browser := browserOrigin(t, srv.URL)
	var err error
	f.config.Catalog, err = library.NewHandler(f.db, nil, library.Config{BrowserOrigin: browser})
	if err != nil {
		t.Fatal(err)
	}
	web, err := webconsole.NewHandler(browser, f.config)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/web/", web)
	mux.Handle("/v1/sessions", f.sessions)
	mux.Handle("/", f.config.Catalog)
	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	webURL, _ := url.Parse(srv.URL + "/web/")
	jar.SetCookies(webURL, []*http.Cookie{{Name: auth.BrowserCookieName, Value: "obsolete", Path: "/web/"}})

	do := func(req *http.Request, want int) string {
		t.Helper()
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s: status %d, want %d", req.Method, req.URL.Path, res.StatusCode, want)
		}
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	login, _ := http.NewRequest("POST", srv.URL+"/web/login", strings.NewReader(url.Values{"username": {"observer"}, "password": {f.pass}}.Encode()))
	login.Header.Set("Origin", srv.URL)
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	do(login, 303)
	if cookies := jar.Cookies(webURL); len(cookies) != 1 || cookies[0].Value == "obsolete" {
		t.Fatal("legacy cookie not replaced")
	}
	payload, _ := json.Marshal(map[string]string{"Username": "observer", "Password": f.pass, "DeviceName": "cli"})
	cliLogin, _ := http.NewRequest("POST", srv.URL+"/v1/sessions", strings.NewReader(string(payload)))
	cliLogin.Header.Set("Content-Type", "application/json")
	var result struct{ Session auth.Session }
	if err := json.Unmarshal([]byte(do(cliLogin, 200)), &result); err != nil || result.Session.AccessToken == "" {
		t.Fatal("missing CLI session")
	}

	id, _ := auth.NewUserID(nil)
	path := "/v1/libraries/" + id
	request := func(method, path, body string) *http.Request {
		t.Helper()
		r, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	const create = `{"Name":"Web-created"}`
	for _, origin := range []string{"", "null", "https://other.example"} {
		for _, r := range []*http.Request{request("PUT", path, create), request("DELETE", path+"/file-history/"+strings.Repeat("a", 64), "")} {
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
			do(r, 403)
		}
	}
	r := request("GET", "/v1/libraries", "")
	r.Host = "other.example"
	// A real cookie jar withholds this cookie from an unrelated Host.
	r.AddCookie(jar.Cookies(webURL)[0])
	do(r, 421)
	r = request("PUT", path, create)
	r.Header.Set("Origin", srv.URL)
	do(r, 201)
	webList := do(request("GET", "/v1/libraries", ""), 200)
	if !strings.Contains(webList, id) {
		t.Fatal("new library missing from list")
	}
	do(request("GET", path, ""), 200)
	for _, suffix := range []string{"head", "blocks/test", "objects/files/test"} {
		r := request("PUT", path+"/"+suffix, "")
		r.Header.Set("Origin", srv.URL)
		do(r, 401)
	}
	r = request("GET", "/v1/libraries", "")
	r.Header.Set("Authorization", "Bearer invalid")
	do(r, 401)

	disabled, err := library.NewHandler(f.db, nil, library.Config{})
	if err != nil {
		t.Fatal(err)
	}
	off := httptest.NewServer(disabled)
	defer off.Close()
	for _, base := range []string{srv.URL, off.URL} {
		r, _ := http.NewRequest("GET", base+"/v1/libraries", nil)
		if base == off.URL {
			do(r, 401)
		}
		r.Header.Set("Authorization", "Bearer "+result.Session.AccessToken)
		if cliList := do(r, 200); cliList != webList {
			t.Fatal("CLI and Web library listings differ")
		}
	}
}
