# Filecloud 服务端部署与客户端使用

本文对应当前实现。正式发布包提供 `filecloud-server` 服务端命令和 `filecloud-client` 客户端命令。本版本的 `filecloud-client` 仍接受旧的服务端子命令以便迁移，但新部署、systemd 配置和自动化必须使用 `filecloud-server`。源码版还包含可选的同源 Web 控制台，与 CLI 共用账号。同一 tag 的[桌面客户端安装包](../../desktop/README.md)与 CLI 归档并列发布。源码版可按库启用 WebDAV。项目暂不提供 Docker 镜像或移动客户端。

## 1. 支持范围

正式发布包支持：

- Linux amd64，本地 ext4。
- macOS arm64，本地 APFS。
- Windows amd64，本地固定 NTFS 卷。

客户端状态目录和同步工作目录必须位于受支持的本地文件系统。NFS、SMB、FAT/exFAT、Windows 网络映射盘、可移动 NTFS 卷不受支持。程序会在绑定阶段检查文件系统，不会降级运行。

生产环境还应满足以下条件：

- 服务端数据目录有足够空间；Filecloud 会在剩余空间低于总容量 5% 或 1 GiB 时停止接收新对象，两者取较大值。
- 公网流量通过可信反向代理使用 HTTPS。服务端自身只提供 HTTP。
- 服务端数据目录、客户端状态目录和工作目录不要相互嵌套。
- 同步期间避免让其他程序持续写入工作目录。

## 2. 获取 `filecloud-client`

