# Filecloud

Filecloud 是一个以资料库（Library）为同步单位的多设备文件同步系统。它由单节点 Go 服务端和跨平台 Go CLI 组成；服务端使用 SQLite 保存可变元数据，并在本地文件系统中保存按 SHA-256 寻址的不可变内容对象。

第一阶段已完成 Linux/ext4、macOS/APFS、Windows/固定 NTFS 三个平台的同步闭环和运维验收。后续版本增加了 CLI 历史浏览与安全恢复；当前源码版还提供可选的 Web 控制台。

> Filecloud 当前是同步产品，不是独立备份方案。服务端故障仍可能同时影响当前数据和历史版本；部署时应单独备份完整服务端数据目录。

## 已实现能力

- 管理员在服务端本地创建用户和重置密码；用户可以登录、注销并管理自己的资料库。
- 多台设备通过扫描、对象传输和 Head CAS 双向同步普通文件和目录。
- 同一路径发生并发修改时执行递归三方合并；无法无损合并的本地内容保留为冲突副本。
- 内容按固定上限分块并以 SHA-256 寻址，服务端保存完整的不可变提交历史。
- 客户端使用持久化发布状态和文件系统 journal，从上传、发布或 checkout 中断后继续收敛。
- CLI 可以浏览主线历史、检查合并来源，并通过“预览 + 精确确认”发布前向恢复提交。
- 提供定时轮询 `watch`、删除保护、健康检查、优雅停止、离线 GC 和完整性检查。
- 源码版服务端可[启用内嵌 Web 入口](./docs/operations/deployment-and-client.md#启用网页入口)，默认关闭。登录后可管理自己的资料库、预览文本、图片和 PDF、下载或恢复单个文件的历史版本，也可从版本列表删除记录。本机显式授权的管理员还可开户、重置密码和检查服务就绪状态。

## 支持范围

正式发布包支持以下原生平台和本地文件系统：

| 平台 | 架构 | 客户端状态目录和工作目录 |
|---|---|---|
| Linux | amd64 | 本地 ext4 |
| macOS | arm64 | 本地 APFS |
| Windows | amd64 | 本地固定 NTFS 卷 |

各平台支持声明以目标主机最近一次显式门禁记录为准。绑定时会检查文件系统能力；NFS、SMB、FAT/exFAT、Windows 网络映射盘、可移动 NTFS 卷，以及无法确认所需原子性和路径安全能力的文件系统会被拒绝。

正式发布包提供两个命令入口：`filecloud-server` 用于服务端初始化、运行和本地维护，`filecloud` 用于登录、资料库管理和同步。本版本的 `filecloud` 仍接受旧的服务端子命令以便迁移，但新部署和自动化必须使用 `filecloud-server`。以下能力暂不提供：

- Docker 镜像、桌面 GUI 和移动客户端
- WebDAV、虚拟盘、S3 或其他对象存储后端
- 多节点部署、在线 GC、PostgreSQL/MySQL 后端
- 共享资料库、成员权限、协作锁和公开注册
- 资料库删除、用户可配置容量配额、历史过期和已发布历史清理
- 端到端加密；服务端可以读取文件名、目录结构和内容
- 原生文件事件监听；`watch` 按设定间隔执行完整扫描

## 获取与构建

正式版本可从 [GitHub Releases](https://github.com/mingming-cn/filecloud/releases) 下载。每个归档都包含 `filecloud` 和 `filecloud-server` 两个二进制、项目许可证、部署说明和对应目标平台的第三方依赖许可材料。

从源码构建和测试请固定使用 Go 1.26.6。Go 1.27 会选择不同的 Unicode 表，无法通过文件名规范化和大小写折叠的协议测试。

```bash
git clone https://github.com/mingming-cn/filecloud.git
cd filecloud
export GOTOOLCHAIN=go1.26.6
go version
CGO_ENABLED=0 go build -trimpath -o filecloud ./cmd/filecloud
CGO_ENABLED=0 go build -trimpath -o filecloud-server ./cmd/filecloud-server
./filecloud version
./filecloud-server version
```

源码构建的版本号为 `dev`。正式发布包会显示 tag、commit 和构建日期。

## 本机快速体验

下面的流程适用于 Bash 或 zsh，需要 `curl`、`jq` 和 `uuidgen`。它会在回环地址启动服务，创建一个空资料库，绑定本地工作目录并完成第一次同步。已有账号的 Windows 用户可按 [PowerShell 客户端快速体验](./docs/operations/deployment-and-client.md#windows-客户端快速体验)完成首次同步。生产服务端见 [Linux 部署](./docs/operations/deployment-and-client.md#4-linux-生产部署)。

先构建二进制。在第一个终端初始化服务端、创建用户并启动服务：

```bash
DATA_DIR="$HOME/.local/share/filecloud-server"

./filecloud-server init --data-dir "$DATA_DIR"

read -r -s PASSWORD
printf '\n'
printf '%s\n' "$PASSWORD" |
  ./filecloud-server user add \
    --data-dir "$DATA_DIR" \
    --username alice \
    --password-stdin
unset PASSWORD

./filecloud-server serve \
  --data-dir "$DATA_DIR" \
  --listen 127.0.0.1:8080
```

在第二个终端检查服务状态，然后登录并创建资料库：

```bash
SERVER="http://127.0.0.1:8080"
curl -fsS "$SERVER/healthz"
curl -fsS "$SERVER/readyz"

CLIENT_DIR="$HOME/.local/share/filecloud-client"
WORKTREE="$HOME/Filecloud/Documents"
LIBRARY_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
DEVICE_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')

mkdir -p "$CLIENT_DIR" "$WORKTREE"

read -r -s PASSWORD
printf '\n'
LOGIN_JSON=$(
  printf '%s\n' "$PASSWORD" |
    ./filecloud login \
      --server "$SERVER" \
      --username alice \
      --device-name laptop \
      --password-stdin
)
unset PASSWORD
TOKEN=$(printf '%s' "$LOGIN_JSON" | jq -er '.Session.AccessToken')
unset LOGIN_JSON

printf '%s\n' "$TOKEN" |
  ./filecloud library create \
    --server "$SERVER" \
    --library-id "$LIBRARY_ID" \
    --name Documents \
    --token-stdin

printf '%s\n' "$TOKEN" |
  ./filecloud library bind \
    --client-dir "$CLIENT_DIR" \
    --server "$SERVER" \
    --library-id "$LIBRARY_ID" \
    --worktree "$WORKTREE" \
    --device-id "$DEVICE_ID" \
    --token-stdin
unset TOKEN
```

向工作目录写入文件并同步：

```bash
printf 'hello, filecloud\n' > "$WORKTREE/hello.txt"

./filecloud library sync \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE"
```

后续可以执行单次同步或持续轮询：

```bash
./filecloud library sync --client-dir "$CLIENT_DIR" --worktree "$WORKTREE"
./filecloud library watch --client-dir "$CLIENT_DIR" --worktree "$WORKTREE" --interval 30s
```

非回环服务地址必须使用 HTTPS。生产部署应让服务端只监听回环地址，再通过可信反向代理提供 TLS；不要把 Bearer token 写入命令行、Git 仓库或普通日志。

## 常用命令

| 用途 | 命令 |
|---|---|
| 初始化和启动服务 | `filecloud-server init`、`filecloud-server serve` |
| 管理用户 | `filecloud-server user add`、`filecloud-server user reset-password` |
| 管理会话 | `filecloud login`、`filecloud logout` |
| 管理资料库 | `filecloud library create/list/inspect/bind/unbind` |
| 查看本地绑定、同步基线和待处理操作（离线） | `filecloud library status`（源码版） |
| 同步 | `filecloud library sync/watch` |
| 浏览与恢复历史 | `filecloud library history list/inspect`、`filecloud library restore` |
| 服务端维护 | `filecloud-server integrity check`、`filecloud-server gc` |
| 查看构建信息 | `filecloud version`、`filecloud-server version` |

`integrity check` 和 `gc` 会取得数据目录独占锁，必须停止 `serve` 后离线运行。完整参数、首次导入、多设备绑定、冲突处理、删除确认、历史恢复和生产部署示例见[部署与客户端使用](./docs/operations/deployment-and-client.md)。

## 验证

在仓库根目录运行普通回归测试，后面的门禁沿用同一工具链：

```bash
export GOTOOLCHAIN=go1.26.6
go version
go test ./...
```

1B 跨平台门禁必须在对应平台的本地 ext4、APFS 或固定 NTFS checkout 中显式运行。交叉编译不能代替实机门禁：

```bash
FILECLOUD_RUN_1B=1 go test ./internal/clientapp \
  -run '^TestCrossPlatformAcceptanceMatrix$' \
  -count=1 -timeout=30m -v
```

PowerShell 使用同一入口：

```powershell
$env:GOTOOLCHAIN = 'go1.26.6'
$env:FILECLOUD_RUN_1B=1
go test ./internal/clientapp -run '^TestCrossPlatformAcceptanceMatrix$' -count=1 -timeout=30m -v
```

完整 1C 运维正确性门禁与部署性能基线：

```bash
./scripts/acceptance-1c.sh
```

1C 性能测试会创建 10000 个小文件，并完整读取一个 10 GiB 稀疏文件；普通回归测试不会运行这些场景。门禁覆盖定义范围内的进程崩溃恢复，不承诺物理断电、存储控制器缓存或硬件故障下的绝对持久性。测试分层和证明要求见[验收规范](./docs/design/acceptance-tests.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [部署与客户端使用](./docs/operations/deployment-and-client.md) | 安装、生产部署、HTTPS、绑定、同步、恢复、维护和故障排查 |
| [领域词汇](./CONTEXT.md) | Library、Worktree、Snapshot、Commit、Head 等统一术语 |
| [同步架构](./docs/design/architecture.md) | 内容对象、存储布局、同步状态机、合并、崩溃恢复和安全边界 |
| [HTTP API 契约](./docs/design/http-api.md) | 认证、资料库、对象、Head 和历史接口 |
| [历史浏览与安全恢复](./docs/design/history-and-safe-restore.md) | 主线历史、合并来源、恢复预览和确认语义 |
| [Web 控制台契约](./docs/design/web-console.md) | 源码版的浏览器会话、文件预览与版本操作，以及和 CLI 的分工 |
| [第一阶段实施计划](./docs/design/phase-1-plan.md) | 1A、1B、1C 的原始交付范围和完成标准 |
| [1C 部署性能基线](./docs/operations/performance-baseline.md) | 性能测量、资源上限和安全默认值 |
| [分发说明](./docs/distribution.md) | 平台归档、发布门禁和许可材料 |
| [架构决策](./docs/adr/) | 已接受的协议、对象模型、合并和恢复决策 |

背景调研见 [Seafile 同步调研](./docs/research/seafile-sync.md)、[macOS/APFS 文件系统原语](./docs/research/macos-apfs-primitives.md)和 [Windows/NTFS 文件系统原语](./docs/research/windows-ntfs-primitives.md)。

## 版本与许可

正式 `vMAJOR.MINOR.PATCH` tag 通过 1B/1C 门禁后，会生成 Linux amd64、macOS arm64 和 Windows amd64 归档。每个平台的 `filecloud-server` 与 `filecloud` 会共同执行初始化、登录、资料库创建、绑定和同步冒烟测试，并随附 SHA-256 校验文件。

项目采用 [MIT License](./LICENSE)。正式归档同时提供由目标平台实际编译依赖生成的第三方模块清单和原始许可文本。
