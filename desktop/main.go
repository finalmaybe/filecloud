package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

//go:embed frontend/dist
var assets embed.FS
var version = "dev"
var commit = "unknown"

type Preferences struct {
	app           *application.App
	startup       loginStartup
	notifications *notifications.NotificationService
	ready         atomic.Bool
}

func (p *Preferences) EnableNotifications() (bool, error) {
	if !p.ready.Load() {
		return false, nil
	}
	return p.notifications.RequestNotificationAuthorization()
}

func (p *Preferences) AutoStart() (bool, error) { return p.startup.IsEnabled() }
func (p *Preferences) SetAutoStart(on bool) error {
	if on {
		return p.startup.Enable()
	}
	return p.startup.Disable()
}
func (p *Preferences) Quit() { go p.app.Quit() }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Filecloud:", err)
		os.Exit(1)
	}
}
func run() error {
	web, _ := fs.Sub(assets, "frontend/dist")
	icon, err := fs.ReadFile(web, "app-icon-1024.png")
	if err != nil {
		return errors.New("缺少嵌入的桌面资源。")
	}
	var win *application.WebviewWindow
	show := func() {
		if win != nil {
			win.Show()
			win.Restore()
			win.Focus()
		}
	}
	app := application.New(application.Options{Name: "Filecloud", Description: "资料库同步客户端", Icon: icon, Logger: slog.New(slog.DiscardHandler), Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(web), DisableLogging: true}, SingleInstance: &application.SingleInstanceOptions{UniqueID: "org.filecloud.desktop", OnSecondInstanceLaunch: func(application.SecondInstanceData) { show() }}})
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	cli := "filecloud-client"
	if runtime.GOOS == "windows" {
		cli += ".exe"
	}
	s, err := newWorkspace(filepath.Join(root, "filecloud-desktop"), filepath.Join(filepath.Dir(exe), cli), version, commit)
	if err != nil {
		win = app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "Filecloud · 启动失败", Width: 540, Height: 220, HTML: `<!doctype html><meta charset="utf-8"><main style="font:16px system-ui;padding:20px"><h2>无法打开工作空间</h2><p>请检查桌面状态目录是否可读，并确认安装包内的 Filecloud 和 CLI 版本一致。原工作文件会保留。</p></main>`})
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) { go app.Quit() })
		return app.Run()
	}
	app.RegisterService(application.NewService(s))
	prefs := &Preferences{app: app, startup: app.Autostart, notifications: notifications.New()}
	// An AppImage's mounted executable disappears on exit; register the outer image instead.
	if image, dir := os.Getenv("APPIMAGE"), os.Getenv("APPDIR"); runtime.GOOS == "linux" && filepath.IsAbs(image) && filepath.IsAbs(dir) && exe == filepath.Join(dir, "usr/lib/filecloud-desktop/filecloud-desktop") {
		prefs.startup = imageStartup{image: image, entry: filepath.Join(root, "autostart", "org.filecloud.desktop.AppImage.desktop")}
	}
	app.RegisterService(application.NewService(prefs))
	win = app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "Filecloud", Name: "workspace", Width: 1120, Height: 740, MinWidth: 480, MinHeight: 420, URL: "/"})
	s.pick = func() (string, error) {
		return app.Dialog.OpenFile().CanChooseFiles(false).CanChooseDirectories(true).AttachToWindow(win).SetTitle("选择工作目录").PromptForSingleSelection()
	}
	s.open = app.Browser.OpenFile
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { win.Hide(); e.Cancel() })
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { show() })
	tray := app.SystemTray.New()
	tray.SetIcon(icon)
	tray.SetTooltip("Filecloud")
	menu := app.NewMenu()
	menu.Add("打开 Filecloud").OnClick(func(*application.Context) { show() })
	menu.Add("退出 Filecloud").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)
	tray.OnClick(show)
	stop := make(chan struct{})
	ns := prefs.notifications
	s.notify = func(id string) {
		if prefs.ready.Load() {
			if allowed, _ := ns.CheckNotificationAuthorization(); allowed {
				_ = ns.SendNotification(notifications.NotificationOptions{ID: id, Title: "Filecloud", Body: "有同步事项需要处理，请打开客户端查看。"})
			}
		}
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		if err := ns.ServiceStartup(context.Background(), application.ServiceOptions{}); err == nil {
			prefs.ready.Store(true)
			ns.OnNotificationResponse(func(r notifications.NotificationResult) {
				if r.Error == nil {
					show()
					app.Event.Emit("attention", r.Response.ID)
				}
			})
		}
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case now := <-ticker.C:
					s.tick(now)
				case <-stop:
					return
				}
			}
		}()
	})
	app.OnShutdown(func() {
		s.closed.Store(true)
		close(stop)
		s.mu.Lock()
		s.mu.Unlock()
		if prefs.ready.Load() {
			_ = ns.ServiceShutdown()
		}
	})
	return app.Run()
}
