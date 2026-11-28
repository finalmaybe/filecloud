package serverapp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunServerCommandContract(t *testing.T) {
	build := BuildInfo{Version: "v1.2.3", Commit: "0123456789abcdef", BuildDate: "2026-08-16T00:00:00Z"}
	var stdout bytes.Buffer
	if err := Run(t.Context(), "filecloud-server", []string{"version"}, strings.NewReader(""), &stdout, io.Discard, build); err != nil {
		t.Fatalf("Run(version): %v", err)
	}
	var got BuildInfo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode version output %q: %v", stdout.String(), err)
	}
	if got != build {
		t.Fatalf("version output = %+v, want %+v", got, build)
	}

	for _, command := range []string{"login", "logout", "library"} {
		err := Run(t.Context(), "filecloud-server", []string{command}, strings.NewReader(""), io.Discard, io.Discard, build)
		if err == nil || err.Error() != `unknown command "`+command+`"` {
			t.Errorf("Run(%q) error = %v, want unknown command", command, err)
		}
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{want: "usage: filecloud-server <version|init|serve|gc|integrity|user>"},
		{args: []string{"version", "extra"}, want: "usage: filecloud-server version"},
		{args: []string{"init"}, want: "usage: filecloud-server init"},
		{args: []string{"serve"}, want: "usage: filecloud-server serve"},
		{args: []string{"serve", "--data-dir", ".", "--site-url="}, want: "--site-url: use http:// or https://"},
		{args: []string{"serve", "--data-dir", ".", "--web-admin-user", "admin"}, want: "--web-admin-user requires --site-url"},
		{args: []string{"serve", "--data-dir", ".", "--history-global-capacity", "0"}, want: "history limits must be positive"},
		{args: []string{"serve", "--data-dir", ".", "--history-user-capacity", "-1"}, want: "history limits must be positive"},
		{args: []string{"serve", "--public-origin", "https://cloud.example"}, want: "flag provided but not defined: -public-origin"},
		{args: []string{"serve", "--web-origin", "https://cloud.example"}, want: "flag provided but not defined: -web-origin"},
		{args: []string{"serve", "--dav-origin", "https://cloud.example"}, want: "flag provided but not defined: -dav-origin"},
		{args: []string{"gc"}, want: "usage: filecloud-server gc"},
		{args: []string{"integrity"}, want: "usage: filecloud-server integrity"},
		{args: []string{"user"}, want: "usage: filecloud-server user"},
	} {
		err := Run(t.Context(), "filecloud-server", test.args, strings.NewReader(""), io.Discard, io.Discard, build)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Run(%q) error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestRunLogsOneLifecyclePairWithoutSensitivePaths(t *testing.T) {
	sensitivePath := t.TempDir() + "/private/metadata.db"
	var logs bytes.Buffer
	err := Run(t.Context(), "filecloud-server", []string{"serve", "--data-dir", sensitivePath, "--listen", "127.0.0.1:0"},
		strings.NewReader(""), io.Discard, &logs, BuildInfo{})
	if err == nil {
		t.Fatal("Run(serve) unexpectedly succeeded")
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %q, want one start/completion pair", lines)
	}
	for index, line := range lines {
		var event map[string]string
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode log line %d %q: %v", index, line, err)
		}
		if event["command"] != "serve" {
			t.Errorf("log command = %q, want serve", event["command"])
		}
	}
	if strings.Contains(logs.String(), sensitivePath) || strings.Contains(logs.String(), "metadata.db") {
		t.Fatalf("logs exposed internal path: %q", logs.String())
	}
}

func TestServerSafetyDefaults(t *testing.T) {
	if _defaultListen != "127.0.0.1:8080" {
		t.Fatalf("default listen = %q, want loopback", _defaultListen)
	}
	if _defaultGlobalKDFCapacity != 2 || _defaultSourceIPKDFCapacity != 1 || _defaultUsernameKDFCapacity != 1 {
		t.Fatalf("KDF concurrency defaults = %d/%d/%d, want 2/1/1",
			_defaultGlobalKDFCapacity, _defaultSourceIPKDFCapacity, _defaultUsernameKDFCapacity)
	}
	if _requestReadPeriod != 30*time.Second || _shutdownPeriod != 5*time.Second {
		t.Fatalf("request/shutdown defaults = %s/%s, want 30s/5s", _requestReadPeriod, _shutdownPeriod)
	}
}

func TestRequestTrackerStopsNewWorkAndDrainsActiveRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	requestDone := make(chan struct{})
	tracker := newRequestTracker(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	go func() {
		defer close(requestDone)
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("request panic: %v", recovered)
			}
		}()
		tracker.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-started

	tracker.stop()
	rejected := httptest.NewRecorder()
	tracker.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("request after stop status = %d, want %d", rejected.Code, http.StatusServiceUnavailable)
	}
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("wait panic: %v", recovered)
			}
		}()
		tracker.wait()
	}()
	select {
	case <-waitDone:
		t.Fatal("tracker drained before active request completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-requestDone
	<-waitDone
}
