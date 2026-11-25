# Sync HTTP API 契约

状态：`/v1` 实现基线。生产环境只能经 HTTPS 调用；服务端自身由反向代理提供 TLS。同步接口见本文，历史读取接口及其资源边界见[历史浏览与安全恢复](./history-and-safe-restore.md)。协议语义已由对象测试向量、状态机测试和目标文件系统门禁锁定；任何修订必须同时更新设计文档和验收用例。

本 API 是新协议，不兼容 Seafile。JSON 字段使用 PascalCase；所有控制面 JSON 成功或失败响应都包含 `RetCode` 和 `Message`。二进制成功响应及 `GetMetadataObject` 返回的规范对象字节除外；这些接口失败时仍返回 JSON 错误对象。

## 通用规则

### 鉴权

除创建会话和健康检查外，CLI 使用：

```http
Authorization: Bearer <opaque-token>
```

通过 `--site-url` 启用 Web 时，ListLibraries、GetLibrary、CreateLibrary、ListFiles、GetFileHistory、DeleteFileVersion 也接受同源 Web 登录的 HttpOnly Cookie。Cookie 请求校验 Host（不匹配为 `421/1002`），建库 PUT 和删除版本 DELETE 还校验 Origin（缺失、不匹配为 `403/1002`）。显式 Authorization 头始终走 Bearer 认证，失败不回退。浏览器 Cookie 不授予会话 API、Head、资料库提交历史或对象传输权限。未启用 Web 时所有受保护接口维持 Bearer 专用。

服务端先鉴权，再查找用户请求的资料库。第一阶段只有资料库所有者可访问。

### JSON 响应

```json
{
  "RetCode": 0,
  "Message": "success",
  "Library": {}
}
```

错误不得包含 SQL、堆栈、内部文件路径或 token。HTTP 状态表达协议类别，`RetCode` 表达稳定的程序错误。

| HTTP | RetCode | 名称 | 说明 |
|---:|---:|---|---|
| 200/201/204 | 0 | Success | 成功 |
| 400 | 1000 | InvalidArgument | 字段、路径、对象格式或分页参数非法 |
| 401 | 1001 | Unauthenticated | token 缺失、无效、过期或撤销 |
| 403 | 1002 | PermissionDenied | 已认证但无权限 |
| 404 | 2000 | NotFound | 资源不存在 |
| 409 | 3000 | AlreadyExists | 唯一资源已存在 |
| 409 | 3001 | ObjectConflict | 同一 ObjectId 已存在不同内容 |
| 412 | 3002 | HeadConflict | `If-Match` 与当前 Head 不一致 |
| 422 | 3003 | MissingObject | 新 Head 的可达图缺对象 |
| 422 | 3004 | HashMismatch | 正文摘要与 URL ObjectId 不一致 |
| 413 | 3005 | PayloadTooLarge | 正文、对象数量或单批次超限 |
| 428 | 3006 | PreconditionRequired | Head 更新缺少 `If-Match` |
| 409 | 3007 | CurrentFileVersion | 不能删除该文件的当前版本 |
| 429 | 4000 | RateLimited | 超限；响应携带 `Retry-After` |
| 500 | 5000 | Internal | 未公开的服务端错误 |
| 503 | 5001 | Unavailable | 存储暂不可用，可重试 |

错误示例：

```json
{
  "RetCode": 3002,
  "Message": "library head changed",
  "Head": {
    "CommitId": "<current>",
    "ETag": "\"head-version-8\""
  }
}
```

### 时间、ID 与分页

- 时间是 RFC3339 UTC。
- UserId、LibraryId、DeviceId 只接受 RFC 9562 规范小写连字符形式（例如 `01234567-89ab-4def-8123-456789abcdef`）；其他等价文本表示一律拒绝。
- ObjectId 是 64 位小写 SHA-256 十六进制。
- List 使用 `PageSize` 和 `PageToken`；默认 100，最大 500；响应返回 `NextPageToken`。
- page token 是不透明值，客户端不得解析。

### 幂等与重试