正式版本可从项目的 [GitHub Releases](https://github.com/mingming-cn/filecloud/releases) 下载。归档中包含二进制、项目许可证、第三方模块清单和许可证文本。

从源码构建时使用 Go 1.27（推荐 1.27.4），客户端和服务端均采用 Unicode 17.0 名称规则：

```bash
git clone https://github.com/mingming-cn/filecloud.git
cd filecloud
export GOTOOLCHAIN=go1.27.4
go version
CGO_ENABLED=0 go build -trimpath -o filecloud-client ./cmd/filecloud-client
CGO_ENABLED=0 go build -trimpath -o filecloud-server ./cmd/filecloud-server
./filecloud-client version
./filecloud-server version
```

安装到系统路径：

```bash
sudo install -m 0755 ./filecloud-client /usr/local/bin/filecloud-client
sudo install -m 0755 ./filecloud-server /usr/local/bin/filecloud-server
```

开发构建的版本信息会显示为 `dev`。正式发布包会显示 tag、commit 和构建日期。

## 3. 本机快速启动

下面的命令适合在当前用户下试用。数据目录必须先初始化，不能创建一个空目录后直接运行 `serve`。

```bash
DATA_DIR="$HOME/Documents/filecloud/data"

./filecloud-server init --data-dir "$DATA_DIR"
```

初始化会创建：

```text
<data-dir>/
  .filecloud.lock
  metadata.db
  objects/
  tmp/
```

然后创建用户。zsh 和 Bash 的 `read` 参数不同，请按当前 shell 选择命令。

zsh：

```zsh
read -rs 'PASSWORD?Password: '
printf '\n'
printf '%s\n' "$PASSWORD" |
  ./filecloud-server user add \
    --data-dir "$DATA_DIR" \
    --username ming \
    --password-stdin
unset PASSWORD
```

Bash：

```bash
read -rsp "Password: " PASSWORD
printf '\n'
printf '%s\n' "$PASSWORD" |
  ./filecloud-server user add \
    --data-dir "$DATA_DIR" \
    --username ming \
    --password-stdin
unset PASSWORD
```

启动服务：

```bash
./filecloud-server serve \
  --data-dir "$DATA_DIR" \
  --listen 127.0.0.1:8080
```

成功后会输出：

```text
listening on 127.0.0.1:8080
```

在另一个终端检查服务：

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

正常响应为：

```json
{"RetCode":0,"Message":"success"}
```

`healthz` 只检查 HTTP 进程是否存活。`readyz` 还会检查数据库、对象目录写入能力和数据目录锁。

### 启用网页入口

Web 控制台和 WebDAV 网络盘共用一个公开地址。要在本机启用网页入口（WebDAV 仍默认关闭），请用以下命令**替换**上面的 `serve` 命令，然后打开 `http://127.0.0.1:8080/`：

```bash
./filecloud-server serve --data-dir "$DATA_DIR" --site-url http://127.0.0.1:8080
```

`--site-url` 填浏览器和网络盘客户端使用的访问地址（协议、主机及可选端口，不含路径、尾斜杠、账号信息或查询参数）。启用 Web 时监听回环地址；只有回环来源可使用 HTTP。页面、样式与图像都在二进制内，不用部署静态目录。浏览器登录使用独立的 HttpOnly、SameSite 会话 cookie；HTTPS 配置还会设置 Secure。资料库和文件列表需要 JavaScript：`/v1/libraries` 用于列库，`PUT /v1/libraries/{id}` 用于建库，`GET /v1/libraries/{id}/files` 用于读取目录；反向代理需将整个公开 origin 转发到同一服务。会话最长 30 天，退出只撤销本次登录。移除 `--site-url` 和 Web 管理员参数后重启，即可同时关闭网页和网络盘。

**管理员授权**：先在停服的主机上用 `user add` 创建第一个账号。用 `--site-url` 启用网页后，按需为每个管理员重复指定 `--web-admin-user ACCOUNT_NAME`。服务端按规范用户名解析已有账号，配置不存在的账号将拒绝启动；默认没有 Web 管理员。移除某个账号参数并重启后，该账号旧的浏览器会话随即失去管理权限，自己的资料库仍可访问。保留本机停服管理命令作为救援入口。

网页上的日期和时间按浏览器当前时区显示；将鼠标停在时间上可查看时区名称。

**日常操作**：未登录时，打开 `/` 会直接进入登录页；登录后进入「我的资料库」。「大小」列统计各库当前发布版本中的全部文件，不含历史版本和回收站；尚未发布的库显示 `0 B`。点击标题旁的「创建资料库」，在弹窗输入名称并确认。点击资料库所在行的「… → 详情」，可在弹窗中查看资料库信息，点击复制图标即可复制资料库 ID；已发布的资料库还可复制当前 Head。按 Esc 或点「关闭」回到当前列表。点击资料库名称则直接打开当前发布版本的文件列表。目录可以逐级打开；接近文件列表底部时，会在原列表追加下一批目录项。名称筛选只作用于已加载的条目，清除筛选后继续自动加载。点击文件名会在当前列表上打开查看器。用底部按钮或键盘 ← / → 翻看打开时已加载、符合筛选的文件；点右上角「关闭预览」、按 Esc 或用浏览器返回键，回到原来的筛选和滚动位置。要单独打开或分享固定版本的链接，可用浏览器的新标签页功能；访问仍需登录资料库所有者账号。

**预览文件**：文本、Markdown、日志、CSV、配置和常见源码按 UTF-8 原文显示（最多前 256 KiB）；HTML 和 SVG 只显示源码。JPEG、PNG、WebP 和 GIF 默认整张适应窗口。可用滚轮或「＋ / −」逐级缩放，也可点「原始尺寸」按原始大小查看。放大后可拖动图片查看各处，点「适应窗口」回到全图；触屏在全图状态下左右滑动可切换文件。切换文件或重新打开预览时会恢复全图。PDF 使用浏览器内置阅读器。Word（`.docx`）按正文段落显示；Excel（`.xlsx`）显示前 8 张可见工作表，每张最多 100 行、26 列，公式显示文件中保存的结果。点击表名即可展开或收起。两种 Office 预览均为只读，文件不发送给第三方。单个文件最多 12 MiB，预览文字最多 256 KiB。要查看完整排版、图表或旧版 `.doc`、`.xls`，点「下载原文件」在本地打开。

**查看历史**：点击文件旁的「… → 历史版本」可查看这个文件的版本记录。桌面在列表右侧打开版本栏，窄屏切换为单栏；点「关闭」或在栏内按 Esc 回到原列表。顶部显示打开列表时的文件版本，下方按从新到旧的顺序列出该路径的变更。点击版本名称或时间，可就地展开修改时间、内容标识和版本标识。点「加载更早的记录」继续查看。

**操作文件版本**：点版本行右侧的「…」打开操作菜单。文件的当前版本只提供下载；只有更早的版本才能恢复或删除。选「下载版本」直接按原文件名保存；选「恢复版本」并核对弹窗中的路径和时间后确认，会把所选文件的内容和修改时间发布为新版本，其他文件保持不变；绑定设备在下次同步时收到变更。如果内容和修改时间都已相同，页面提示「文件已与所选版本相同」，不创建新提交。选「删除版本」并确认，该记录进入资料库回收站，当前文件不变。侧栏会在原位置提示「版本已删除」，无需重新打开文件列表。打开列表后如果有设备发布了新版本，请重新打开资料库，核对后再操作。

**回收站**：文件或目录旁选「… → 删除」，在「确认删除」弹窗核对路径后点「确认」，条目会移入回收站；设备同步发布的删除也会出现在同一回收站。回收站按行显示名称、原位置、删除时间和到期时间，可按名称或原路径筛选本页。点击名称或从行内「…」选择恢复，在弹窗中核对后确认。整个目录只显示一条记录，包括子目录和空目录。「恢复整个目录」或「恢复文件」会向前发布，其他设备下次同步收到。恢复路径留空表示原路径，已占用时须改名或取消，不会覆盖或合并。随条目保留删除时可见的文件版本，先前已单独移入回收站的版本维持自己的到期日。「放回版本列表」不会替换当前文件。同步协议按路径识别删除；移动或改名后，原路径也可能出现在回收站。

**保留设置**：创建资料库时，两项留空分别跟随服务端 `serve --trash-days`（初始默认 30）和 `--history-days`（初始默认 60）。全局设置保存在服务端，重启时省略参数会沿用上次设置。管理员缩短全局历史期时，若有跟随默认的资料库，首次启动会输出预计影响；核对后按提示添加 `--confirm-history-preview` 再启动。CLI 创建可用 `--trash-days`、`--history-days` 分别覆盖。进入资料库侧栏「保留设置」可以修改或恢复跟随默认；缩短历史保留期时先核对预计过期的快照和可释放的内容块字节，再确认。回收站倒计时从服务端确认删除开始，修改设置不追溯已有条目。历史快照从服务端发布时算，当前快照不受期限限制。升级前的资料库保持永久历史，须所有者主动取消「永久保留历史」；旧提交没有服务端发布时间，从升级时起计时。升级前已隐藏的版本记录不会凭空获得新的删除日期。

**管理与维护**：管理员从「头像 → 管理设置」进入「服务管理」。「账号管理」先按用户名列出账号、角色和创建日期，可按用户名筛选。点击「创建账号」在线开户，或点击目标账号的「… → 重置密码」，在弹窗中核对账号、输入新密码并确认；完成后仍停留在原列表。确认重置会撤销目标用户所有 Web/CLI 会话和 WebDAV 应用密码，不赋予管理员直接读取其他资料库的权限。删除普通账号时，从该行的「… → 删除账号」核对账号名和资料库数量后确认。账号、全部资料库（含服务端文件、历史和回收站）、登录会话及应用密码一并删除，设备上已有的本地副本保留。管理员账号不能通过网页删除。如磁盘清理中断，可按下文的离线 GC 流程清理超过宽限期的未引用对象。「服务概览 → 状态」展示运行环境、Go 堆内存和数据盘可用空间的当前快照；「重新检查」单独刷新进程存活和存储就绪状态。「数据库」可查看 SQLite 版本与连接池用量，「构建信息」可查看版本、提交和构建时间。日常 GC 在服务进程内自动执行。手动运行 `gc`、完整性检查，以及备份或恢复整个数据目录时，仍须停服。

**共享读取并发**：管理员在「头像 → 管理设置 → 文件访问」可调整服务器全局并发上限和每账号并发上限，初始值分别为 50 和 20。两项各接受 1–1000 的整数；每个账号同时受全局上限约束。CLI 历史查询、文件版本查询、目录浏览、文件预览和 WebDAV 目录列举等操作共享这些读取名额。无名额时拒绝新请求（HTTP 429），不排队。保存后立即以新上限接纳请求，不中断已开始的请求；设置在重启后保留，不改变同步对象传输或 Head 校验的并发限制。

主机也可显式传入 `serve --history-global-capacity N`、`--history-user-capacity N`（仍接受任意正整数）。每个参数只覆盖本次启动的对应值，不会写入数据库；未传的值使用已保存设置。网页保存会立即替换两项生效值并将它们持久化；下次启动若再次传入参数，该字段仍由参数覆盖。

### 启用 WebDAV 网络盘

```bash
./filecloud-server serve --data-dir "$DATA_DIR" --site-url https://cloud.example.com --web-admin-user ACCOUNT_NAME
```

将 `ACCOUNT_NAME` 换为已有管理员账号。用该账号登录，进入右上角「头像 → 管理设置 → 文件访问」，开启「WebDAV 网络盘」。新安装和从旧版升级时均默认关闭；更改对所有资料库的新请求立即生效，重启后保留。关闭不会删除已有应用密码，重新开启后仍可使用尚未过期或撤销的凭据。

`--site-url` 是 Web 和 WebDAV 共用的访问地址。`--listen` 仍独立控制本机监听地址，默认为 `127.0.0.1:8080`。只在本机使用时，可将示例中的公开地址换成 `http://127.0.0.1:8080`。

在 Web 打开资料库，选择侧栏「应用密码」，点击标题右侧的「创建应用密码」，在弹窗中填写凭据名称并确认。复制挂载地址，在网络盘登录框填写页面给出的**挂载用户名和应用密码**，不要填账号主密码。密码只显示一次，有效期一年。每个凭据只访问一个库，可以多台设备共用；若要分别撤销，请分别签发。从列表的「… → 查看」可查看挂载信息；选择「删除」并确认会撤销该凭据、释放写锁并将它从列表移除。退出网页不会中断网络盘，重置账号密码会撤销所有应用密码。

生产环境用 HTTPS 代理保留 Host、Authorization、Destination、If、Lock-Token、Depth、Range 及全部 DAV 方法，不记录含凭据的请求头。下方 nginx 示例的 `40m` 是同步对象的限制；需要上传更大的单文件时，为 `/dav/` 配置相应的 `client_max_body_size`，保留流式转发并按传输时长设置超时。

发现 `423` 时先让持锁的编辑器保存并关闭；`412` 表示版本已变，先另存草稿再重新打开；`428` 表示客户端没有提供覆盖所需的 ETag 或写锁。锁冲突时原生客户端保留待发布修改，解锁后重跑 `library sync`。误删到本库「回收站」恢复；覆盖错了去文件「历史版本」。具体客户端和测试环境见[WebDAV 验收记录](../acceptance/webdav-office.md)。

## 4. Linux 生产部署

以下示例使用：

```text
服务端程序：/usr/local/bin/filecloud-server
客户端程序：/usr/local/bin/filecloud-client
数据：/var/lib/filecloud
监听：127.0.0.1:8080
用户：filecloud
```

创建系统用户和数据目录：

```bash
sudo useradd \
  --system \
  --home-dir /var/lib/filecloud \
  --shell /usr/sbin/nologin \
  filecloud

sudo install -d -o filecloud -g filecloud -m 0700 /var/lib/filecloud
sudo -u filecloud /usr/local/bin/filecloud-server init --data-dir /var/lib/filecloud
```

创建第一个用户。`user add` 和 `user reset-password` 会取得数据目录管理锁，应在服务停止时执行。

zsh：

```zsh
read -rs 'PASSWORD?Password: '
printf '\n'
printf '%s\n' "$PASSWORD" |
  sudo -u filecloud /usr/local/bin/filecloud-server user add \
    --data-dir /var/lib/filecloud \
    --username alice \
    --password-stdin
unset PASSWORD
```

Bash：

```bash
read -rsp "Password: " PASSWORD
printf '\n'
printf '%s\n' "$PASSWORD" |
  sudo -u filecloud /usr/local/bin/filecloud-server user add \
    --data-dir /var/lib/filecloud \
    --username alice \
    --password-stdin
unset PASSWORD
```

创建 `/etc/systemd/system/filecloud.service`：

```ini
[Unit]
Description=Filecloud server
After=network.target

[Service]
Type=simple
User=filecloud
Group=filecloud
UMask=0077
ExecStart=/usr/local/bin/filecloud-server serve --data-dir /var/lib/filecloud --listen 127.0.0.1:8080
Restart=on-failure
RestartSec=3s
TimeoutStopSec=10s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/filecloud

[Install]
WantedBy=multi-user.target
```

启动并查看日志：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now filecloud
sudo systemctl status filecloud
sudo journalctl -u filecloud -f
```

服务收到 `SIGTERM` 后会停止接收新请求，并在最多 5 秒内等待正在处理的请求结束。

### 新增用户或重置密码

先停止服务：

```bash
sudo systemctl stop filecloud
```

新增用户使用前面的 `user add`。重置密码前，zsh 运行 `read -rs 'PASSWORD?Password: '`，Bash 运行 `read -rsp "Password: " PASSWORD`，然后执行：

```bash
printf '\n'
printf '%s\n' "$PASSWORD" |
  sudo -u filecloud /usr/local/bin/filecloud-server user reset-password \
    --data-dir /var/lib/filecloud \
    --username alice \
    --password-stdin
unset PASSWORD
```

操作完成后重新启动：

```bash
sudo systemctl start filecloud
```

## 5. 配置 HTTPS 反向代理

客户端仅允许两类服务地址：

- 回环地址可以使用 HTTP，例如 `http://127.0.0.1:8080`。
- 非回环地址必须使用 HTTPS，例如 `https://cloud.example.com`。

因此不要把 `filecloud-server serve --listen 0.0.0.0:8080` 直接暴露到公网。若启用 Web 和 WebDAV，请为回环监听的 `serve` 加上 `--site-url https://cloud.example.com`，让代理保留浏览器的 Host（含非默认端口）。服务端不从 `X-Forwarded-Host` 推断公开地址。登录并发限制也按直连 IP 计算：使用下面的配置时，所有经过该代理的登录共享回环 IP 的并发槽（默认 1），添加 `X-Forwarded-For` 不会改变此行为。

```nginx
server {
    listen 443 ssl;
    server_name cloud.example.com;

    ssl_certificate     /etc/letsencrypt/live/cloud.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/cloud.example.com/privkey.pem;

    # Directory 元数据对象最大为 32 MiB，给协议头和代理留出余量。
    client_max_body_size 40m;

    # 客户端请求预算为 3 分钟，Head 校验的服务端预算为 2 分钟。
    proxy_connect_timeout 10s;
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;
    proxy_request_buffering off;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
    }
}
```

配置完成后检查公网地址：

```bash
curl -fsS https://cloud.example.com/healthz
curl -fsS https://cloud.example.com/readyz
```

客户端使用系统证书池验证 TLS。自签名证书必须先加入客户端操作系统的信任存储。

## 6. 客户端登录

以下示例使用 `jq` 从登录响应中读取 token：

```bash
SERVER="https://cloud.example.com"
```

zsh：

```zsh
read -rs 'PASSWORD?Password: '
printf '\n'
LOGIN_JSON=$(
  printf '%s\n' "$PASSWORD" |
    filecloud-client login \
      --server "$SERVER" \
      --username alice \
      --device-name laptop \
      --password-stdin
)
unset PASSWORD

TOKEN=$(printf '%s' "$LOGIN_JSON" | jq -er '.Session.AccessToken')
unset LOGIN_JSON
```

Bash 只需将第一行换成：

```bash
read -rsp "Password: " PASSWORD
```

登录 token 的有效期是 30 天。服务端数据库只保存 token 的 SHA-256 摘要，但客户端绑定后会把原始 token 保存在权限受限的客户端数据库中。不要把 token 写入命令行参数、Git 仓库或普通日志。

本机试用时，服务地址可以改为：

```bash
SERVER="http://127.0.0.1:8080"
```

## 7. 创建和查看资料库

每个资料库和每台设备都需要一个规范的小写 UUID。

Linux/macOS：

```bash
LIBRARY_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
DEVICE_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
```

Windows 用户可直接按 [PowerShell 客户端快速体验](#windows-客户端快速体验)操作。

创建资料库：

```bash
printf '%s\n' "$TOKEN" |
  filecloud-client library create \
    --server "$SERVER" \
    --library-id "$LIBRARY_ID" \
    --name "Documents" \
    --token-stdin
```

列出当前用户的资料库：

```bash
printf '%s\n' "$TOKEN" |
  filecloud-client library list \
    --server "$SERVER" \
    --token-stdin
```

查看指定资料库：

```bash
printf '%s\n' "$TOKEN" |
  filecloud-client library inspect \
    --server "$SERVER" \
    --library-id "$LIBRARY_ID" \
    --token-stdin
```

当前 CLI 没有删除资料库的子命令。

## 8. 绑定工作目录

`client-dir` 保存 `client.db`、绑定关系、同步基线和崩溃恢复日志。`worktree` 是用户实际编辑文件的目录。两者必须分开，`client-dir` 不能位于 `worktree` 内。

```bash
CLIENT_DIR="$HOME/.local/share/filecloud"
WORKTREE="$HOME/Filecloud/Documents"

mkdir -p "$CLIENT_DIR" "$WORKTREE"
```

### 空工作目录

远端为空时，首次绑定会创建初始空提交。远端已有内容时，首次绑定会下载远端内容。

```bash
printf '%s\n' "$TOKEN" |
  filecloud-client library bind \
    --client-dir "$CLIENT_DIR" \
    --server "$SERVER" \
    --library-id "$LIBRARY_ID" \
    --worktree "$WORKTREE" \
    --device-id "$DEVICE_ID" \
    --token-stdin
```

### 导入已有本地文件

只有远端资料库从未发布过提交（Head 为空）时才能首次导入非空工作目录，必须显式使用 `--import-local`：

```bash
printf '%s\n' "$TOKEN" |
  filecloud-client library bind \
    --client-dir "$CLIENT_DIR" \
    --server "$SERVER" \
    --library-id "$LIBRARY_ID" \
    --worktree "$WORKTREE" \
    --device-id "$DEVICE_ID" \
    --token-stdin \
    --import-local
```

### 和已有文件夹同步

解除同步后，要使用原文件夹，或要将另一个非空目录和已有资料库合并：先在上述 `library bind` 命令中用 `--preview-merge` 替换 `--import-local`，核对返回的上传（`Upload`）、下载（`Download`）、冲突文件（`Conflict`）清单。每类最多展示 50 个路径，`Count` 是完整数量。这一步不建立绑定，也不改动文件或发布版本。

确认后用 `--merge-existing CANDIDATE` 替换 `--preview-merge`（`CANDIDATE` 取自本次预览的 `Candidate`），再运行 `library sync`；桌面端会自动开始这轮同步。确认前本地文件或远端版本变化时要重新预览。两端快照完全一致时不重新传输文件，也不发布新版本。文件只在一边存在时保留；同路径内容不同时，服务端文件保留原名，本地内容保留为冲突文件。仅大小写不同的路径需先改名。差异合并使用正常同步的主线历史核对范围；找不到可验证的空快照时，仍可保留原目录并选择新的空目录下载。

绑定成功后，后续 `sync` 和 `watch` 从 `client.db` 读取服务地址、设备 ID 和 token，不再要求输入 token。

### 刷新绑定凭据

用同一账号重新登录，将新 token 通过标准输入交给原来的 `bind` 命令，保持服务地址、资料库、设备 ID 和两个目录不变。刷新要求同时满足两个条件：工作目录的内容和修改时间与同步基线一致，且服务端 Head 仍是这条基线。其他设备发布了提交时，即使本机没有编辑，也要先同步。

旧 token 仍有效时，先用原绑定运行 `library sync`，再刷新。若旧 token 已失效且两端尚未收敛，先停止 `watch`，保留原工作目录和客户端状态目录。用新的状态目录、空工作目录和设备 ID 绑定同一资料库，下载当前版本，再对照原目录逐项合入要保留的本地修改并同步；核对完毕前保留原目录。

### Windows 客户端快速体验

前提：服务已启动、管理员已为你开户，`filecloud-client.exe` 已加入 `PATH`。在 PowerShell 7.4 或更高版本中运行下面的代码，将服务地址和用户名换成自己的。两个目录应位于本地固定 NTFS 卷；示例使用新的工作目录，避免覆盖现有文件。

```powershell
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true
$Server = 'https://cloud.example.com'
$Username = 'alice'
$LibraryId = [guid]::NewGuid().ToString()
$DeviceId = [guid]::NewGuid().ToString()
$ClientDir = Join-Path $env:LOCALAPPDATA 'Filecloud\client'
$Worktree = Join-Path $HOME "Filecloud\$LibraryId"
New-Item -ItemType Directory -Force -Path $ClientDir | Out-Null
New-Item -ItemType Directory -Path $Worktree | Out-Null

try {
    $Login = (Read-Host '密码' -MaskInput) | filecloud-client.exe login --server $Server --username $Username --device-name windows-pc --password-stdin | ConvertFrom-Json
    $Token = $Login.Session.AccessToken
    $Token | filecloud-client.exe library create --server $Server --library-id $LibraryId --name "Documents-$($LibraryId.Substring(0,8))" --token-stdin
    $Token | filecloud-client.exe library bind --client-dir $ClientDir --server $Server --library-id $LibraryId --worktree $Worktree --device-id $DeviceId --token-stdin
} finally {
    Remove-Variable Login, Token -ErrorAction SilentlyContinue
}

Set-Content -LiteralPath (Join-Path $Worktree 'hello.txt') -Value 'hello, filecloud' -Encoding utf8
filecloud-client.exe library sync --client-dir $ClientDir --worktree $Worktree
```

## 9. 同步

查看这个工作目录最近确认到哪个版本、是否有待处理操作（源码版）：

```bash
filecloud-client library status --client-dir "$CLIENT_DIR" --worktree "$WORKTREE"
```

命令离线读取本地绑定，输出 JSON。`LibraryId` 是资料库 ID，`SyncBase` 是上次确认的本地同步基线。它不扫描工作目录，也不查询服务端 Head。`Pending` 取值如下：

| `Pending` | 含义和下一步 |
|---|---|
| `none` | 没有记录在案的待处理操作。本地文件或远端仍可能有新变化。 |
| `sync` / `restore` / `checkout` | 分别有同步发布、已确认的恢复或文件落盘尚待完成。运行普通 `sync` 继续。 |
| `delete-confirmation` | 删除保护等待确认。先核对 `Deletion.Deleted`（待删除数）和 `Deletion.Tracked`（跟踪总数），确定后再按提示运行 `sync --confirm-delete`。 |
| `restore-confirmation` | 历史恢复预览等待确认。确定要应用预览时，按提示运行 `restore --confirm`。 |

有待处理操作时，`NextCommand` 给出完整命令的参数数组，带空格的目录也是一个参数。查询本身不会执行该命令、更新基线或迁移数据库。SQLite 使用只读连接；读取 WAL 模式的数据库时，SQLite 仍可能在客户端状态目录中创建或更新 `client.db-wal` / `client.db-shm` 辅助文件。

执行一轮同步：

```bash
filecloud-client library sync \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE"
```

没有变化时输出：

```text
library already synchronized
```

持续轮询，例如每 30 秒同步一次：

```bash
filecloud-client library watch \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --interval 30s
```

`watch` 使用定时轮询，不依赖文件系统事件。不要同时对同一工作目录运行多个 `sync` 或 `watch`，绑定锁会拒绝并发操作。

### 多设备

第二台设备使用同一个 `SERVER` 和 `LIBRARY_ID`，但必须生成新的 `DEVICE_ID`，并使用独立的 `CLIENT_DIR` 和 `WORKTREE`。先登录，再绑定一个空工作目录即可下载现有资料库。

双方同时修改同一路径时，远端版本保留原文件名，本地版本会生成带 `Filecloud conflict` 标记的冲突副本。冲突副本也会进入资料库并同步到其他设备。

### 大量删除保护

候选提交删除超过 100 个已跟踪路径，或者删除比例达到 10% 时，同步会停止并输出一个 12 位候选前缀。确认工作目录内容无误后，按错误消息给出的前缀重新执行：

```bash
filecloud-client library sync \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --confirm-delete 0123456789ab
```

不能使用更短前缀、完整 commit ID 或其他候选的前缀。

## 10. 历史浏览与安全恢复

历史命令从绑定读取服务地址、资料库和凭据，只读取已发布历史，不扫描或修改工作目录，也不推进同步基线。列出主线历史：

```bash
filecloud-client library history list \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --page-size 100
```

输出包含完整 Commit ID。存在下一页时，末行会给出 `next_page_token`；将该值原样传回：

```bash
filecloud-client library history list \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --page-size 100 \
  --page-token "$PAGE_TOKEN"
```

默认只列出曾经成为 Head 的主线提交。高级排查可以增加 `--include-merged`，在所属主线提交下展开已发布且由服务端证明角色的 `merge-source`：

```bash
filecloud-client library history list \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --include-merged
```

检查提交元数据时必须使用完整的 64 位小写 Commit ID：

```bash
filecloud-client library history inspect \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --commit "$COMMIT_ID"
```

增加 `--path` 可以检查历史文件或目录。文件只显示 File ID、大小、mtime 和 block 数，不下载内容；目录按规范名称排序并支持分页。`.` 表示历史根目录：

```bash
filecloud-client library history inspect \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --commit "$COMMIT_ID" \
  --path . \
  --page-size 100
```

恢复前先运行一次 `sync`，确保工作目录、同步基线和服务端 Head 已收敛。恢复预览选择一个历史提交和其中存在的原路径，目标固定为相同路径：

```bash
filecloud-client library restore \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --commit "$COMMIT_ID" \
  --path docs/report.txt
```

目录恢复执行 overlay：历史同名项覆盖当前项，当前独有项保留；`.` 对整个历史根执行相同规则。预览会持久化固定候选，显示创建、更新、类型替换、移除后代和保留当前独有路径的统计，并最多列出 100 条变化路径。预览阶段不会上传对象、改变 Head 或修改工作目录；no-op 不创建候选。

核对预览后，使用输出中的 12 位小写候选前缀精确确认：

```bash
filecloud-client library restore \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE" \
  --confirm 0123456789ab
```

恢复通过一个新的前向 Commit 发布，不会把 Head 指回旧版本。预览后若工作目录或 Head 已变化，确认会失效，必须先同步并重新预览；客户端不会自动把旧确认意图应用到新 Head。历史中不存在的源路径会返回 NotFound，不表示删除当前路径。

中断后按待处理状态继续，不必重新生成候选：尚待确认时，使用预览中的 12 位前缀执行 `library restore --confirm`；已确认但上传、发布结果核对或 checkout 尚未完成时，运行普通 `filecloud-client library sync --client-dir "$CLIENT_DIR" --worktree "$WORKTREE"`。已有待处理 checkout 时，再执行 `restore --confirm` 会提示先同步。源码版可用[离线状态命令](#9-同步)查看 `Pending` 和 `NextCommand`。

### 桌面客户端使用的本机接口（源码版）

- `filecloud-client library bindings --client-dir PATH --server URL --user-id UUID`：离线列出该服务、该账号在指定状态目录中的绑定；返回 `LibraryId`、`DeviceId`、`Worktree` 和 `Phase`（`bound` / `binding`）。未创建数据库时返回 `[]`。不会返回 token。
- `library status` 在等待确认时额外给出 `Confirmation.Kind`、`Confirmation.CandidatePrefix`。核对时应保存当时所见候选及计数，不能用刚刷新的前缀代替用户确认。
- `library sync --output=jsonl` 保留既有目录及确认参数，每行一条 JSON。实际完成初始扫描时发 `scan_completed`；终态 `result` 携带 `outcome`（`ok`、`no_change`、`needs_confirmation`、`failed`）和可选 `code`。进程中断且没有终态时，应重新读 `status`。
- `library history list --output=json` 返回主线响应（含 `Commits`、`NextPageToken`），不与 `--include-merged` 同用。`library unbind --if-idle` 只在取得锁且没有待处理状态时解绑。
- `filecloud-client --machine` 前缀用于桌面的一次性查询/操作（`check-server`、`login`、`logout`、`version`，以及 `library` 的 `list/create/inspect/check-directory/bindings/bind/unbind/status/restore/history list`）。`check-server --server URL` 匿名检查 `/healthz`，不提交账号；`library check-directory --worktree PATH` 只读检查文件系统、目录读写权限和本层项目数，不建立绑定。成功时保持子命令原响应，失败时 stdout 返回脱敏 `result` 错误对象并以非零状态退出；不能套用到 `watch`。登录成功的响应仍含敏感凭据，不应写进诊断日志。

## 11. 解除绑定和注销

解除绑定会删除客户端绑定状态，不会删除工作目录中的普通文件，也不会删除服务端资料库：

```bash
filecloud-client library unbind \
  --client-dir "$CLIENT_DIR" \
  --worktree "$WORKTREE"
```

如果 checkout 曾中断，解绑会检查登记的临时文件。遇到 `checkout temporary file content` 错误时，客户端会保留文件和待处理状态。请先备份工作目录及客户端状态；若是本地缓存损坏，先从可靠备份恢复缓存。若临时文件含有需要保留的编辑，把它移到工作目录外妥善保存，再重试解绑。不要手工清空 `client.db`。

注销会撤销当前 token：

```bash
printf '%s\n' "$TOKEN" |
  filecloud-client logout \
    --server "$SERVER" \
    --token-stdin
unset TOKEN
```

已经保存在绑定数据库中的同一 token 也会失效。之后需要重新登录并刷新绑定凭据。

## 12. 服务端维护

### 在线自动 GC（默认）

`filecloud-server serve` 默认在**服务器本地时间每天 03:00** 运行一次垃圾回收，不停服，也不需要 cron 或 systemd timer。要改到 04:30，在现有 `serve` 启动参数后加 `--gc-at=04:30`；`--gc-at=off` 关闭自动任务。修改启动参数后重启服务生效。服务在计划时间未运行时，重启后等下一个计划时间。

GC 逐库工作。遇到正在传输对象的设备、发布版本、恢复回收站条目等操作时，让前台先完成；当前轮继续处理其他库，每 5 分钟重试尚忙的库。传输结束后给同库预留 5 分钟，让一次同步的多次请求有时间接续。繁忙的资料库可能较晚释放空间。删除按小批执行；进程重启后，下一轮会根据当前引用重新扫描，不沿用旧删除清单。

查看服务日志中 `command=online_gc` 的结构化记录：`gc` 字段给出完成、延后和失败的资料库数，以及本次实际删除的对象数和字节数。`deferred_libraries` 表示正常让路。若持续出现 `failed_libraries`，可停服执行下方完整性检查。

### 手动离线维护

命令行 `gc`（包括 `--dry-run`）和 `integrity check` 需要独占数据目录。先停止服务：

```bash
sudo systemctl stop filecloud
```

完整性检查：

```bash
sudo -u filecloud /usr/local/bin/filecloud-server integrity check \
  --data-dir /var/lib/filecloud
```

预览垃圾回收：

```bash
sudo -u filecloud /usr/local/bin/filecloud-server gc \
  --data-dir /var/lib/filecloud \
  --dry-run
```

执行垃圾回收：

```bash
sudo -u filecloud /usr/local/bin/filecloud-server gc \
  --data-dir /var/lib/filecloud
```

GC 处理未发布对象和过期历史独占的内容块，默认对象宽限期为 24 小时。当前快照、仍可恢复的历史快照和回收站引用的内容块都会保留；共享块只存一份。回收站到期并不保证立刻腾出同等大小的空间，实际字节数以停服后的 `--dry-run` 为准。GC 保留已发布提交、目录树和文件描述信息作为三方同步基线，因此离线超过 60 天的设备仍可用共同基线合并。若本地缓存和服务端都缺少所需基线元数据，保留原工作目录，在新空目录重新绑定后核对并迁入本地修改。不要在原工作目录强行覆盖。

维护完成后启动服务：

```bash
sudo systemctl start filecloud
curl -fsS http://127.0.0.1:8080/readyz
```

### 备份与恢复

备份必须同时包含 SQLite 元数据和对象目录。推荐流程：

```bash
sudo install -d -m 0700 /srv/backup
sudo systemctl stop filecloud
sudo tar -C /var/lib -czf "/srv/backup/filecloud-$(date +%Y%m%d-%H%M%S).tar.gz" filecloud
sudo systemctl start filecloud
```

恢复时先停止服务，把整个数据目录恢复到同一路径，确认所有者和权限，然后运行完整性检查：

```bash
sudo chown -R filecloud:filecloud /var/lib/filecloud
sudo chmod 0700 /var/lib/filecloud
sudo -u filecloud /usr/local/bin/filecloud-server integrity check --data-dir /var/lib/filecloud
sudo systemctl start filecloud
```

不要只备份 `metadata.db` 或只备份 `objects/`，两者单独恢复都不能组成一致的资料库。

## 13. 常见错误

### `open data-directory lock ... no such file or directory`

原因：数据目录存在，但没有执行过 `filecloud-server init`，所以缺少 `.filecloud.lock`。

处理：

```bash
filecloud-server init --data-dir /path/to/data
```

初始化成功后再创建用户并启动服务。已有正式数据的目录不要删除后重新初始化。

### zsh: `read: -p: no coprocess`

原因：`read -p` 在 zsh 中表示从 coprocess 读取；它不是 Bash 的提示参数。读取失败后密码变量为空，随后还会看到 `password must be 1-1024 bytes`。

zsh 使用：

```zsh
read -rs 'PASSWORD?Password: '
```

Bash 使用：

```bash
read -rsp "Password: " PASSWORD
```

### `password must be 1-1024 bytes`

传入的密码为空或超过 1024 bytes。先确认 `read` 命令适用于当前 shell，并确认管道左侧确实输出了一行密码。

### `server must use https unless it is a loopback url`

客户端正在访问非回环 HTTP 地址。为服务配置 HTTPS，并把 `--server` 改成 `https://...`。本机测试可以使用 `http://127.0.0.1:8080` 或 `http://localhost:8080`。

### `data directory is locked by another process`

`serve`、用户管理、GC 或完整性检查正在争用数据目录锁。停止正在运行的服务后再执行离线管理命令。

### `worktree is not bound`

传给 `sync` 的工作目录没有在指定 `client-dir` 中完成绑定，或者使用了另一套客户端状态目录。检查 `--client-dir` 和 `--worktree` 是否与绑定时完全一致。

### 文件系统被拒绝

确认客户端状态目录和工作目录位于本地 ext4、APFS 或固定 NTFS。网络共享、软链接绕转、挂载边界和不受支持的卷会被拒绝。

## 14. 命令速查

```text
filecloud-server version
filecloud-server init --data-dir PATH
filecloud-server serve --data-dir PATH [--listen ADDRESS]
filecloud-server user add --data-dir PATH --username NAME --password-stdin
filecloud-server user reset-password --data-dir PATH --username NAME --password-stdin
filecloud-client version
filecloud-client login --server URL --username NAME --device-name NAME --password-stdin
filecloud-client logout --server URL --token-stdin
filecloud-client library create --server URL --library-id UUID --name NAME --token-stdin
filecloud-client library list --server URL --token-stdin
filecloud-client library inspect --server URL --library-id UUID --token-stdin
filecloud-client library bind --client-dir PATH --server URL --library-id UUID --worktree PATH --device-id UUID --token-stdin [--import-local | --preview-merge | --merge-existing CANDIDATE]
filecloud-client library sync --client-dir PATH --worktree PATH [--confirm-delete 12_HEX]
filecloud-client library watch --client-dir PATH --worktree PATH --interval DURATION
filecloud-client library history list --client-dir PATH --worktree PATH [--page-size N] [--page-token TOKEN] [--include-merged]
filecloud-client library history inspect --client-dir PATH --worktree PATH --commit 64_HEX [--path PATH_OR_DOT] [--page-size N] [--page-token TOKEN]
filecloud-client library restore --client-dir PATH --worktree PATH --commit 64_HEX --path PATH_OR_DOT
filecloud-client library restore --client-dir PATH --worktree PATH --confirm 12_HEX
filecloud-client library unbind --client-dir PATH --worktree PATH
filecloud-server integrity check --data-dir PATH
filecloud-server gc --data-dir PATH [--dry-run] [--grace-period DURATION]
```

以下两项在 `v0.3.0` 标签之后加入，使用当前源码构建的二进制：

```text
filecloud-server serve --data-dir PATH [--listen ADDRESS] [--site-url URL] [--web-admin-user USERNAME ...]
filecloud-client library status --client-dir PATH --worktree PATH
```

协议、冲突处理和崩溃恢复边界见 [同步架构](../design/architecture.md)，接口细节见 [HTTP API 契约](../design/http-api.md)。
