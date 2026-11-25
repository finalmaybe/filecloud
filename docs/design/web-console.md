# 服务端 Web 控制台契约

状态：当前源码版契约。Web 控制台在 `v0.3.0` 之后加入，默认关闭。实际操作见[部署与客户端使用](../operations/deployment-and-client.md#启用网页入口)。

网页以文件为入口：从资料库打开目录，点击文件名预览，或从「历史版本」打开该路径的版本栏。资料库主线历史、合并来源和目录叠加恢复由 CLI 提供。

资料库侧栏的「回收站」列出该库尚未到期的文件、目录和版本记录。文件列表的「删除」操作在当前页打开「确认删除」弹窗，显示目标路径。确认后针对列表所示的 Head 发布删除，并重新显示所在目录。目录作为一个条目恢复，目标已占用时拒绝发布；「放回版本列表」仅撤销版本记录的删除。「保留设置」允许所有者修改两项保留期，缩短历史期须先预览影响并确认。这些表单走现有同源 Web 会话。

## 开关、来源与身份

- `filecloud-server serve --site-url <URL>` 启用 Web；WebDAV 共用该地址，但默认关闭。仅管理员可进入「头像 → 管理设置」，切换 WebDAV 或访问「服务概览」「账号管理」。省略 `--site-url` 时两者均关闭。配置值是浏览器看到的完整外部 origin（scheme、host、可选 port；无路径、凭据、query 或 fragment）。启用后 `/` 是浏览器入口，页面位于 `/login`、`/libraries`、`/accounts` 等路径，资产位于 `/assets/`；同时为下述资料库 API 启用浏览器会话。`/v1/`、`/dav/`、`/healthz` 和 `/readyz` 保持独立，未知 Web 页面返回 404，不回落到首页。
- 生产使用固定 HTTPS origin，反向代理终止 TLS，后端监听回环；本机试用仅在监听地址和配置的 HTTP origin 均为回环地址时允许明文。按配置校验请求 Host 与写请求 Origin，不从任意转发头推断公开地址或授予权限。代理须保留外部 Host。
- 可重复的本机启动参数 `--web-admin-user <username>`：启动时按现有规范用户名解析为已有用户 ID，失败则报配置错误；未启用 Web 不接受此参数。授权集合固定到本次服务运行。配置变更并重启即生效；旧会话下一次请求按新集合判断。默认集合为空，不按用户名、首用户、首个登录者或来源 IP 自动提权。首个账号仍由主机在停服时创建。

| 主体 | 允许 | 明确拒绝 / 验收 |
|---|---|---|
| 未登录 | 登录页、公共静态资产；沿用匿名存活/就绪探针 | 私有页面去登录，未认证写请求不执行。登录表单同样保护来源。 |
| 已登录所有者 | 自己的资料库、历史和设备交接；退出当前会话 | 直接请求后台管理为 403；他人资料库与不存在统一 404。以两个所有者使用同一 LibraryId 验证。 |
| 本机显式授权的管理员 | 自身所有者权限 + 服务概览、开户和重置已有账号密码 | 账号管理不提供直接代登录或跨资料库访问。每次请求在服务端验证；撤销权限后旧会话也无法再操作。重置属于高权限操作，确认对象后才提交。 |
| 主机运维者 / 绑定设备 | 本机管理开关、停服维护 / 客户端扫描、合并、确认、恢复、落盘 | 不接受网页下发 shell、任意磁盘路径、启动服务或远控客户端。 |

### 浏览器会话

浏览器登录复用现有密码校验、可撤销会话、过期规则和同一组 KDF 并发预算。每次登录独立签发；退出仅撤销本次会话。目标账号密码重置与撤销其所有 Web/CLI 会话在同一存储事务中完成。管理请求使用服务进程已持锁的 Store；密码派生使用有界预算。离线 CLI 及独占锁保留。

Web 用 host-only、`Path=/`、`HttpOnly`、`SameSite=Lax` 的会话 cookie；HTTPS 强制 `Secure`，回环明文试用例外。Web 表单用 POST，共享建库接口用 PUT。所有浏览器会话写请求（含登录、退出）均要求 Origin 精确匹配配置 origin；缺失、不匹配或 `null` 均拒绝，不执行操作。不得在 GET 里改变业务状态。凭据不返回给 JS、不放在 URL、Web Storage、日志或复制命令中；含身份及私有信息的响应 `Cache-Control: no-store`。日志记录操作类别/结果，不记录表单、原始 URL 查询、用户名或文件路径。沿用 `opslog` 脱敏约定。只使用同源嵌入资源，不加载外站脚本或字体。

「我的资料库」用同源 JavaScript 获取列表、创建资料库。`GET /v1/libraries`、`GET /v1/libraries/{LibraryId}` 和 `PUT /v1/libraries/{LibraryId}` 共用 CLI 的 JSON 处理逻辑。文件列表页面地址为 `/libraries/{LibraryId}`，根目录、子目录和翻页数据由 `GET /v1/libraries/{LibraryId}/files` 提供。目录链接、分页、预览和版本入口使用响应的固定提交。点击文件名在原地查看器中预览（[交互依据](../research/nextcloud-file-viewer.md)）；`/libraries/{LibraryId}/file-preview` 也可独立打开，两者共用内容模板。「更多 → 历史版本」保留版本栏。预览按所有者和已发布快照路径读取，不开放原始对象接口。文件版本栏（含续页）使用 `GET /v1/libraries/{LibraryId}/file-history` 获取 JSON；新标签页和失败回退继续打开 `/libraries/{LibraryId}/file-history` 完整 HTML 页面，二者读取同一提交、路径和分页游标。确认删除单条记录时，网页使用 `DELETE /v1/libraries/{LibraryId}/file-history/{CommitId}`，成功后就地移除该行并保留滚动位置；独立历史页在无脚本时仍可提交表单。启用 Web 后，这些操作在没有 Authorization 头时也接受 Web cookie，沿用 Web Host 和写请求 Origin 校验。其余受保护的 `/v1` 接口仍只认 Bearer；Web 管理操作不接受 Bearer。两种会话保持独立，显式 Authorization 认证失败不回退到 cookie。

## 功能兼容矩阵

`L` = `/v1/libraries/{LibraryId}`。协议请求、状态码、分页、缓存、错误体与预算以[既有契约](http-api.md)及[历史规格](history-and-safe-restore.md)为准。下表区分浏览器、绑定设备和服务主机的职责。

| 入口（CLI / HTTP） | 用户从哪里完成；哪里是真相 | 禁止动作及回归场景 |
|---|---|---|
| `login`、`logout`；`POST /v1/sessions`、`DELETE /v1/sessions/current` | 浏览器独立会话；CLI 仍用 Bearer | 登录不自动重试；验证两处同时登录，Web 注销后 CLI 仍能用。 |
| `user add/reset-password` | 管理页面或原本机命令 | 不能公开注册。重置后新密码可用，旧口令和两端旧会话失效；普通账号无法操作。 |
| `library create/list/inspect`；`PUT L`、`GET /v1/libraries`、`GET L`、`GET L/head` | 浏览器用共享的资料库接口列库、建库、查看详情；单独的 `GET L/head` 使用 CLI Bearer。Head 表示服务端已发布状态 | 重试创建复用 UUID；验证同名、空 Head、ETag/304、稳定分页和所有者隔离；不显示虚构设备在线状态。 |
| `library history list [--include-merged]`；`GET L/history`、`GET L/history/{CommitId}` | CLI 锚定首次读取的 Head；合并来源在所属提交下展开。网页只列出选定文件的版本 | 不按客户端时间排序；翻页期间发布新 Head，旧游标仍在原锚点；游标失效可刷新。只展开经角色证明的来源，保留深度/数量上限。 |
| `library history inspect [--path]`；`GET L/history/{CommitId}`、`GET L/objects/{ObjectType}/{ObjectId}` | 先固定已发布的完整 CommitId，再从 Root 逐级检查目录和文件元数据 | CLI 检查元数据时不读块；Web 预览按已发布快照中的路径读取文件内容。 |
| `library bind [--import-local]`、凭据刷新、`unbind` | 在设备工作目录操作；资料库信息弹窗提供库 ID，绑定步骤见[部署与客户端使用](../operations/deployment-and-client.md) | 不读客户端数据库；分别验证空目录 checkout、非空目录导入限制、更新凭据；解绑不删普通文件。 |
| `library status`、`sync [--confirm-delete]`、`watch` | CLI 的 Sync Base、Pending 及工作目录扫描；设备端执行 | 不把 Head 写成“设备已同步”。走通删除精确确认、两设备合并/冲突副本、进程中断与 journal 续跑。 |
| `POST L/object-checks`、`GET/PUT L/blocks/{ObjectId}`、`GET/PUT L/objects/{ObjectType}/{ObjectId}`、`PUT L/head` | 绑定设备传输同步对象并更新 Head；这些原始对象和 Head 接口只接受 Bearer | 不提供对象编辑器。原 CLI 上传/下载、重复 PUT、摘要错误、缺对象、限流和强 If-Match CAS 均按原契约；Web 开关不影响。 |
| `library restore --commit … --path …`、`restore --confirm …` | 在绑定设备选择文件、目录或根；先同步，预览后用候选的 12 位小写前缀确认 | 支持找回已删除的路径和目录叠加。预览后工作目录或 Head 变化须先同步并重新预览。 |
| Web 文件版本栏：下载、恢复、删除 | 所有者可下载所选版本，或确认恢复、删除一条更早的记录。恢复只替换当前仍存在的同路径文件 | 服务端核对打开列表时的 Head；恢复有变化时校验并 CAS 发布，相同时不创建提交。删除仅隐藏该路径的记录，不改变 Head 或历史快照。 |
| `GET /healthz`、`GET /readyz`、两侧 `version` | 管理页面显示探针、检查时间和服务端构建信息；客户端版本在设备查看 | 进程存活不等于存储就绪；模拟 503 和请求未抵达，分别显示未就绪与未知；原匿名探针不增加重定向/登录。 |
| `init`、`serve` 参数、停止/重启，`integrity check`、`gc [--dry-run] [--grace-period]`，完整备份恢复 | 在服务器主机上执行 | 不在线维护。验证关停→独占检查/GC→启动→就绪；灾备同时还原元数据和对象。 |
| 双二进制及 `filecloud-client` 过渡期服务端子命令 | 原 Go 构建与原生平台发布；网页资源编入服务端，不依赖 Node/CDN | 旧目录直接升级、Web 开/关均可用原 CLI；执行原 1B/1C 和目标平台双二进制冒烟。无用户配置迁移要求。 |

预览和版本链接使用完整提交 ID；绑定设备下次同步时接收 Web 恢复发布的新版本。安全恢复继续遵循 [ADR-0004](../adr/0004-restore-history-with-a-forward-commit.md)，合并继续遵循 [ADR-0003](../adr/0003-merge-on-the-client.md)。

## 资料库应用密码

资料库详情侧栏的「应用密码」打开当前库的独立管理页。所有者可在此创建、列出或撤销凭据，复制挂载地址。入口、表单权限及验收见[可写 WebDAV 技术方案](./webdav-office.md#2-应用密码按资料库管理)。

## 验收与原设计归档

文件列表、预览、单文件版本恢复和删除见[文件版本界面验收记录](../acceptance/web-console/file-versions.md)。构建归档仍按[发布门禁](../distribution.md)执行。

原阶段规格（[#57](https://github.com/finalmaybe/filecloud/issues/57)，基线 `8f484e9`）采用「图谱」：选择资料库主线提交或合并来源，检查快照，再复制命令交给绑定设备恢复。这套工作台已由文件列表和版本栏取代。[离线原型](./web-console-prototype/README.md)、[旧版浏览器走查](../acceptance/web-console/browser.md)和[候选版发布记录](../acceptance/web-console/release.md)保留当时的页面与验收结果。
