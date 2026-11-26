package main

import (
	"context"
	"net/url"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func revealInManager(app *application.App, file string) error {
	if bus, err := dbus.SessionBus(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		uri := url.URL{Scheme: "file", Path: file}
		if bus.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1").CallWithContext(ctx, "org.freedesktop.FileManager1.ShowItems", 0, []string{uri.String()}, "").Err == nil {
			return nil
		}
	}
	return app.Browser.OpenFile(filepath.Dir(file))
}