- 所有对象 `PUT` 由 ObjectId 天然幂等。
- 资料库 ID 由客户端生成，创建使用资源 `PUT`，重复请求天然幂等。
- Head 更新以 `If-Match` 实现并发控制；网络结果未知时先读取 Head，不盲目重放。
- 客户端只自动重试明确幂等请求的网络错误、429 和 503；`CreateSession` 禁止自动重试。其他 4xx 必须重新计算或提示用户。

## CreateSession

`POST /v1/sessions`

创建可撤销访问令牌。正文只允许经 HTTPS 发送；不得记录。请求正文最大 8 KiB，超过返回 `PayloadTooLarge`。

请求：

```json
{
  "Username": "alice",
  "Password": "...",
  "DeviceName": "alice-laptop"
}
```

约束：Username 1-128 字符；Password 1-1024 字节；DeviceName 1-128 字符。`CreateSession` 不可自动重试，响应必须带 `Cache-Control: no-store`。

响应 `200`：

```json
{
  "RetCode": 0,
  "Message": "success",
  "Session": {
    "AccessToken": "<shown-once>",
    "ExpiresAt": "2026-09-08T00:00:00Z",
    "UserId": "<UUID>"
  }
}
```

错误：`InvalidArgument`、`Unauthenticated`、`RateLimited`。用户名不存在和密码错误必须执行等价 KDF 工作并返回相同消息与状态。所有 `401` 响应携带 `WWW-Authenticate: Bearer`。

