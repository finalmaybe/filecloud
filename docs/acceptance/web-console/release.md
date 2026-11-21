# Web 控制台发布候选：兼容与原生验收（#69）

被测提交 **`7d8c9c8`**（`feat/web-console`）。三台原生主机使用同一份 `git archive` 源码，SHA-256：`57b961eb04d65d53b0d0421407a376e4708f8f12ece8e260318321a27ab2029b`。本记录是发布候选的验收结果，没有创建 tag 或发布正式版本。

## 兼容闭环

- 用旧服务端初始化用户、会话和资料库，通过旧 CLI 绑定并发布文件。原目录直接用新服务端启动，两次分别关闭和开启 Web。原 CLI 会话仍可用，Head 相同，`If-None-Match` 返回 304；旧 CLI 与新 Web 读取的固定提交、文件及大小一致。
- Web 建库 → 两台 CLI 绑定并发布 → Web 展开已证明的合并来源、检查快照 → CLI 执行页面给出的预览，精确确认 → 原路径内容恢复，Web 可见新的前向提交。测试文件名含中文、空格、单引号及 `&`。Windows 原生测试也检验了 PowerShell 参数的字面量传递。
- 管理员在线开户后用真实 CLI 与浏览器分别登录；确认重置后，两个旧会话与旧密码均失效，新密码可登录。移除管理员参数并重启后，既有会话访问账号管理为 403，自有资料库仍可用；重新授权并重启后管理入口恢复。
- [浏览器走查、实测截图与性能条件](browser.md)。Go 1.26.6 普通回归通过（1C 脚本的第一步）；`go vet ./...` 和认证、Web 包 `go test -race` 通过。

## 原生门禁与双二进制冒烟

| 平台 / 文件系统 | 独立 1B：`TestCrossPlatformAcceptanceMatrix` | 临时归档解包后的冒烟 |
|---|---|---|
| Linux amd64 / ext4 | 通过，285.385 s | `.tar.gz` 通过 |
| macOS arm64 / APFS | 通过，586.351 s | `.tar.gz` 通过 |
| Windows amd64 / 本地固定 NTFS | 通过，1279.046 s | `.zip` 通过 |

按[验收主机指南](../../agents/acceptance-hosts.md)连接目标系统，在各自本地磁盘执行 `FILECLOUD_RUN_1B=1 go test ./internal/clientapp -run '^TestCrossPlatformAcceptanceMatrix$' -count=1 -timeout=30m`。Linux 另独立执行 `./scripts/acceptance-1c.sh` 通过：10000 小文件、10 GiB 文件、默认 KDF、10 万目录项的采样分别约为 4.15、10.38、0.04、0.23 秒。

冒烟使用 Go 1.26.6、`CGO_ENABLED=0`，注入测试版本字符串 `v0.0.0`、候选提交及构建日期。三个目标均生成 `tools/notices` 许可材料；对解包二进制运行 `tools/release-smoke`，先验证开启 Web 时嵌入的登录页面、CSS、JS、SVG，再以默认关闭 Web 的方式完成登录、创建、绑定和同步。Windows 临时归档用系统 `tar.exe` 创建并解包 ZIP；一次独立的 Windows PowerShell `Compress-Archive` 演练停在进度输出，未作为成功证据。产品资源均为本地内容，未新增第三方前端库。

## 验收中另行跟踪的问题

**[#70：Linux/ext4 解绑临时文件身份断言间歇失败](https://github.com/finalmaybe/filecloud/issues/70)**。1B 与 1C 同期占用本机磁盘时，预检及候选各发生一次 `TestLibraryCheckoutPendingUnbindCleansOnlyRegisteredTemp/identity_mismatch` 失败。未改动客户端清理代码或测试断言，也未跳过该用例；按 CI 分离门禁的方式完整运行，原 main（287.162 s）与同一最终候选均通过。独立门禁结果与该发现分开记录；正式发布前仍需评估文件身份与内容保留风险。
