package webconsole

import (
	"encoding/base64"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestHandoffPathIsOneLiteralArgument(t *testing.T) {
	for _, name := range []string{"survey/O'Brien & Co.md", "survey/春季 ’ 复核.md"} {
		if runtime.GOOS == "windows" {
			s := `[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(` + literal(name, true) + `))`
			out, err := exec.CommandContext(t.Context(), "powershell", "-NoProfile", "-NonInteractive", "-Command", s).Output()
			if err != nil || strings.TrimSpace(string(out)) != base64.StdEncoding.EncodeToString([]byte(name)) {
				t.Fatal("PowerShell path changed", err)
			}
		} else {
			out, err := exec.CommandContext(t.Context(), "sh", "-c", "printf '%s' "+literal(name, false)).Output()
			if err != nil || string(out) != name {
				t.Fatal("shell path changed", err)
			}
		}
	}
}
