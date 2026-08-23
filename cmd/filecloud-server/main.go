// Command filecloud-server initializes, administers, and serves a single-node Filecloud data directory.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/mingming-cn/filecloud/internal/serverapp"
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
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, "filecloud-server:", err); writeErr != nil {
			return 1
		}
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return serverapp.Run(ctx, "filecloud-server", args, stdin, stdout, stderr, serverapp.BuildInfo{
		Version: _version, Commit: _commit, BuildDate: _buildDate,
	})
}
