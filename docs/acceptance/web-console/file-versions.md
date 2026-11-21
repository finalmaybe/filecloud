# 文件列表、预览与单文件版本操作验收

被测提交：`06562bb`。环境：Linux amd64 / 本地 ext4、Go 1.26.6、Chromium 157。服务端与 CLI 均从该提交构建，浏览器通过回环 HTTP 访问真实服务；使用临时账号、资料库和工作目录。

## 浏览器与绑定设备闭环

| 操作 | 实际结果 |
|---|---|
| 用 CLI 绑定空库，先后将文本 `first`、`second` 写入同一文件并各同步一次。在网页打开资料库，点击文件名。 | 文件列表就地打开预览，显示 `second`；按 Esc 回到原列表。 |
| 从该文件的「更多操作 → 历史版本」打开版本栏，选择较早的 `first` 并确认恢复。 | 网页提示「已将所选版本恢复为当前版本」。变更记录从两条增至三条；绑定设备运行一次 `library sync` 后读到 `first`。 |
| 返回文件列表，再次选择最早的 `first` 记录并确认恢复。 | 页面提示「文件已与所选版本相同」，仍有三条记录。 |
| 在版本栏确认删除最早的记录。 | 侧栏就地提示「版本已删除」，剩两条记录。再次同步后，设备的 Sync Base 与首次恢复后相同，文件内容仍是 `first`。 |

## HTTP 和页面处理器回归

在仓库根目录执行以下命令，四项测试均通过：

```bash
GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/webconsole \
  -run '^(TestFileListOpensFileScopedVersions|TestPublishedFilePreviews|TestFileVersionActions|TestSharedLibraryAPIWithCookieJar)$'
```

其中还检查了固定提交的目录与文件版本、文本预览截断、图片和 PDF 响应、下载文件名和字节、跨所有者隔离、过期锚点拒绝写入，以及拒绝删除当前版本。

本次浏览器走查范围是 Chromium 上的文本预览、恢复和删除。[三引擎走查及性能样本](browser.md)对应旧「图谱」界面；[三平台发布候选验收](release.md)对应 `7d8c9c8`。
