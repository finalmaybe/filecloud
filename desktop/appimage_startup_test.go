package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppImageLoginStartup(t *testing.T) {
	s := imageStartup{image: `/home/test/My apps/100% "file" $x.AppImage`, entry: filepath.Join(t.TempDir(), "autostart", "filecloud.desktop")}
	must(t, s.Enable())
	b, err := os.ReadFile(s.entry)
	must(t, err)
	if !strings.Contains(string(b), `Exec="/home/test/My apps/100%% \\"file\\" \\$x.AppImage"`+"\n") {
		t.Fatalf("unexpected desktop entry: %q", b)
	}
	if on, err := s.IsEnabled(); !on || err != nil {
		t.Fatalf("enabled = %v, %v", on, err)
	}
	s.image = "/another/place/Filecloud.AppImage"
	if on, err := s.IsEnabled(); on || err != nil {
		t.Fatalf("moved image enabled = %v, %v", on, err)
	}
	must(t, s.Enable())
	must(t, s.Disable())
	if on, err := s.IsEnabled(); on || err != nil {
		t.Fatalf("disabled = %v, %v", on, err)
	}
}
