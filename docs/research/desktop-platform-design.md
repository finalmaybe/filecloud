# 桌面同步客户端：三端设计依据

核对日期：2026-11-22。问题是：用户如何在本机找到资料库、理解一次同步、决定是否发布删除，而不是怎样让三个窗口看起来完全相同。以下是官方资料与本项目的取舍；产品范围见 [PRD](../design/desktop-client-prd.md)，工程约束见[技术方案](../design/desktop-client-architecture.md)。

| 平台及一手资料 | 官方资料说了什么 | Filecloud 如何使用、如何验收 |
|---|---|---|
| Linux / GNOME：[Header Bars](https://developer.gnome.org/hig/patterns/containers/header-bars.html)、[Sidebars](https://developer.gnome.org/hig/patterns/nav/sidebars.html)、[Dialogs](https://developer.gnome.org/hig/patterns/feedback/dialogs.html) | 标题栏容纳少量上下文操作，侧栏切换位置；破坏性操作应允许撤销或要求确认，对话框由用户动作触发。 | 保留「添加绑定」和菜单，列表中选库、详情中打开工作目录。删除会传播到其他设备，没有原地撤销，采用主动核对。后台发现删除时只标记，不抢焦点。GNOME 是本轮参考桌面，不用它代表所有 Linux 桌面。 |
| GNOME：[通知](https://developer.gnome.org/hig/patterns/feedback/notifications.html)、[使用通知](https://developer.gnome.org/documentation/tutorials/notifications.html) | 通知可以被用户关闭；点击后应回到相关界面。 | 即使禁用通知、没有托盘扩展，也能从应用列表重开原窗口，在「需要处理」找到同一事项。 |
| Windows：[Title bar](https://learn.microsoft.com/en-us/windows/apps/develop/ui/controls/title-bar)、[NavigationView](https://learn.microsoft.com/en-us/windows/apps/design/controls/navigationview)、[Dialog controls](https://learn.microsoft.com/en-us/windows/apps/develop/ui/controls/dialogs-and-flyouts/dialogs) | 系统标题栏负责移动窗口和窗口按钮；导航可随宽度收起；对话框应提供安全退出动作，Enter 会执行默认按钮。 | 保留系统标题栏、最大化和贴靠。窄窗从列表进入详情再返回，保持选中项。确认框刚打开时按 Enter 不发布；用键盘明确选中「确认同步删除」后才可执行。这里借鉴 Fluent 行为，不要求引入 WinUI。 |
| macOS：[Sidebars](https://developer.apple.com/design/human-interface-guidelines/sidebars)、[Sheets](https://developer.apple.com/design/human-interface-guidelines/sheets)、[Alerts](https://developer.apple.com/design/human-interface-guidelines/alerts)、[The menu bar](https://developer.apple.com/design/human-interface-guidelines/the-menu-bar) | Sheet 绑定父窗口并保留上下文；破坏性确认给出取消路径；系统菜单区分关闭窗口与退出 App。 | 从指定资料库打开核对面板，不锁住其他应用。若使用原生警告，安全按钮按系统本地化为「取消」，语义等同预览的「暂不处理」；不设默认删除。测试 Esc/⌘. 取消、⌘W 关窗、⌘Q 退出和 Dock 重开。 |
| 同类产品：[Seafile – Install and sync Files](https://help.seafile.com/syncing_client/install_sync/)、[Nextcloud – Using the Synchronization Client](https://docs.nextcloud.com/server/stable/user_manual/en/desktop/usage.html) | Seafile 从资料库关联本地目录；Nextcloud 在主界面呈现近期活动、异常以及打开本地文件夹的入口。 | 采用「资料库 → 工作目录」和「活动 → 处理问题」。明确区分本机活动与服务端主线历史；不照搬共享、协作、容量面板或虚拟盘。 |

三个平台复用[同一品牌](../../assets/brand/filecloud-logo.svg)和[同一流程](../design/desktop-mockups/filecloud-desktop-design.pdf)。图中的系统只是三个观察位置，不是功能分配。边角、按钮顺序、菜单位置可以不同；确认对象、数量、取消效果和发布后果必须一致。
