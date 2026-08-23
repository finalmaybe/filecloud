package main

import (
	"strings"
	"testing"
)

func TestValidateVersionOutput(t *testing.T) {
	valid := []byte(`{"Version":"v1.2.3","Commit":"0123456789abcdef","BuildDate":"2026-08-23T00:00:00Z"}`)
	if err := validateVersionOutput("client", valid, "v1.2.3"); err != nil {
		t.Fatalf("validateVersionOutput(valid): %v", err)
	}

	for _, test := range []struct {
		name   string
		output []byte
		want   string
	}{
		{name: "malformed", output: []byte(`{`), want: "decode server version output"},
		{name: "wrong version", output: valid, want: "unexpected server version metadata"},
		{name: "unknown commit", output: []byte(`{"Version":"v1.2.3","Commit":"unknown","BuildDate":"2026-08-23T00:00:00Z"}`), want: "unexpected server version metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			expected := "v1.2.3"
			if test.name == "wrong version" {
				expected = "v9.9.9"
			}
			err := validateVersionOutput("server", test.output, expected)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVersionOutput() error = %v, want %q", err, test.want)
			}
		})
	}
}
