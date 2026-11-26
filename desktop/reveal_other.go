//go:build !linux

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"path/filepath"
	"strings"
)

func revealInManager(app *application.App, file string) error {
	// Wails expands environment variables in paths passed to OpenFileManager.
	if strings.Contains(file, "$") {
		return app.Browser.OpenFile(filepath.Dir(file))
	}
	return app.Env.OpenFileManager(file, true)
}
