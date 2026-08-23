//go:build !windows

package clientapp

import "os"

func testModeMatches(info os.FileInfo, want os.FileMode) bool {
	return info.Mode().Perm() == want
}
