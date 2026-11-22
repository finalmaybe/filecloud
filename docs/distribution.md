# Filecloud 分发说明

Filecloud 采用 [MIT License](../LICENSE) 发布。每个 CLI 正式归档均包含客户端 `filecloud-client`、服务端 `filecloud-server`、`README.md`、`DEPLOYMENT.md`、`CONTEXT.md`、完整 `docs/`、项目 `LICENSE`、`THIRD_PARTY_NOTICES.md`，以及该目标平台构建两个二进制实际使用的第三方 Go 模块原始许可文件。Windows 下两个命令分别带 `.exe` 后缀。

从采用此命名的版本起，原来脚本中的 `filecloud`（Windows 为 `filecloud.exe`）请改为 `filecloud-client`（`filecloud-client.exe`）。命令参数、已有绑定和状态目录不变，归档内不再附带旧文件名。

推送 `vMAJOR.MINOR.PATCH` 标签后，由一个发布流程收集以下文件，全部通过构建与测试后放在**同一个 GitHub Release**。下表中的 `<tag>` 含 `v` 前缀。

| 平台 | CLI 归档（含服务端和客户端） | 桌面安装包（含配套客户端） |
|---|---|---|
| Linux amd64 | `filecloud-<tag>-linux-amd64.tar.gz` | `filecloud-desktop-<tag>-linux-amd64.deb`、`filecloud-desktop-<tag>-linux-amd64.AppImage` |
| macOS arm64 | `filecloud-<tag>-darwin-arm64.tar.gz` | `filecloud-desktop-<tag>-darwin-arm64.dmg` |
| Windows amd64 | `filecloud-<tag>-windows-amd64.zip` | `filecloud-desktop-<tag>-windows-amd64.exe` |

同一份 `SHA256SUMS` 包含全部七个下载文件的校验值。桌面安装包的签名与试用说明见[桌面客户端](../desktop/README.md)。

## 支持范围

正式发布的客户端和服务端二进制支持以下平台；下列文件系统限制适用于客户端状态目录和工作目录：

- Linux：本地 ext4。
- macOS：具备所需原子 rename 和文件锁能力的本地 APFS。
- Windows：具备 reparse point、hard-link、persistent ACL 和 Unicode 能力的本地固定 NTFS 卷。

NFS、SMB、FAT、exFAT、Windows 网络映射盘、可移动 NTFS 卷，以及无法确认所需原子性和路径安全能力的文件系统不受支持。Filecloud 会在创建客户端状态或访问远端之前拒绝这些工作目录；不会降级其持久性或路径安全保证。

## 发布门禁

以下是当前源码构建正式 tag 归档时使用的门禁。`v0.3.0` 已提供两个命令入口；内嵌 Web 控制台及其资源冒烟测试是在该标签之后加入的。

1. Linux/ext4、macOS/APFS 和 Windows/NTFS 上的统一 1B 验收，包括历史浏览、安全恢复、故障收敛和固定跨平台对象向量。
2. 完整 1C 运维正确性测试和部署性能基线。
3. 每个平台的 `filecloud-client` 和 `filecloud-server` 均注入同一 tag、commit 和构建日期。
4. 双二进制冒烟测试只用 `filecloud-server` 执行初始化、用户创建和服务启动，只用 `filecloud-client` 执行登录、资料库创建、绑定和同步。
   同一冒烟流程先启用 Web 并从构建出的服务端读取登录页和内嵌 CSS、JS、SVG，再以默认关闭的方式完成既有 CLI 同步流程。页面资源为本仓库源码，随 Go 二进制编入，无外部字体、CDN、Node 或新增第三方前端许可材料。
5. 每个原生目标平台在双二进制构建和冒烟后，根据两个目标实际编译依赖的并集生成并打包第三方许可材料。
6. 同一 tag 的桌面 GUI 与配套 CLI 一起打包；桌面三端构建、Linux AppImage 内容检查和双设备测试通过后，统一发布作业才创建 Release。

本版本为命令拆分过渡期：`filecloud-client` 仍接受旧的服务端子命令，但该兼容入口不属于新部署和自动化契约；新调用必须使用 `filecloud-server`。门禁证明进程崩溃恢复，不声明物理断电、存储控制器缓存或硬件故障下的绝对持久性。
