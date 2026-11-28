## Agent skills

### Issue tracker

议题与规格在本仓库的 GitHub Issues 中管理。参见 `docs/agents/issue-tracker.md`。

### Triage labels

使用五个默认 triage 标签：`needs-triage`、`needs-info`、`ready-for-agent`、`ready-for-human`、`wontfix`。参见 `docs/agents/triage-labels.md`。

### Domain docs

采用 single-context 布局：根目录 `CONTEXT.md` 和 `docs/adr/`。参见 `docs/agents/domain.md`。

### Cross-platform acceptance hosts

在 macOS/APFS 或 Windows/NTFS 上验收前，先按[跨平台验收主机指南](docs/agents/acceptance-hosts.md)连接目标系统，再在目标系统的本地磁盘上运行测试。

## Desktop

- 修改desktop客户端后，在交付给用户前重新打包。
