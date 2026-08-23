package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/serverapp"
)

func TestRunUsesExecutableBuildMetadata(t *testing.T) {
	originalVersion, originalCommit, originalBuildDate := _version, _commit, _buildDate
	t.Cleanup(func() {
		_version, _commit, _buildDate = originalVersion, originalCommit, originalBuildDate
	})
	_version, _commit, _buildDate = "v1.2.3", "0123456789abcdef", "2026-08-16T00:00:00Z"

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"version"}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatalf("run version: %v", err)
	}
	var got serverapp.BuildInfo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode version output %q: %v", stdout.String(), err)
	}
	want := serverapp.BuildInfo{Version: _version, Commit: _commit, BuildDate: _buildDate}
	if got != want {
		t.Fatalf("version output = %+v, want %+v", got, want)
	}
}

func TestRunRejectsClientCommands(t *testing.T) {
	for _, command := range []string{"login", "logout", "library"} {
		err := run(t.Context(), []string{command}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || err.Error() != `unknown command "`+command+`"` {
			t.Errorf("run(%q) error = %v, want unknown command", command, err)
		}
	}
}
