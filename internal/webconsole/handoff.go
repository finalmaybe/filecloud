package webconsole

import (
	"encoding/base64"
	"github.com/mingming-cn/filecloud/internal/library"
	"strings"
)

func literal(s string, powershell bool) string {
	if powershell {
		if strings.ContainsAny(s, "‘’‚‛") {
			return "([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString([]byte(s)) + "')))"
		}
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

type recoveryGuide struct {
	library.Snapshot
	Status, Sync, Preview, Shell string
}

func guide(s library.Snapshot, ps bool) recoveryGuide {
	args := " --client-dir 'CLIENT_DIR' --worktree 'WORKTREE'"
	label := "POSIX shell"
	if ps {
		label = "PowerShell"
	}
	return recoveryGuide{s, "filecloud library status" + args, "filecloud library sync" + args, "filecloud library restore" + args + " --commit " + s.CommitID + " --path " + literal(s.Path, ps), label}
}
