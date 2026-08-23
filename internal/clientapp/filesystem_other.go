//go:build !linux && !darwin && !windows

package clientapp

import (
	"errors"
	"os"
)

func requireSupportedFilesystem(*os.File) error {
	return errors.New("unsupported worktree platform; Linux/ext4, macOS/APFS, or Windows/NTFS is required")
}

var validateWorktreeDirectoryHandle = func(int) error { return nil }
