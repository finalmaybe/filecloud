# 桌面开发预览验收记录 · 2026-11-22

范围：[PRD A1–A6](../design/desktop-client-prd.md)、[构建与复测步骤](../../desktop/README.md)。以下使用临时服务、虚构账号和一次性工作目录；Web 控制台关闭。三端预览包已在本地构建，Linux 同时提供 `.deb` 和 `.AppImage`，未发布为正式版本。

| 环境 | 本轮实际执行 | 仍需验证 |
|---|---|---|
| Linux amd64 | Ubuntu 24.04 原生架构容器构建 GTK 4/WebKitGTK 6 `.deb` 并用 `dpkg` 安装；Linux 本地 ext4 上运行桌面模块真实双设备测试和 `-race`。在开发机图形会话中使用 GTK 4 常规后端：向已绑定的真实窗口发送关闭请求，确认窗口隐藏、进程仍在，修改工作文件，由第二个客户端收到同一内容；再启动后原进程的窗口重新可见。 | 容器内的 Xvfb 启动被 WebKit 的命名空间要求阻止，没有关闭沙箱绕过；干净 Ubuntu GNOME 会话、无托盘扩展、读屏和 200% 缩放仍需人工走查。 |
| Windows 11 amd64 / 固定 NTFS | 在 Windows 验收来宾原生编译 GUI 与 CLI，用 NSIS 打包；来宾安装到隔离目录，使用**安装后的** CLI 运行 `TestDesktopTwoDevices`，通过后运行卸载程序。 | SSH 会话不是交互桌面；开始菜单、通知点击、关窗唤回、DPI/高对比和公开发行签名待验。 |
| macOS 27.0.1 arm64 / APFS | 在 Mac 原生构建 `.app`、ad-hoc 签名、生成并挂载 DMG，通过 `codesign --verify --deep --strict`；使用包内 CLI 在 APFS 上运行 `TestDesktopTwoDevices`。实际启动包内 GUI 进程并检查存活后退出。 | 进程启动检查不代替人工 Dock、⌘W/⌘Q、VoiceOver 和取消面板的验收。Developer ID 签名及公证待办。 |

**Linux AppImage 运行库验收：** Ubuntu 24.04 amd64 打包 GTK 4、WebKitGTK 6 和 JavaScriptCore；对比 `.deb` 的 CLI 字节与 GUI Go build ID，使用 `ldd` 验证 GUI 及 WebKit 子进程从镜像解析这三套库。另在系统缺少 `libwebkitgtk-6.0.so.4` 的 Arch 桌面清除外部 `LD_LIBRARY_PATH`，直接启动 FUSE 镜像：确认登录界面和资料库实际渲染，并从运行中 WebKitWebProcess 的映射核对所用库来自镜像。用含空格的路径运行，关窗后修改本地文件，第二设备收到相同内容，再次启动能唤回原窗口。未关闭 WebKit 沙箱，未向宿主安装 GTK/WebKit 软件包。自启动单测覆盖外层文件路径转义、移动后的状态和取消注册。

`TestDesktopTwoDevices` 调用桌面 Go 工作空间与真实随包子程序，覆盖登录、建库、选目录后绑定、后台调度、第二台设备收到文件、保护触发、待办时拒绝解绑、取消/错误候选/本地变更后旧确认失效、重启后重新核对、明确确认后的删除传播、只读历史、解绑和注销。测试替换目录选择器及系统密钥环，不把这些结果算作原生弹窗或凭据库验收。

**回归与发布阻塞：** 根模块 `go test ./...`、前端 TypeScript 构建、桌面模块 `go vet` 和 Linux `go test -race` 已通过。Linux 的 `FILECLOUD_RUN_1B=1` 完整门禁两次在 `TestLibraryBindCheckoutOwnsOnlyRegisteredDirectories/registered_directory_replacement` 失败：首次绑定的落盘中断后，原目录被替换，恢复被拒绝，替代文件内容仍在，但替代目录的修改时间发生变化。未改该断言，也未把普通回归通过当作门禁通过。正式发布前须定位并修复，再重跑三端 1B、完整 1C、升级/卸载与 A1–A6；本轮没有签名发布或远程上传安装包。
