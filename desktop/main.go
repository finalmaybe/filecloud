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

func (p *Preferences) NotificationsAllowed() bool {
	if !p.ready.Load() {
		return false
	}
	on, _ := p.notifications.CheckNotificationAuthorization()
	return on
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
	width, height := 1440, 900
	if !s.Snapshot().SignedIn {
		width, height = 830, 450
	}
	win = app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "Filecloud", Name: "workspace", Width: width, Height: height, MinWidth: 480, MinHeight: 420, URL: "/"})
	s.pick = func() (string, error) {
		return app.Dialog.OpenFile().CanChooseFiles(false).CanChooseDirectories(true).AttachToWindow(win).SetTitle("选择工作目录").PromptForSingleSelection()
	}
	s.open = app.Browser.OpenFile
	s.reveal = func(path string) error { return revealInManager(app, path) }
	s.openWeb = app.Browser.OpenURL
	s.saveFile = func(name string) (string, error) {
		return app.Dialog.SaveFile().AttachToWindow(win).SetFilename(name).PromptForSingleSelection()
	}
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { win.Hide(); e.Cancel() })
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { show() })
	tray := app.SystemTray.New()
	tray.SetIcon(icon)
	tray.SetTooltip("Filecloud")
	menu := app.NewMenu()
	quick := app.Window.NewWithOptions(application.WebviewWindowOptions{Name: "quick", Title: "Filecloud · 同步概览", Width: 340, Height: 384, DisableResize: true, Hidden: true, Frameless: true, AlwaysOnTop: true, HideOnFocusLost: true, Windows: application.WindowsWindow{HiddenOnTaskbar: true}, URL: "/quick.html"})
	tray.AttachWindow(quick).WindowOffset(6)
	menu.Add("同步概览").OnClick(func(*application.Context) { tray.ShowWindow() })
	menu.Add("需要处理").OnClick(func(*application.Context) { show(); app.Event.Emit("navigate", "attention") })
	menu.AddSeparator()
	menu.Add("打开 Filecloud").OnClick(func(*application.Context) { show() })
	menu.Add("设置…").OnClick(func(*application.Context) { show(); app.Event.Emit("navigate", "settings") })
	menu.Add("退出 Filecloud").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)
	syncCtx, stopSync := context.WithCancel(context.Background())
	defer stopSync()
	ns := prefs.notifications
	s.notify = func(id string) {
		if prefs.ready.Load() {
			if allowed, _ := ns.CheckNotificationAuthorization(); allowed {
				body := "有同步事项需要处理，请打开客户端查看。"
				if id == "" {
					body = "本轮同步已完成。"
					id = "sync-complete"
				}
				_ = ns.SendNotification(notifications.NotificationOptions{ID: id, Title: "Filecloud", Body: body})
			}
		}
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		if err := ns.ServiceStartup(context.Background(), application.ServiceOptions{}); err == nil {
			prefs.ready.Store(true)
			ns.OnNotificationResponse(func(r notifications.NotificationResult) {
				if r.Error == nil {
					show()
					if r.Response.ID == "sync-complete" {
						app.Event.Emit("navigate", "activity")
					} else {
						app.Event.Emit("attention", r.Response.ID)
					}
				}
			})
		}
		go s.runAutoSync(syncCtx)
	})
	app.OnShutdown(func() {
		s.closed.Store(true)
		stopSync()
		s.mu.Lock()
		s.mu.Unlock()
		if prefs.ready.Load() {
			_ = ns.ServiceShutdown()
		}
	})
	return app.Run()
}
