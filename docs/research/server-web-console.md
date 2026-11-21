# 服务端 Web 管理界面：调研与范围提案

**状态：调研归档；推荐范围已确认，尚未发布。** 冻结的实施与验收口径见[控制台契约](../design/web-console.md)。 核对基线：`8f484e9`。本文记录调研依据；排期、依赖和验收以 [GitHub 总议题 #56](https://github.com/finalmaybe/filecloud/issues/56) 及其 13 个子任务为准。

## 谁要用，在哪里完成任务？

- **部署管理员**要知道服务能否接收请求、能否读写存储，能为用户开户、重置密码，并知道哪些维护必须停服。当前管理员的权力来自服务器本机权限，并不存在可直接用于 Web 的管理员角色。
- **资料库所有者**要登录、创建和找到自己的资料库，知道当前 Head 是什么，沿正式版本找到一个历史文件，并安全地开始恢复。知道资料库 ID 不授予访问权。
- **在某台设备上同步的人**要绑定目录、处理删除确认或继续中断的发布。服务器看不到那台设备上的未同步变化，页面应说清下一步在哪里做，而不是猜测设备状态。

### 功能兼容矩阵

| 现有能力 | 建议的 Web 交付 | 兼容底线及依据 |
|---|---|---|
| 登录、注销、会话过期和撤销 | 浏览器独立登录/退出；失效后回登录页 | CLI 保留 Bearer；Web 共享密码校验、限流和重置撤销语义。[会话](../../internal/auth/auth.go)、[账号存储](../../internal/storage/accounts.go) |
| `user add` / `reset-password` | 管理员专属表单；提交前说明重置会让该用户所有设备重新登录 | **新增在线管理权限，已确认**。建议在本机配置显式授权已有账号；默认无人获权，不自动提升首用户。服务进程复用已持锁的 Store，不启动子命令或第二个数据目录写者。保留离线 CLI。[服务入口](../../internal/serverapp/serverapp.go)、[部署](../operations/deployment-and-client.md) |
| 资料库 create/list/inspect、读取 Head | 创建、稳定分页、详情、空库引导、显示最新已发布版本 | 所有查询限定所有者；不同所有者可使用同一 UUID；不引入跨账号文件管理。创建重试保持同一 UUID。[HTTP 契约](../design/http-api.md) |
| 主线历史、合并来源、提交与路径检查 | 沿第一父提交浏览；按需展开来源；在当前或历史快照内查看目录与文件元数据 | 固定提交再浏览，分页不中途换版本；来源须由服务端证明。从已发布提交的根按路径读取，文件检查不下载块。[历史规格](../design/history-and-safe-restore.md)、[CLI 路径检查](../../internal/clientapp/library_history_inspect.go) |
| 安全恢复（预览、确认、恢复中断） | 从历史路径交接给已绑定设备，提供该版本、原路径及预览命令；精确确认留在客户端 | 工作目录、同步基线、Head 必须收敛。目录叠加、预览过期、12 位候选确认、新提交前向发布仍由同一实现负责。[ADR-0004](../adr/0004-restore-history-with-a-forward-commit.md) |
| bind（含首次导入/刷新凭据）、unbind、离线 status、sync/watch、删除保护、冲突副本、checkout 与中断续跑 | 设备操作指引：使用当前资料库 ID，分平台给出命令；标明以该设备的 `status`/预览输出为准 | 不读取客户端 `client.db`，不代点删除确认，不把提交上的 DeviceId 当成当前在线设备。[使用手册](../operations/deployment-and-client.md)、[客户端合并决策](../adr/0003-merge-on-the-client.md) |
| 对象检查、分块上传下载、校验、去重、Head CAS | 继续由 CLI/现有 HTTP 客户端完成；页面只展示已发布结果 | 沿用认证、限流、对象编码和强 ETag；不做第二套同步引擎或通用对象编辑器。[HTTP 契约](../design/http-api.md)、[对象模型](../adr/0002-use-content-addressed-snapshots.md) |
| `healthz`、`readyz`、版本 | 展示进程存活、存储就绪、检查时间和服务端构建信息；失联显示未知 | 现有探针的状态码、JSON 和匿名用法原样保留。构建信息来自实际二进制，客户端版本在设备上查看。[探针](../../internal/health/health.go)、[服务入口](../../internal/serverapp/serverapp.go) |
| init、serve 参数、优雅停服、integrity check、GC 预演/执行/宽限期、整目录备份与灾备恢复 | 管理员维护清单，可在停服前复制步骤；停服后回终端执行，重启后检查就绪 | 不从页面执行 shell、不做在线 GC 或在线修复；保留独占锁。备份须覆盖元数据与对象。[部署与维护](../operations/deployment-and-client.md)、[存储入口](../../internal/storage/storage.go) |
| 双二进制、旧命令兼容和跨平台发布 | 建议作为 `filecloud-server serve` 的可选同源 `/web/`，不增加部署服务 | 默认关闭；开关不改变 `/v1`、探针及未知路由。保持源码 Go 构建、离线静态资源、原生平台门禁、发布包及许可材料。[分发规范](../distribution.md) |

第一版不新增原产品没有的共享、资料库删除、配额或独立网页网盘上传/下载。若目标是“从网页远程操控设备完成同步或恢复”，这是另一项产品能力，需要设备端执行与授权；应先调整范围，再开始相应任务。

## 技术可行性与选择

`serve` 已集中装配会话、资料库、健康探针及优雅停服。建议注册独立 `/web/` 路由，用 Go `html/template`、本地 CSS 和少量渐进增强 JS，把资源编入二进制（[Go embed](https://pkg.go.dev/embed)）。这让现有源码构建和离线部署不依赖 Node、CDN 或字体服务。另一条可行路线是编译型 SPA；若后续交互原型显示确有需要，再比较构建、体积和第三方许可成本。

浏览器走同源会话，生产使用 HTTPS、HttpOnly/Secure/SameSite cookie；状态变更校验 CSRF，个人页面不被共享缓存。沿用 CLI 登录的 KDF 并发预算，服务端每次检查会话是否有效、操作是否授权。账号管理权仅限账号操作，不含直接浏览他人内容。已有 Bearer 路由不接收 Web cookie。依据：[现有鉴权](../../internal/auth/auth.go)、[OWASP 会话指南](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)。

## 视觉方向：用版本脉络表达科技感

| 一手参考 | 借鉴什么 | 在 Filecloud 的落点 |
|---|---|---|
| [Linear：界面重设计复盘](https://linear.app/now/how-we-redesigned-the-linear-ui) | 降低导航噪声，通过边界、对齐和密度建立层级；用完整流程检验方案 | 石墨底、冷青色仅强调当前选择或焦点；汉字用系统无衬线，哈希与数字等宽。资料库 → 提交 → 文件保持相同定位方式。 |
| [IBM Carbon：数据表](https://carbondesignsystem.com/components/data-table/usage/) | 统一行高、按需展开与分页，让密集信息可扫读 | 历史桌面版以一条轴标示正式 Head 的演进，合并来源折叠在对应提交下。窄屏保留版本、状态和操作，将次要字段收入详情。 |
| [GitLab Pajamas：Badge](https://design.gitlab.com/components/badge/) | 只突出有意义的状态，结合短文本及图标 | 区分「已发布」「合并来源」「存储未就绪」；颜色不是唯一提示，不把每项元数据都做成彩色标签。 |
| [W3C：WCAG 2.2](https://www.w3.org/TR/WCAG22/) | AA 对比度、重排、键盘、可见且不被遮挡的焦点 | 正文至少 4.5:1；键盘可走完整任务并回到触发位置；320 CSS px、200% 缩放、减少动画模式分别验收。 |
| [web.dev：Core Web Vitals](https://web.dev/articles/vitals) | 加载、交互和布局稳定性分别衡量 | 目标 LCP ≤2.5 s、INP ≤200 ms、CLS ≤0.1。发布前固定设备/网络和样本测实验室加载与交互；真实用户 p75 是不同口径，不为此引入遥测。 |

建议主界面围绕两个问题组织：**「服务现在能用吗？」** 看存活、就绪及检查时间；**「我要找的文件在哪个版本？」** 从资料库直达主线、合并来源和具体路径。拒绝无数据来源的总容量曲线、设备在线数及装饰性仪表盘。

视觉验收以同一组匿名示例贯穿：空库 → CLI 首次同步 → 两个设备产生合并来源 → 在更新时间接近的版本中找出指定文件 → 生成恢复预览命令。用该流程交付桌面/窄屏可点击原型、键盘走查、长中文路径和错误态实测截图；原型经维护者确认后才铺开功能页面。
