package syncignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNameRules(t *testing.T) {
	r := Rules{SkipHidden: true, Patterns: []string{"*.tmp", "node_modules", "image-?.png"}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		".settings": true, "photo.tmp": true, "node_modules": true, "image-一.png": true,
		"photo.tmp.txt": false, "node_modules_old": false, "image-01.png": false,
	} {
		if got := r.Match(name); got != want {
			t.Errorf("Match(%q) = %v, want %v", name, got, want)
		}
	}
	for _, invalid := range []string{"", "dir/file", `dir\file`, "foo["} {
		if err := (Rules{Patterns: []string{invalid}}).Validate(); err == nil {
			t.Errorf("accepted invalid rule %q", invalid)
		}
	}
}

func TestLoadMissingAndInvalidRules(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sync-ignore.json")
	r, err := Load(file)
	if err != nil || r.Active() || r.Match(".hidden") {
		t.Fatalf("missing file should keep existing sync behavior: rules=%+v err=%v", r, err)
	}
	for _, invalid := range []string{"{", `{"Patterns":["dir/file"]}`} {
		if err := os.WriteFile(file, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(file); err == nil {
			t.Fatal("invalid config silently disabled rules")
		}
	}
}
