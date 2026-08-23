## Agent skills

### Issue tracker

议题与规格在本仓库的 GitHub Issues 中管理。参见 `docs/agents/issue-tracker.md`。

### Triage labels

使用五个默认 triage 标签：`needs-triage`、`needs-info`、`ready-for-agent`、`ready-for-human`、`wontfix`。参见 `docs/agents/triage-labels.md`。

### Domain docs

采用 single-context 布局：根目录 `CONTEXT.md` 和 `docs/adr/`。参见 `docs/agents/domain.md`。

### Cross-platform acceptance hosts

需要在 macOS/APFS 或 Windows/NTFS 实机验收时，先读取本地文件 `.pi/acceptance-hosts.md`。该文件包含当前环境的 SSH 地址和操作命令，仅用于本机 agent，不提交到仓库。
