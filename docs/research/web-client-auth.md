# Nextcloud 与 Seafile：网页版和同步客户端如何共用接口

## Nextcloud：同一个文件接口，两种登录凭据

- 对已登录用户的文件，WebDAV 入口是 `/remote.php/dav/files/{user}/`。官方手册明确列出两种认证方式：有效的会话 Cookie，或 HTTP Basic；对于 WebDAV 客户端和脚本，推荐使用应用密码。列目录、上传和移动分别使用 `PROPFIND`、`PUT` 和 `MOVE`。[WebDAV 基础与认证](https://docs.nextcloud.com/server/latest/developer_manual/client_apis/WebDAV/basic.html)
- 网页的 DAV 客户端会附带 `requesttoken` 请求头；服务端在会话认证路径上按请求检查 CSRF。这个值是防伪造请求用的，不是独立的登录凭据。[网页 DAV 客户端](https://github.com/nextcloud-libraries/nextcloud-files/blob/7fa4fd21c97951d07a9b05541c3a1a487df12921/lib/dav/dav.ts#L70-L90) · [服务端 DAV 认证](https://github.com/nextcloud/server/blob/6f923a986c0745596e07e56c5e270ced57455e88/apps/dav/lib/Connector/Sabre/Auth.php#L121-L193)
- 原生应用可通过 Login Flow v2 打开浏览器，由用户登录并授权。应用轮询完成后得到 `server`、`loginName` 和 `appPassword`，后续使用自己的应用密码，而不是取走浏览器会话。[Login Flow v2](https://docs.nextcloud.com/server/latest/developer_manual/client_apis/LoginFlow/index.html)

## Seafile：业务接口可共用，桌面同步另有协议

- Seahub 网页初始化 API 客户端时读取 `sfcsrftoken`，设置 `X-CSRFToken`，用浏览器会话访问同源接口。同一份客户端代码另有显式 Token 模式，设置 `Authorization: Token …`。此版本的服务端 Token 认证同时识别 `Token` 和 `Bearer` 前缀。[两种初始化路径](https://github.com/haiwen/seahub/blob/9c436d7228694baf0d2509f0fcff415498cdc44f/frontend/src/api/seafile-api.js#L8-L35) · [网页实际使用的路径](https://github.com/haiwen/seahub/blob/9c436d7228694baf0d2509f0fcff415498cdc44f/frontend/src/api/seafile-api.js#L2323-L2327) · [前缀定义](https://github.com/haiwen/seahub/blob/9c436d7228694baf0d2509f0fcff415498cdc44f/seahub/utils/auth.py#L14-L17) · [服务端读取请求头](https://github.com/haiwen/seahub/blob/9c436d7228694baf0d2509f0fcff415498cdc44f/seahub/api2/authentication.py#L54-L71)
- 例如 `/api2/repos/` 的 `Repos` 视图同时接受 `TokenAuthentication` 和 `SessionAuthentication`。两种调用者访问同一个资料库列表接口，而不是各自维护一份列表业务。[路由](https://github.com/haiwen/seahub/blob/9c436d7228694baf0d2509f0fcff415498cdc44f/seahub/api2/urls.py#L35-L37) · [视图](https://github.com/haiwen/seahub/blob/9c436d7228694baf0d2509f0fcff415498cdc44f/seahub/api2/views.py#L839-L849)
- 桌面同步还使用 fileserver 的提交、目录对象、块和 Head 接口，有资料库同步令牌。网页列资料库和同步引擎传块是不同粒度的操作。[fileserver 路由](https://github.com/haiwen/seafile-server/blob/746546ad4c45d6724da3e041e8961da22ee99f94/fileserver/fileserver.go#L351-L416) · [同步令牌与 Head 更新](https://github.com/haiwen/seafile-server/blob/746546ad4c45d6724da3e041e8961da22ee99f94/fileserver/sync_api.go#L1016-L1093)

## 对 FileCloud 的启示

建议分开决定三件事：哪些业务操作对两端开放、各端如何建立身份、哪些步骤只属于设备同步。可以让网页和 CLI 共用资料库操作与发布规则，同时保留浏览器 Cookie 和 CLI Bearer。若决定让同一 HTTP 路由接受两种凭据，需要显式调整现有的 [Web 与 `/v1` 凭据隔离契约](../design/web-console.md)，保留浏览器写请求的来源校验和管理操作的单独授权。
