// Command filecloud-client runs the Filecloud client and transitional server-command compatibility entry.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mingming-cn/filecloud/internal/clientapp"
)

var (
	_version   = "dev"
	_commit    = "unknown"
	_buildDate = "unknown"
)

func main() {
	os.Exit(mainCode())
}

func mainCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := clientapp.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, clientapp.BuildInfo{
		Version: _version, Commit: _commit, BuildDate: _buildDate,
	}); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, "filecloud-client:", err); writeErr != nil {
			return 1
		}
		return 1
	}
	return 0
}
