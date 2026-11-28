# Filecloud Desktop（开发预览）

三端共用 Wails v3.1.0、TypeScript 和同版本的 `filecloud-client` 子程序；无需开启服务端 Web 控制台。界面和交互以 [16 页桌面设计稿](../docs/design/desktop-mockups/filecloud-desktop-design.pdf) 为准；[首版 PRD](../docs/design/desktop-client-prd.md) 与[技术方案](../docs/design/desktop-client-architecture.md)记录原始基础。本目录独立于根 Go module，不影响服务器构建。当前是测试资料库用的开发预览：见[三端实测结果和 Linux 1B 发布阻塞](../docs/acceptance/desktop-preview.md)。

[按设计稿逐页核对的差异、实测截图与验收范围](../docs/acceptance/desktop-design-parity.md)。

## 试用

在「连接你的 Filecloud」登录 → 选择或新建资料库 → 选择本地文件夹，核对文件系统、读写权限、目录内容和同步方向后连接。地址步骤只访问 `/healthz`，确认服务可用后才提交账号密码。首次导入非空目录要另行勾选。解除同步后，要使用原文件夹，点「同步该资料库」，选择原目录，核对上传、下载和冲突文件，再勾选「和已有文件夹同步」。两端一致时无需重新下载；同名但内容不同的文件保留两份。只支持本机 ext4、APFS、固定 NTFS；不要让两套客户端状态管理同一个工作目录。

