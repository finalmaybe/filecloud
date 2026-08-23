//go:build linux || darwin

package clientapp

import "syscall"

var testAccessDenied = error(syscall.EACCES)
