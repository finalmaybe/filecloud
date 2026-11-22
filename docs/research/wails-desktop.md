# Wails 用于三端桌面客户端的选型依据

核对日期：2026-11-22。范围：Linux、macOS、Windows 桌面端。本文是选型建议，未做 Filecloud GUI 实机验收。

- **维护状态。** [v3.0.0](https://github.com/wailsapp/wails/releases/tag/v3.0.0) 于 10 月 26 日正式发布，是一次应用模型重写；v2 继续接收关键更新。[v3.1.0](https://github.com/wailsapp/wails/releases/tag/v3.1.0) 于 11 月 1 日发布，已修复托盘、通知与多显示器缩放等问题。新项目可用 v3 稳定标签，首版避开 nightly 和标为实验性的无 CGO 后端。
- **生态。** v3 提供 Go 服务绑定、事件、原生菜单、对话框、托盘、通知与安装包工具。可使用常规 Web 前端组件。[官方案例](https://github.com/wailsapp/wails/tree/v3.1.0/docs/mpress/content/community/showcase)包括三端 Redis 客户端 Tiny RDM，以及浏览、恢复备份的 Restic Browser；它们是 Wails 应用案例，不是 Filecloud 或 v3 的验收记录。
- **发布差异。** [安装文档](https://github.com/wailsapp/wails/blob/v3.1.0/docs/mpress/content/getting-started/installation.md)列出的 Linux 基线是 Ubuntu 24.04，默认使用 GTK 4 和 WebKitGTK 6.0；Windows 需要 WebView2。[托盘文档](https://github.com/wailsapp/wails/blob/v3.1.0/docs/mpress/content/features/menus/systray.md)明确指出 GNOME 顶栏需要扩展，应保留从应用菜单重新打开窗口的入口。[自启动文档](https://github.com/wailsapp/wails/blob/v3.1.0/docs/mpress/content/features/autostart/basics.md)区分了三种平台的用户登录启动机制。

**建议：** 让 Wails 负责界面，复用现有 Go 同步实现和[离线状态](../../internal/clientapp/library_status.go)。先在[已有的三端验收环境](../distribution.md)加桌面安装测试，走通“绑定目录 → 关窗后仍同步 → 重新打开查看状态 → 对大量删除显式确认”，再决定首版覆盖哪些 Linux 发行版。

## 同类桌面同步客户端

- **Nextcloud Desktop v34.0.7：** 三端共用 C++/Qt 6 主客户端，`nextcloudCore` 直接链接 `Nextcloud::sync`、Qt Widgets 和 Qt Quick/QML（[构建清单](https://github.com/nextcloud/desktop/blob/v34.0.7/src/gui/CMakeLists.txt#L576-L592)）。[平台扩展](https://github.com/nextcloud/desktop/tree/v34.0.7/shell_integration)分别面向 Linux 的 Nautilus/Dolphin、macOS Finder、Windows Explorer。按需文件另有 macOS File Provider 和 [Windows Cloud Files 后端](https://github.com/nextcloud/desktop/tree/v34.0.7/src/libsync/vfs/cfapi)。
- **Seafile Sync Client v9.0.22：** 三端共用 C++/Qt Widgets GUI，[构建清单](https://github.com/haiwen/seafile-client/blob/v9.0.22/CMakeLists.txt)同时支持 Qt 5、Qt 6。GUI [启动独立的 `seaf-daemon`](https://github.com/haiwen/seafile-client/blob/v9.0.22/src/daemon-mgr.cpp) 并通过[本机 RPC](https://github.com/haiwen/seafile-client/blob/v9.0.22/src/rpc/rpc-client.cpp)控制它。平台适配包括 [Windows Explorer 扩展](https://github.com/haiwen/seafile-client/tree/v9.0.22/extensions)、[macOS Finder Sync 扩展](https://github.com/haiwen/seafile-client/tree/v9.0.22/fsplugin)以及 Linux Qt D-Bus。这里比较的是同步到普通目录的客户端；SeaDrive 属于另一种[按需访问产品](seafile-sync.md)。