登录 KDF 的默认并发上限为全局 2、每个直连来源 IP 1、每个规范用户名 1。任一上限用尽即返回 `429`。来源 IP 取自 TCP 连接的对端地址；经同一反向代理 IP 的登录共享该来源的并发槽。参见[部署性能基线](../operations/performance-baseline.md#冻结默认值)。

## DeleteCurrentSession

`DELETE /v1/sessions/current`

撤销当前 token。首次成功返回 `204`；之后该 token 已不能通过鉴权，重复请求返回 `401 Unauthenticated`。

## CreateLibrary

`PUT /v1/libraries/{LibraryId}`

LibraryId 由客户端生成 UUID，并只在当前认证用户的所有者作用域内唯一；不同用户使用相同 UUID 时创建彼此隔离的资料库。请求正文最大 8 KiB；Name 为 NFC UTF-8、1-128 字符且最多 512 bytes。请求：

```json
{
  "Name": "Documents"
}
```

可选整数字段 `TrashDays`、`HistoryDays` 各自取 1–36500；省略表示跟随全局默认（初始为 30、60）。创建请求重放不会覆盖后续修改的保留设置。返回的同名字段中 0 表示继承，`HistoryDays=-1` 表示永久历史。

首次创建响应 `201`；相同用户以同一 LibraryId 和 Name 重试返回 `200`；同一 ID 携带不同 Name 返回 `ObjectConflict`。响应：

```json
{
  "RetCode": 0,
  "Message": "success",
  "Library": {
    "LibraryId": "<UUID>",
    "OwnerUserId": "<UUID>",
    "Name": "Documents",
    "TrashDays": 0,
    "HistoryDays": 0,
    "HeadCommitId": null,
    "ETag": "\"head-version-0\"",
    "CreatedAt": "2026-08-09T00:00:00Z",
    "UpdatedAt": "2026-08-09T00:00:00Z"
  }
}
```

`OwnerUserId` 是 library bind 客户端所需的响应字段，服务端必须返回；旧客户端可以忽略该字段。新客户端用它把访问 token 与提交作者绑定，不接受用户手填身份；缺失或无效时客户端将服务端视为不兼容且不得上传对象或发布 Head。同一所有者下 Name 唯一。所有服务端查询、对象路径和锁都以 `(OwnerUserId, LibraryId)` 定位资料库，不能通过全局 UUID 冲突泄露其他用户资源。错误：`InvalidArgument`、`AlreadyExists`、`ObjectConflict`。

## ListLibraries

`GET /v1/libraries?PageSize=100&PageToken=...`

响应：

```json
{
  "RetCode": 0,
  "Message": "success",
  "Libraries": [
    {
      "LibraryId": "<UUID>",
      "Name": "Documents",
      "HeadCommitId": "<CommitId>",
      "ETag": "\"head-version-8\"",
      "CreatedAt": "2026-08-09T00:00:00Z",
      "UpdatedAt": "2026-08-09T01:00:00Z"
    }
  ],
  "NextPageToken": ""
}
```

默认按 `CreatedAt, LibraryId` 升序稳定分页。

## ListFiles

`GET /v1/libraries/{LibraryId}/files` 返回当前发布版本的根目录。可选查询参数：`CommitId` 固定已发布提交，`Path` 指定规范相对目录（默认 `.`），`Offset` 是从零开始的目录项偏移量（默认 0）。

响应包含 `RetCode`、`Message` 和 `Directory`。`Directory` 含 `CommitId`、`Path`、`Parent`、`Entries`（每项含 `Name`、`Type`、`ModifiedAt`）和 `NextOffset`。每页最多 50 项；翻页沿用返回的 `CommitId` 和 `Path`，传入 `NextOffset`；零表示结束。尚未发布的资料库根目录返回 `CommitId: null` 和 `Entries: []`；已发布的空目录返回实际提交 ID 和空数组。未知库、他人资料库、未发布的提交和不存在的路径返回 `404/2000`；非法参数、超出目录长度的偏移或请求文件而非目录返回 `400/1000`。只读取元数据，不下载文件内容。

## GetFileHistory

`GET /v1/libraries/{LibraryId}/file-history?CommitId=…&Path=…[&PageToken=…]`

`CommitId` 必须是固定的已发布主线提交，`Path` 是该提交内文件的规范相对路径，按 URL 查询参数编码。首次请求返回该文件在该提交的状态。更早的记录按主线从新到旧读取，每页从最多 50 个主线提交中筛选变更；还有更早的提交时，额外读取下一页的首个提交作边界比较。即使本页没有这个文件的变更也可能有下一页。续页沿用原 `CommitId` 和 `Path`，仅追加 `PageToken`，不跟随新 Head。

响应包含 `RetCode`、`Message` 和 `FileHistory`。后者包含 `LibraryId`、`AnchorCommitId`、`Path`、`Current`（`CommitId`、`FileId`、`ModifiedAt`；续页为 `null`）、`Versions` 数组和 `NextPageToken`（结束时为空字符串）。已在 Web 删除的单文件记录不再列出，不改变发布提交及其快照。`IsCurrent` 标记截至锚定提交该路径最近一次变更，翻页时延续同一判断。每条变更另含 `CreatedAt`、`Message`、`Change`（`added`、`content` 或 `modified-time`）。非法参数/游标返回 `400/1000`，无权访问的库、未发布的提交或首次读取时不存在的文件统一返回 `404/2000`；历史读取繁忙返回 `429/4000` 并携带 `Retry-After`。

## DeleteFileVersion

`DELETE /v1/libraries/{LibraryId}/file-history/{CommitId}?Path=…&AnchorCommitId=…`

`CommitId` 是所选变更记录的提交，`Path` 是该文件的规范相对路径，`AnchorCommitId` 是打开列表时的主线提交。仅所有者可删除。成功返回 `204`，无响应体；该路径的这条记录移入回收站，不改变 Head 和快照。在回收站中放回记录也不发布 Head。参数非法返回 `400/1000`，记录不存在或已删除返回 `404/2000`，锚点过期返回 `412/3002`，选中当前版本返回 `409/3007`。

## GetLibrary

`GET /v1/libraries/{LibraryId}`

返回与 `CreateLibrary` 相同的 Library 对象，并在 HTTP `ETag` header 返回当前 Head 版本。不存在返回 `NotFound`。

## GetLibraryHead

`GET /v1/libraries/{LibraryId}/head`

响应：

```json
{
  "RetCode": 0,
  "Message": "success",
  "Head": {
    "CommitId": "<CommitId-or-null>",
    "ETag": "\"head-version-8\""
  }
}
```

HTTP 同时返回 `ETag`。客户端可发送 `If-None-Match`；未变化返回 `304`，无 JSON body。

## CheckObjects

`POST /v1/libraries/{LibraryId}/object-checks`

检查服务端缺失对象，只是传输优化，不是发布验证。每次最多 1000 个 ID，整个 JSON 最大 1 MiB。

请求：

```json
{
  "Objects": [
    {"ObjectId": "<sha256>", "ObjectType": "Block"},
    {"ObjectId": "<sha256>", "ObjectType": "Directory"}
  ]
}
```

`ObjectType` 取 `Block`、`File`、`Directory`、`Commit`。

响应：

```json
{
  "RetCode": 0,
  "Message": "success",
  "MissingObjects": [
    {"ObjectId": "<sha256>", "ObjectType": "Block"}
  ]
}
```

对象存在不代表其可被新 Head 引用；`UpdateLibraryHead` 仍执行完整图验证。

## 资源预算

- Commit JSON 最大 64 KiB；File JSON 最大 20 MiB；Directory JSON 最大 32 MiB；元数据 JSON 最多嵌套 256 层（顶层容器计为第一层）。
- 一个 File 最多 262144 个 blocks，最大逻辑大小 1 TiB。
- 一个 Directory 最多 100000 个 entries；快照树深最多 256。
- 一次 Head 验证的共享总预算为 2000000 个去重对象，包含当前 Root 和第二 parent 首次引入的全部对象；首次引入的 Commit 最多 1024 个，parent 遍历深度最多 1024。
- 普通提交验证当前 Root。合并提交还必须验证第二 parent 首次带入已发布历史的 Commit 和快照，直到遇到已发布可达对象；作者、类型、摘要和引用均需验证。
- JSON 解析、数组分配和图遍历必须先检查预算；任一预算超限返回 `PayloadTooLarge`，不得部分接受 Head。Head 验证受全局工作池、每资料库单并发和请求 deadline 约束，相同 ETag 的重复昂贵验证可短期缓存结果。
- block 始终流式处理，不按 `Content-Length` 一次性分配内存。
- 每个对象 PUT 在读取正文前取得全局和用户并发槽并设置读取 deadline；无槽返回 `RateLimited`。PutBlock 还按 Content-Length 原子预留临时字节，失败时释放。
- 磁盘剩余空间低于总容量 5% 或 1 GiB（取较大值）时拒绝新的对象 PUT，返回 `Unavailable`。用户滚动时间窗内首次接收的对象字节达到安全预算时返回 `RateLimited`；重传已存在对象不重复计费。这些限制用于保护共享服务，不是用户可配置容量配额。

## PutMetadataObject

`PUT /v1/libraries/{LibraryId}/objects/{ObjectType}/{ObjectId}`

`ObjectType` 只能是 `files`、`directories`、`commits`。Header：`Content-Type: application/json`。正文可为普通 JSON，服务端解析、校验、按 RFC 8785 重新规范化并计算 SHA-256；摘要必须等于 URL ObjectId。

成功首次写入返回 `201`，同内容已存在返回 `200`：

```json
{
  "RetCode": 0,
  "Message": "success",
  "Object": {
    "ObjectId": "<sha256>",
    "ObjectType": "File",
    "Created": true
  }
}
```

未知字段、重复 JSON key、错误 Type/Version 或非法路径名返回 `InvalidArgument`。正文、数组、深度或对象数量超过硬限制返回 `PayloadTooLarge`。同 ID 不同内容返回 `ObjectConflict`；摘要不同返回 `HashMismatch`。

## GetMetadataObject

`GET /v1/libraries/{LibraryId}/objects/{ObjectType}/{ObjectId}`

成功返回 `application/json` 的 RFC 8785 规范字节，并带强 ETag：`ETag: "<ObjectId>"`、`Cache-Control: private, immutable`。不存在返回 `NotFound`。

## PutBlock

`PUT /v1/libraries/{LibraryId}/blocks/{ObjectId}`

Header：`Content-Type: application/octet-stream`、`Content-Length` 必填。缺失或非法 Content-Length 返回 `InvalidArgument`；正文超过 4 MiB 返回 `PayloadTooLarge`；零长度返回 `InvalidArgument`。空文件由空 Blocks 数组表示。File 校验时，除最后一个 block 外都必须恰好为 4 MiB，最后一个 block 为 1 byte 至 4 MiB。服务端流式写临时文件并计算 SHA-256，匹配后原子发布。

成功首次写入 `201`，已存在相同 block `200`：

```json
{
  "RetCode": 0,
  "Message": "success",
  "Block": {
    "ObjectId": "<sha256>",
    "Size": "4194304",
    "Created": true
  }
}
```

连接中断不得留下最终对象；临时文件由宽限期清理。

## GetBlock

`GET /v1/libraries/{LibraryId}/blocks/{ObjectId}`

成功返回 `application/octet-stream`，带 `Content-Length`、`ETag: "<ObjectId>"`、`Cache-Control: private, immutable`。第一阶段按 block 恢复，不要求单 block Range；客户端重新请求未完成 block。

## UpdateLibraryHead

`PUT /v1/libraries/{LibraryId}/head`

必须带从 `GetLibraryHead` 获得的单个强 `If-Match`；初始空 Head 使用 `"head-version-0"`。请求正文最大 4 KiB。缺失 `If-Match` 返回 `PreconditionRequired`；弱 ETag、ETag 列表、`*` 或语法错误返回 `InvalidArgument`。请求：

```json
{
  "CommitId": "<CommitId>"
}
```

服务端在一个事务语义内：

1. 鉴权并读取当前 Head/ETag。
2. 比较 `If-Match`。
3. 登记该库的发布活动后读取 Commit，验证 `AuthorUserId` 等于认证用户，并验证其第一个 Parent 与当前 Head 一致；初始提交 Parents 为空。
4. 遍历并校验当前 Root 的完整可达图、对象类型、摘要、block 总大小和路径规则。若存在第二 parent，只沿尚未发布的分支验证它首次引入发布历史的子图，遇到已发布 Commit 边界即停止。
5. 用短数据库事务再次比较 Head/ETag，并原子更新 Head、head version 和发布 reachability 索引。第 4、5 步都在同一次发布活动内，在线 GC 不会在两步之间删除对象。

响应 `200`：

```json
{
  "RetCode": 0,
  "Message": "success",
  "Head": {
    "CommitId": "<CommitId>",
    "ETag": "\"head-version-9\""
  }
}
```

`If-Match` 不匹配始终返回 `HeadConflict` 和当前 Head；不得修改 Head。缺对象返回 `MissingObject`，响应最多列出前 100 个缺失 ObjectId 并给出 `Truncated`，不得接受部分发布。

CAS 请求结果因断网而未知时，客户端必须调用 `GetLibraryHead`：当前 CommitId 等于待发布 CommitId 表示已成功，否则按 pending publication 状态重试或合并。服务端不得仅因 CommitId 相同绕过旧 ETag 返回成功。

## Health

`GET /healthz` 仅表示进程存活，不访问数据库。

`GET /readyz` 检查服务持有的数据目录锁、元数据库可读和对象目录可写；成功返回 `200`，失败返回 `503`。响应不得泄露内部路径或数据库 DSN。

`204` 与 `304` 没有响应正文，不受 JSON envelope 规则约束。已认证用户访问不属于自己的 Library 与资源不存在统一返回 `404 NotFound`，避免泄露资源身份。

## 协议版本演进

- URL 主版本 `/v1` 内只允许向响应增加可忽略的可选字段。
- 客户端发送未知请求字段默认拒绝，防止拼写被静默忽略。
- `/v1` 只接受对象 `Version: 1`。对象编码一旦发布不可原地改变。
- 本次 Unicode 17.0 更新在 `/v1` 内采用新的 NFC 与同名判定表，保留对象编码、哈希算法和 URL，不增加客户端版本检查。其余涉及新对象 Version、字段删除、类型变化、哈希输入或路径语义的变更仍须发布新的 HTTP 主版本。
- [WebDAV](./webdav-office.md) 使用独立 `/dav/` 路径和应用密码，不复用这些对象路由。原生发布涉及 DAV 写锁时返回 `503/5001`、`Retry-After` 及可选的 `Reason: DAVLocked`；候选整体保留，解锁后再发布。