- 已绑定资料库会监听整个本地文件夹：保存、改名或删除后自动同步。云端发布新版本时，在线客户端也会自动拉取。连续保存会合并处理；暂停和删除确认仍然有效。每轮结束后保留约 30 秒的定时检查，漏掉通知也能赶上。云端通知需要更新服务端且桌面账号会话有效；旧服务端仍可定时拉取，无需开启 Web 控制台。
- 「设置 → 通用 → 同步忽略规则」适用于本机所有资料库。点「编辑规则…」，每行填写一个名称或通配模式（如 `*.tmp` 或 `node_modules`），匹配文件夹会跳过其全部内容。默认同步以 `.` 开头的隐藏文件，可在这里关闭。保存后从下一轮同步起跳过匹配项，不删除已有副本；移除规则后恢复同步。若资料库已经在等待删除确认，可在「检查删除」中点「按当前规则重新检查」，无需批准旧候选。
- 可手动立即同步、暂停后续轮次、打开工作目录和浏览分页主线历史。关窗保持监听；再次从系统入口启动会唤回已有窗口，选择「退出应用」才停止。
- 「需要处理 → 检查删除」列出工作目录、删除计数、占比和候选码。取消不会发布。遇到冲突时，可并排对照实际保留的两份内容，标记已整理不会删除文件。
- 「资料库 → 浏览文件」查看已发布版本，支持文本、图片、PDF，以及 Word（`.docx`）正文和 Excel（`.xlsx`）工作表的只读预览。点击工作表名称即可展开或收起，公式显示文件中保存的结果。Office 文件最大 12 MiB；旧版 `.doc`、`.xls` 或需查看完整排版时，可下载原文件。从文件的「更多操作 → 历史版本」可在检查器中查看和恢复该文件的版本；「版本历史」可查看合并来源、浏览快照、预览并确认前向恢复。取消历史恢复会撤销本次未确认的预览。
- 后台同步时仍可浏览云端文件、预览内容和查看文件历史。如果新旧快照只改了一个已有文件，本机先准备并核对新版，再替换这个文件；其他文件和目录留在原位。若新版刚安装、同步还没结束时又保存了目标文件，需要回滚时会在同目录留下可见的 `Filecloud recovered …` 副本，保留这次编辑。同步仍会扫描、校验资料库。涉及增删、改名或合并冲突时，沿用原有同步流程。升级前已经开始的一轮同步会按已记录的计划继续，无需重新绑定。
- 「回收站」可恢复文件、目录或版本记录。勾选已加载且符合筛选的条目，点「永久删除所选」并核对数量（每次最多 100 条）；筛选隐藏的条目会取消勾选。也可用某一行的「更多操作 → 永久删除」，核对路径后只删除该条目。确认前可取消；删除后无法从回收站恢复，当前文件和历史快照不受影响。「资料库设置」分别管理回收站与历史保留期。缩短历史保留期需要核对影响。文件内容、版本恢复、回收站和保留策略需要同时更新服务端（[HTTP 接口](../docs/design/http-api.md#桌面文件检查器与恢复)）；WebDAV 应用密码在独立登录的网页控制台管理。
- 「设置」可切换登录系统后启动、授权通知、重新登录或退出账号。账号 token 使用系统凭据库，不可用时仅在进程内保存；CLI 绑定原有的受限凭据存储不变。重新登录后从「资料库设置 → 刷新绑定凭据」刷新已收敛的绑定；若旧凭据失效且不能刷新，按[保全流程](../docs/operations/deployment-and-client.md#刷新绑定凭据)操作。
- 桌面状态位于 `os.UserConfigDir()/filecloud-desktop/`，按服务地址和所有者隔离；近期活动只保留 200 条。如需迁移，请先退出应用并保留工作目录和完整状态目录，不要手动编辑 `client.db`。

### Linux：运行 AppImage

将 AppImage 放在固定位置。以下是本地 `dev` 构建的文件名；从 Release 下载时，请使用含版本号的实际文件名，例如 `filecloud-desktop-v1.2.3-linux-amd64.AppImage`：

```bash
chmod +x filecloud-desktop-linux-amd64.AppImage
./filecloud-desktop-linux-amd64.AppImage
```

AppImage 自带 GTK 4、WebKitGTK 6、JavaScriptCore 以及匹配的 WebKit 子进程、加载器和资源；主机无需安装 WebKitGTK。构建基线是 Ubuntu 24.04 amd64（glibc 2.39），使用主机的图形驱动、字体、桌面会话和正常工作的 WebKit 沙箱工具（`bwrap`、`xdg-dbus-proxy`）。`.deb` 仍使用系统 GTK/WebKit，两种包共用本机状态。无法使用 FUSE 时，在固定的空目录中用 `--appimage-extract` 解包，从 `./squashfs-root/AppRun` 启动，并保留该目录。启用登录自启动后若移动 AppImage，请从新位置运行并重新开启自启动。

## 在目标系统构建

工具链沿用根目录的 Go 版本，另需 Node/npm。Ubuntu 24.04 安装 GCC、`pkg-config`、`libgtk-4-dev`、`libwebkitgtk-6.0-dev`、`dpkg-deb`、`squashfs-tools` 和 `curl`。AppImage 收集运行库还需要 `python3`、`patchelf`、`glib-networking`、`dconf-gsettings-backend`、`gsettings-desktop-schemas`、`adwaita-icon-theme`、`libgdk-pixbuf2.0-bin`、`librsvg2-common` 和 `gstreamer1.0-plugins-base`；macOS arm64 需要 Xcode 命令行工具；Windows amd64 需要 Git Bash、NSIS，以及运行应用所需的 WebView2。Windows 下让 `makensis` 位于 PATH。

```bash
# 在仓库根目录；必须在对应系统运行
bash desktop/build/package.sh
```

产物位于 `desktop/out/`：Linux `.deb` 与 `.AppImage`、macOS `.dmg`、Windows 用户级安装 `.exe`。GUI 与随包 CLI 使用同一 `VERSION`、`COMMIT`（默认 `dev`、当前提交）；不会从 PATH 找 CLI。包包含项目许可证和随包 Go 依赖的第三方声明；前端运行时随固定版本 Wails 内嵌，不从 CDN 加载。可单独准备 `desktop/frontend/dist` 后设置 `FRONTEND_PREBUILT=1` 在原生构建机打包。默认 Windows 不签名、macOS 使用 ad-hoc 签名；正式分发须另过签名、公证及发行门禁。

AppImage 从构建机安装的 Ubuntu 软件包收集运行库。发行方应在 WebKitGTK 等依赖有安全更新时更新构建机并重新打包；实际版本、来源包与版权文件在镜像内 `usr/lib/filecloud-desktop/licenses/linux-runtime/`。打包时按固定 SHA-256 校验[官方 Type 2 runtime](https://github.com/AppImage/type2-runtime/tree/fedfad51acc2860b2916a1eef32ba4a500ff31ef)；离线构建可将 `APPIMAGE_RUNTIME` 指向同一份 `runtime-x86_64` 文件。

开发时先在 `desktop/frontend` 运行 `npm ci && npm run build`。修改绑定方法后，在 `desktop/` 使用同版本的 `wails3 generate bindings -ts -b -i -names -noevents -d frontend/bindings`，然后重新构建前端。首次生成前若无嵌入目录，可在 `desktop/frontend` 运行 `npm ci && node assets.mjs`。PDF.js、字体和许可随前端资源离线打包，不加载远程脚本。桌面图标源文件为 `desktop/build/icons/folder.svg`。不要手改生成的 bindings。

## 验证

- 根目录 `go test ./...`；前端完成构建后，在 `desktop/` 运行 `go test ./...`。Linux/macOS 桌面 module 需 `CGO_ENABLED=1` 及原生依赖。
- 将 `FILECLOUD_DESKTOP_CLI` 和 `FILECLOUD_DESKTOP_SERVER` 设为本次构建的两个**绝对路径**，在目标系统受支持的本地卷上从 `desktop/` 运行 `go test -run '^TestDesktopTwoDevices$' -count=1 -v`。测试在该卷创建临时服务和两个设备，通过真实 CLI 验证两台设备无需手动操作即可互相收到修改，以及暂停待确认、拒绝错误候选、重启恢复、确认后删除传播、历史和解绑；测试不会使用实际系统凭据库。此项验证 Go 主进程与 CLI，不替代真实窗口验收。
- 推送 `vMAJOR.MINOR.PATCH` tag 后，[发布流程](../.github/workflows/release.yml)调用[桌面构建](../.github/workflows/desktop.yml)。桌面安装包命名为 `filecloud-desktop-<tag>-<os>-<arch>.<格式>`，与 CLI 归档一起发布到该 tag 的 GitHub Release，共用 `SHA256SUMS`。直接推送分支或创建 PR 不触发发布。真实安装、关窗、系统入口唤起、读屏/键盘及签名按 [A1–A6](../docs/design/desktop-client-prd.md) 单独验收。
