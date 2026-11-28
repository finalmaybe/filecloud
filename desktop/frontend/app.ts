import { Events, Clipboard, Window } from "/wails/runtime.js";
import {
  api,
  prefs,
  n,
  box,
  btn,
  icon,
  heading,
  line,
  section,
  empty,
  sub,
  tabs,
  toggle,
  search,
  filterRows,
  noMatch,
  tell,
  errorText,
  bytes,
  describe,
  attention,
  status,
  state,
  copy,
  dialog,
  date,
  pickRow,
  menu,
  type LibraryStats,
  type LibraryView,
  type View,
  type Icon,
} from "./ui.js";
import {
  login,
  wizard,
  createLibrary,
  bindLibrary,
  reviewDelete,
  reviewRestore,
  compare,
} from "./flows.js";
import { libraryDetail } from "./details.js";
const sidebar = document.getElementById("sidebar")!,
  main = document.getElementById("main")!;
let view: View,
  page = "libraries",
  selected = "",
  libTab = "全部",
  term = "",
  activityTab = "进行中",
  prefTab = "通用",
  detailTab = "文件",
  conflictTarget = "";
const libraries = () => view.Libraries || [];
function nameOf(l: LibraryView) {
  return libraries().some((x) => x.ID !== l.ID && x.Name === l.Name)
    ? `${l.Name} · ${l.ID.slice(0, 8)}`
    : l.Name;
}
function go(p: string) {
  page = p;
  tell("");
  render();
}
function openDetail(l: LibraryView, t: string) {
  selected = l.ID;
  detailTab = t;
  go("detail");
}
async function mutate(fn: () => Promise<unknown>) {
  try {
    await fn();
  } finally {
    await update(true);
  }
}
function folderAction(l: LibraryView) {
  return btn("打开文件夹", () => api.OpenWorktree(l.ID), "", "folder");
}
function syncRequiredAction(l: LibraryView) {
  if (l.Pending === "delete-confirmation")
    return btn("检查删除", () => mutate(() => reviewDelete(l)), "", "shield");
  if (l.Pending === "restore-confirmation")
    return btn("核对恢复", () => mutate(() => reviewRestore(l)), "", "history");
  if (l.Problem === "auth_required")
    return btn("重新登录", () => go("login"), "", "lock");
}
function requestSyncAction(l: LibraryView, label: string, cls = "") {
  const b = btn(label, () => mutate(() => api.RequestSync(l.ID)), cls, "sync");
  b.disabled = l.Running;
  return b;
}
function syncAction(l: LibraryView) {
  return (
    syncRequiredAction(l) ||
    requestSyncAction(l, l.Problem ? "立即重试" : "同步")
  );
}
function openBind(l: LibraryView) {
  return mutate(() => bindLibrary(l));
}
function drawSidebar() {
  sidebar.hidden = false;
  const nav = n("nav");
  nav.setAttribute("aria-label", "工作空间");
  const p =
    page === "detail" ? "libraries" : page === "conflicts" ? "attention" : page;
  const navBtn = (t: string, label: string, ic: Icon) => {
    const b = btn(label, () => go(t), "nav-button", ic);
    b.dataset.page = t;
    if (p === t) b.setAttribute("aria-current", "page");
    return b;
  };
  const count = libraries().filter(attention).length,
    issues = navBtn("attention", "需要处理", "issue");
  if (count) issues.append(n("span", "badge", String(count)));
  nav.append(
    navBtn("libraries", "资料库", "folder"),
    navBtn("activity", "同步活动", "sync"),
    issues,
  );
  let host = view.Server;
  if (host) host = new URL(host).host;
  const account = btn(
    "账号设置",
    () => {
      prefTab = "账号";
      go("settings");
    },
    "account",
  );
  account.replaceChildren(
    n("span", "avatar", (view.Username || "F").slice(0, 1).toUpperCase()),
    box(
      "account-info",
      n("strong", "", view.Username || "当前账号"),
      sub(host),
    ),
    n("span", view.CatalogSession ? "dot online" : "dot"),
  );
  sidebar.replaceChildren(
    box("brand", icon("folder", "blue"), n("strong", "", "Filecloud")),
    n("p", "nav-caption", "工作空间"),
    nav,
    n("footer", "", navBtn("settings", "设置", "settings"), account),
  );
}
function updateSessionWarning() {
  document.querySelector(".session-warning")?.remove();
  if (
    view.CatalogSession &&
    !libraries().some((l) => l.Problem === "auth_required")
  )
    return;
  sidebar.after(
    box(
      "session-warning",
      icon("lock", "orange"),
      box(
        "grow",
        n("strong", "", "需要重新登录，本地文件已保留。"),
        sub("登录后将再次检查原目录能否安全恢复同步。"),
      ),
      btn("重新登录", () => go("login"), "primary"),
    ),
  );
}
function recent(limit = 200) {
  const list = box("recent-list");
  for (const a of [...(view.Activity || [])].reverse().slice(0, limit)) {
    const l = libraries().find((l) => l.ID === a.LibraryID),
      success = ["completed", "no_change"].includes(a.Kind),
      scan = a.Kind === "scan_completed";
    const t = n(
      "time",
      "",
      new Date(a.Time).toLocaleTimeString("zh-CN", {
        hour: "2-digit",
        minute: "2-digit",
      }),
    );
    t.title = date(a.Time);
    list.append(
      box(
        "event",
        icon(
          success ? "check" : scan ? "sync" : "issue",
          success ? "green" : scan ? "blue" : "orange",
        ),
        n(
          "span",
          "",
          (l ? nameOf(l) : a.LibraryID.slice(0, 8)) + " · " + describe(a.Kind),
        ),
        t,
      ),
    );
  }
  if (!list.childElementCount) list.append(sub("还没有本机同步活动。"));
  return list;
}
const sizes = new Map<
  string,
  { stamp: string; value: Promise<LibraryStats> }
>();
function stats(l: LibraryView) {
  const stamp =
    [...(view.Activity || [])]
      .reverse()
      .find((a) => a.LibraryID === l.ID && a.Kind === "completed")?.Time || "";
  let cached = sizes.get(l.ID);
  if (!cached || cached.stamp !== stamp) {
    cached = { stamp, value: api.LibraryStats(l.ID) };
    sizes.set(l.ID, cached);
  }
  return cached.value;
}
async function fillSummaries(pane: HTMLElement, choose = false) {
  for (const el of pane.querySelectorAll<HTMLElement>("[data-summary]")) {
    if (!pane.isConnected) return;
    const l = libraries().find((x) => x.ID === el.dataset.summary);
    if (!l) continue;
    try {
      const s = await stats(l);
      el.textContent = choose
        ? s.Published
          ? `${bytes(s.Size)} · 已发布版本`
          : "尚未发布版本"
        : [
            bytes(s.Size),
            s.Items ? `${Number(s.Items).toLocaleString()} 个项目` : "",
          ]
            .filter(Boolean)
            .join(" · ");
    } catch {
      el.textContent = "资料库信息暂不可用";
    }
  }
}
function summary(l: LibraryView) {
  const el = sub("正在读取…");
  el.dataset.summary = l.ID;
  return el;
}
function showIssue(l: LibraryView) {
  selected = l.ID;
  if (l.Pending.endsWith("-confirmation"))
    return l.Pending === "delete-confirmation"
      ? mutate(() => reviewDelete(l))
      : mutate(() => reviewRestore(l));
  if (l.Conflicts?.length) {
    conflictTarget = l.Conflicts[0].Target;
    go("conflicts");
  } else go("attention");
}
function inspectorSyncStatus(l: LibraryView) {
  if (
    l.Running ||
    l.Problem ||
    (l.Pending && l.Pending !== "none") ||
    l.Conflicts?.length
  )
    return status({ ...l, Paused: false });
  return box(
    "status",
    l.Paused ? icon("pause") : icon("check", "green"),
    n("span", "", l.Paused ? "已暂停" : "自动同步已开启"),
  );
}
function inspectorSyncToggle(l: LibraryView) {
  const label = l.Paused ? "恢复自动同步" : "暂停自动同步";
  const toggle = btn(
    label,
    () => mutate(() => api.SetPaused(l.ID, !l.Paused)),
    "outline square sync-toggle",
    l.Paused ? "play" : "pause",
    true,
  );
  toggle.removeAttribute("title");
  const hint = n("span", "sync-toggle-hint", label);
  hint.setAttribute("aria-hidden", "true");
  toggle.append(hint);
  return toggle;
}
function inspector(l: LibraryView) {
  const side = n("aside", "inspector");
  const bound = l.Phase === "bound";
  const required = bound && !l.Running ? syncRequiredAction(l) : undefined;
  side.append(
    box(
      "inspector-head",
      icon("folder", "blue"),
      box(
        "",
        n("h2", "", nameOf(l)),
        sub(bound ? "已连接到此设备" : "尚未连接到本机"),
      ),
    ),
  );
  if (bound) {
    const syncOnce = requestSyncAction(l, "立即同步一次", "menu-link");
    if (required) syncOnce.disabled = true;
    side.append(
      box("actions", folderAction(l), libraryMenu(l, "更多 ⌄", syncOnce)),
    );
  } else {
    side.append(
      box(
        "actions",
        l.Phase === "binding"
          ? btn(
              "继续同步",
              () => mutate(() => api.RetryBinding(l.ID)),
              "primary",
              "folder",
            )
          : btn("同步该资料库", () => openBind(l), "primary", "folder"),
      ),
    );
  }
  if (l.Worktree)
    side.append(
      section(
        "本机位置",
        box("path", n("span", "", l.Worktree), copy(l.Worktree)),
      ),
    );
  if (bound) {
    const sync = section(
      "同步",
      box("sync-controls", inspectorSyncStatus(l), inspectorSyncToggle(l)),
    );
    if (required) sync.append(box("actions", required));
    else if (!l.Running && l.Problem)
      sync.append(box("actions", requestSyncAction(l, "立即重试")));
    else if (l.Running && !l.Paused)
      sync.append(sub("暂停在当前轮次结束后生效。"));
    side.append(sync);
  }
  const size = n("span", "", "正在读取…");
  void stats(l)
    .then((value) => {
      size.textContent = bytes(value.Size);
    })
    .catch(() => {
      size.textContent = "暂不可用";
    });
  side.append(
    section(
      "资料库",
      line("当前文件", size),
      line("最近成功同步", date(l.LastSuccess)),
    ),
    box(
      "inspector-links",
      ...(
        [
          ["浏览文件", "文件", "file"],
          ["版本历史", "版本历史", "history"],
          ["资料库设置", "设置", "settings"],
        ] as const
      ).map(([name, t, ic]) => {
        const b = btn(name, () => openDetail(l, t), "menu-link", ic);
        b.append(icon("chevron"));
        return b;
      }),
    ),
  );
  return side;
}
function libraryMenu(
  l: LibraryView,
  caption = "···",
  ...extraItems: HTMLElement[]
) {
  return menu(
    nameOf(l) + "的更多操作",
    [
      ...extraItems,
      btn("浏览文件", () => openDetail(l, "文件"), "menu-link", "file"),
      btn("版本历史", () => openDetail(l, "版本历史"), "menu-link", "history"),
      btn("资料库设置", () => openDetail(l, "设置"), "menu-link", "settings"),
    ],
    caption,
  );
}
function libraryPage() {
  const libs = libraries(),
    bound = libs.filter((l) => l.Phase === "bound");
  const pane = n("section", "pane library-pane"),
    root = box("workspace", pane);
  const table = box("lib-table"),
    visible = libs.filter(
      (l) =>
        libTab === "全部" || (libTab === "已连接") === (l.Phase === "bound"),
    );
  const select = (l: LibraryView) => {
    selected = l.ID;
    root.querySelector("aside")?.remove();
    root.append(inspector(l));
    for (const r of table.querySelectorAll<HTMLElement>("[data-library]"))
      r.classList.toggle("selected", r.dataset.library === selected);
  };
  const apply = () => {
    filterRows(table, term);
    const rows = Array.from(
      table.querySelectorAll<HTMLElement>("[data-library]"),
    ).filter((r) => !r.hidden);
    const l =
      visible.find(
        (l) =>
          rows.some((r) => r.dataset.library === l.ID) && l.ID === selected,
      ) || visible.find((l) => l.ID === rows[0]?.dataset.library);
    if (l) select(l);
    else root.querySelector("aside")?.remove();
  };
  const input = search("筛选资料库", term, (t) => {
    term = t;
    apply();
  });
  pane.append(
    heading(
      "资料库",
      `${libs.length} 个资料库 · ${bound.length} 个已连接到本机${view.More ? " · 已加载部分" : ""}`,
      input,
      btn(
        "新建资料库",
        () => mutate(() => createLibrary(view)),
        "primary",
        "plus",
      ),
    ),
    tabs(["全部", "已连接", "未连接"], libTab, (t) => {
      libTab = t;
      render();
    }),
  );
  const tools = box(
    "lib-cols table-head",
    n("span", "", "名称"),
    n("span", "", "本机文件夹"),
    n("span", "", "同步状态"),
    btn(
      "刷新资料库",
      () => {
        sizes.clear();
        return mutate(() => api.Refresh(false));
      },
      "bare",
      "sync",
      true,
    ),
  );
  pane.append(tools, table);
  for (const l of visible) {
    const row = box(
      "lib-cols library-row",
      box(
        "identity",
        icon("folder", "blue"),
        box(
          "",
          n("strong", "", nameOf(l)),
          l.Deleted
            ? sub(`${l.Deleted} 项删除待确认`)
            : l.Conflicts?.length
              ? sub(`${l.Conflicts.length} 个冲突文件待整理`)
              : summary(l),
        ),
      ),
      n("span", "local-path", l.Worktree || "尚未连接到本机"),
      box(
        "row-state",
        status(l),
        ...(l.Running
          ? [n("progress", "indeterminate")]
          : l.LastSuccess && !attention(l)
            ? [sub(date(l.LastSuccess))]
            : []),
        ...(l.Phase === ""
          ? [btn("同步该资料库", () => openBind(l))]
          : attention(l)
            ? [btn("查看", () => showIssue(l), "link")]
            : []),
      ),
      libraryMenu(l),
    );
    row.dataset.library = l.ID;
    row.dataset.match = l.Name + " " + l.Worktree;
    pickRow(row, () => select(l), nameOf(l));
    row.ondblclick = (e) => {
      if (!(e.target as Element).closest("button,details"))
        openDetail(l, "文件");
    };
    table.append(row);
  }
  if (visible.length) {
    table.append(noMatch());
    apply();
  } else
    table.append(
      empty(
        libs.length ? "此分组暂无资料库" : "还没有资料库",
        "新建资料库，或刷新以查看服务器上的资料库。",
      ),
    );
  if (view.More)
    pane.append(
      btn("加载更多资料库", () => mutate(() => api.Refresh(true)), "more"),
    );
  if (!view.CatalogFresh)
    pane.append(sub("显示本机记录，远端资料库尚未刷新。"));
  pane.append(section("近期活动", recent(3)));
  main.replaceChildren(root);
  void fillSummaries(root);
}
function activities() {
  const issues = page === "attention",
    libs = libraries(),
    bound = libs.filter((l) => l.Phase === "bound"),
    allPaused = bound.length > 0 && bound.every((l) => l.Paused);
  const pane = n("section", "pane");
  pane.append(
    heading(
      issues ? "需要处理" : "同步活动",
      issues
        ? "核对需要判断的变更，以及下一步操作。"
        : "当前任务与最近发生的变化",
      ...(!issues && bound.length
        ? [
            btn(
              allPaused ? "继续自动检查" : "暂停自动检查",
              () => mutate(() => api.PauseAll(!allPaused)),
              "",
              "pause",
            ),
          ]
        : []),
    ),
  );
  if (!issues)
    pane.append(
      tabs(
        ["进行中", "最近活动"],
        activityTab,
        (t) => {
          activityTab = t;
          render();
        },
        "segments",
      ),
    );
  if (issues || activityTab === "进行中") {
    const items = issues
      ? libs
          .filter(attention)
          .sort((a, b) => Number(b.ID === selected) - Number(a.ID === selected))
      : bound;
    if (!items.length)
      pane.append(
        empty(
          issues ? "没有需要处理的事项" : "暂无本机任务",
          issues
            ? "需要核对的删除、恢复与冲突文件会显示在这里。"
            : "同步该资料库后，可在这里查看同步状态。",
          issues ? "check" : "sync",
        ),
      );
    for (const l of items) {
      const [ic, text, cls] = state(l),
        actions = box("actions");
      if (l.Phase === "binding")
        actions.append(
          btn("继续同步", () => mutate(() => api.RetryBinding(l.ID))),
          ...(l.CanReselect
            ? [btn("选择其他文件夹", () => openBind(l))]
            : [sub("请先完成当前同步。")]),
        );
      else if (l.Problem === "auth_required")
        actions.append(btn("重新登录", () => go("login"), "primary"));
      else if (l.Phase === "bound")
        actions.append(
          syncAction(l),
          btn(
            l.Paused ? "继续" : "暂停后续同步",
            () => mutate(() => api.SetPaused(l.ID, !l.Paused)),
            "outline",
          ),
        );
      actions.append(libraryMenu(l));
      pane.append(
        box(
          "task" + (l.Running ? " selected" : ""),
          icon(ic, cls),
          box(
            "grow",
            n("strong", "", nameOf(l)),
            sub(
              l.Deleted
                ? `${l.Deleted} 个路径待核对`
                : l.Running && l.Scanned
                  ? "已完成本地扫描"
                  : l.Problem === "offline" ||
                      l.Problem === "dav_locked" ||
                      l.Problem === "retry_later"
                    ? "本地修改已保留"
                    : l.Paused
                      ? "后续自动检查已暂停"
                      : "最近成功同步 · " + date(l.LastSuccess),
            ),
          ),
          n("span", "", text),
          actions,
        ),
      );
      if (issues)
        for (const p of l.Conflicts || [])
          pane.append(
            box(
              "task",
              icon("shield", "blue"),
              box("grow", n("strong", "", "已保留两个版本"), sub(p.Target)),
              btn("对照内容", () => {
                selected = l.ID;
                conflictTarget = p.Target;
                go("conflicts");
              }),
            ),
          );
    }
  }
  if (!issues)
    pane.append(
      section("最近活动", recent(activityTab === "进行中" ? 4 : 200)),
    );
  main.replaceChildren(pane);
}
async function editIgnoreRules() {
  const rules = await api.IgnoreRules();
  const hidden = toggle("同步隐藏文件（名称以 . 开头）", !rules.SkipHidden);
  const patterns = n("textarea");
  patterns.rows = 6;
  patterns.spellcheck = false;
  patterns.value = (rules.Patterns || []).join("\n");
  patterns.placeholder = "*.tmp\nnode_modules";
  const saved = await dialog(
    "同步忽略规则",
    [
      sub("适用于本机所有资料库。从下一轮同步起，匹配项将跳过上传和下载，已有副本会保留。"),
      hidden.label,
      n("label", "field", n("span", "", "忽略的名称（每行一条）"), patterns),
      sub("只填名称，不填路径。*.tmp 匹配任意目录下的临时文件；匹配文件夹时会跳过整个文件夹。删除一行即可恢复该项同步。"),
    ],
    "保存",
    {
      onAccept: () =>
        api.SetIgnoreRules({
          SkipHidden: !hidden.input.checked,
          Patterns: patterns.value.split("\n").map((s) => s.trim()).filter(Boolean),
        }),
    },
  );
  if (saved) tell("已保存同步忽略规则。");
}
function settings() {
  const p = n("section", "pane settings");
  p.append(
    heading("设置"),
    tabs(["通用", "账号", "通知", "诊断"], prefTab, (t) => {
      prefTab = t;
      render();
    }),
  );
  const row = (ic: Icon, t: string, ...controls: (Node | string)[]) =>
    box("setting-row", icon(ic), n("span", "grow", t), ...controls);
  const sw = (
    label: string,
    on: boolean,
    apply: (on: boolean) => Promise<unknown>,
  ) => {
    const b = btn(
      label,
      async () => {
        await apply(!on);
        render();
      },
      "switch",
    );
    b.textContent = "";
    b.setAttribute("role", "switch");
    b.setAttribute("aria-checked", String(on));
    return b;
  };
  if (prefTab === "通用") {
    const auto = row("power", "登录时启动 Filecloud", sub("正在读取…"));
    p.append(
      section(
        "通用",
        auto,
        row(
          "device",
          "关闭窗口后继续在后台运行",
          btn(
            "已开启",
            () =>
              dialog(
                "后台运行",
                [
                  sub(
                    "关闭主窗口后，Filecloud 继续同步。要停止后台运行，请在快捷菜单选择「退出 Filecloud」。",
                  ),
                ],
                "",
                { cancel: "关闭" },
              ),
            "bare",
            "chevron",
          ),
        ),
        row(
          "sync",
          "自动检查",
          btn(
            "每轮结束 30 秒后",
            () => {
              activityTab = "进行中";
              go("activity");
            },
            "bare",
            "chevron",
          ),
        ),
        row("folder", "同步忽略规则", btn("编辑规则…", editIgnoreRules)),
      ),
    );
    void prefs
      .AutoStart()
      .then((on) =>
        auto.lastChild?.replaceWith(
          sw("登录时启动 Filecloud", on, prefs.SetAutoStart),
        ),
      )
      .catch((e) => auto.lastChild?.replaceWith(sub(errorText(e))));
  }
  if (prefTab === "账号")
    p.append(
      section(
        "账号",
        box(
          "setting-row",
          n("span", "avatar", (view.Username || "F").slice(0, 1).toUpperCase()),
          n("strong", "grow", view.Username || "当前账号"),
          btn("管理登录", () => go("login")),
        ),
        row(
          "folder",
          "服务器",
          n("span", "setting-value", view.Server),
          btn(
            "退出账号",
            async () => {
              if (
                await dialog(
                  "退出当前账号？",
                  [sub("停止此账号的自动同步并撤销当前会话，保留本地文件。")],
                  "退出账号",
                )
              )
                await mutate(() => api.Logout());
            },
            "link",
          ),
        ),
        row(
          "device",
          "设备",
          n("span", "setting-value", view.DeviceName || "此设备"),
        ),
      ),
    );
  if (prefTab === "通知") {
    const notification = section("通知");
    p.append(notification);
    void prefs
      .NotificationsAllowed()
      .then((ok) => {
        for (const [label, on, isIssues] of [
          ["需要处理时通知我", !view.MuteIssues, true],
          ["同步完成通知", view.NotifyDone, false],
        ] as const)
          notification.append(
            row(
              "bell",
              label,
              sw(label, on && ok, async (enable) => {
                if (enable && !ok && !(await prefs.EnableNotifications())) {
                  tell("系统未允许通知，仍可在「需要处理」查看。");
                  return;
                }
                await api.NotificationPolicy(
                  isIssues ? enable : !view.MuteIssues,
                  isIssues ? view.NotifyDone : enable,
                );
                await update();
              }),
            ),
          );
      })
      .catch((e) => notification.append(sub(errorText(e))));
  }
  if (prefTab === "诊断") {
    const l = libsSelected();
    p.append(
      section(
        "高级",
        row(
          "lock",
          "WebDAV 应用密码",
          btn(
            "在网页中管理",
            async () => {
              const libs = libraries();
              if (!libs.length) {
                tell("请先选择或创建一个资料库。");
                return;
              }
              const sel = n("select");
              sel.setAttribute("aria-label", "资料库");
              for (const x of libs) {
                const o = n("option", "", nameOf(x));
                o.value = x.ID;
                sel.append(o);
              }
              sel.value = l?.ID || libs[0].ID;
              if (
                await dialog(
                  "按资料库管理应用密码",
                  [sel, sub("将在系统浏览器中打开服务器网页，需要独立登录。")],
                  "打开网页",
                )
              )
                await api.AppPasswords(sel.value);
            },
            "",
            "external",
          ),
        ),
        row(
          "info",
          "诊断信息",
          btn("复制诊断信息", async () => {
            await Clipboard.SetText(
              `Filecloud Desktop ${view.Version}\n已加载资料库：${libraries().length}\n需要处理：${libraries().filter(attention).length}`,
            );
            tell("已复制诊断摘要（不含账号、路径或凭据）。");
          }),
        ),
        row(
          "info",
          "关于 Filecloud",
          btn(
            "版本信息",
            () =>
              dialog(
                "关于 Filecloud",
                [
                  line("桌面客户端版本", view.Version),
                  sub("本机同步使用随包的同版本 filecloud-client。"),
                ],
                "",
                { cancel: "关闭" },
              ),
            "bare",
            "chevron",
          ),
        ),
        row(
          "power",
          "结束后台运行",
          btn("退出应用", async () => {
            if (
              await dialog(
                "退出 Filecloud？",
                [sub("等待当前轮次完成后退出。仅关闭窗口可继续后台同步。")],
                "退出应用",
              )
            )
              await prefs.Quit();
          }),
        ),
      ),
    );
  }
  main.replaceChildren(p);
}
function libsSelected() {
  return libraries().find((l) => l.ID === selected) || libraries()[0];
}
function choose() {
  const libs = libraries().filter((l) => l.Phase !== "bound"),
    chosen = libs.find((l) => l.ID === selected) || libs[0],
    list = box("choices");
  for (const l of libs) {
    const b = btn(
      nameOf(l),
      () => {
        selected = l.ID;
        render();
      },
      chosen === l ? "choice selected" : "choice",
    );
    b.setAttribute("aria-pressed", String(l === chosen));
    b.replaceChildren(
      icon("folder", "blue"),
      box("grow", n("strong", "", nameOf(l)), summary(l)),
    );
    list.append(b);
  }
  if (!libs.length) list.append(sub("还没有可连接的资料库。"));
  const next = btn(
    "继续",
    async () => {
      if (chosen && (await bindLibrary(chosen))) {
        selected = chosen.ID;
        page = "libraries";
        await update(true);
      }
    },
    "primary",
  );
  next.disabled = !chosen;
  main.replaceChildren(
    box(
      "wizard-host",
      wizard(
        "选择要同步的资料库",
        "选择要同步到本地的资料库。你也可以新建一个资料库用于分类存放文件。",
        ["选择资料库", "本机文件夹", "确认"],
        0,
        [
          list,
          btn(
            "新建资料库",
            () => mutate(() => createLibrary(view)),
            "link",
            "plus",
          ),
          ...(view.More
            ? [btn("加载更多资料库", () => mutate(() => api.Refresh(true)))]
            : []),
        ],
        [btn("取消", () => go("libraries")), next],
      ),
    ),
  );
  void fillSummaries(list, true);
}
let compactWindow: boolean | undefined;
function render() {
  if (!view) return;
  document.querySelector(".session-warning")?.remove();
  const onboarding = !view.SignedIn || ["choose", "login"].includes(page);
  if (compactWindow !== undefined && compactWindow !== onboarding) {
    const width = onboarding ? 830 : 1440,
      height = onboarding ? 450 : 900;
    void Window.SetSize(
      Math.min(screen.availWidth, width),
      Math.min(screen.availHeight, height),
    )
      .then(() => Window.Center())
      .catch(() => {});
  }
  compactWindow = onboarding;
  sidebar.hidden = onboarding;
  main.classList.toggle("first-run", onboarding);
  if (view.LogoutPending) {
    main.replaceChildren(
      box(
        "wizard-host",
        wizard(
          "退出尚未完成",
          "本机已停止同步，远端注销尚未完成。",
          [],
          0,
          [],
          [
            btn("仅忘记本机会话", async () => {
              if (
                await dialog(
                  "仅清除本机会话？",
                  [sub("远端会话可能仍有效，本机将不再重试撤销。")],
                  "清除",
                )
              )
                await mutate(() => api.ForgetUnavailableSession());
            }),
            btn("重试注销", () => mutate(() => api.Logout()), "primary"),
          ],
        ),
      ),
    );
    return;
  }
  if (!view.SignedIn || page === "login") {
    const first = !view.SignedIn,
      refreshBindings = libraries().filter(
        (l) => l.Phase === "bound" && l.Problem === "auth_required",
      );
    main.replaceChildren(
      login(
        view,
        async () => {
          page = first ? "choose" : "libraries";
          let issue = "";
          for (const l of refreshBindings) {
            try {
              await api.RetryBinding(l.ID);
            } catch (e) {
              selected = l.ID;
              detailTab = "设置";
              page = "detail";
              issue =
                errorText(e) +
                " 原目录已保留。请先保存本地修改，再解除同步，选择「和已有文件夹同步」或选择新的空文件夹。";
              break;
            }
          }
          await update(true);
          if (issue) tell(issue);
        },
        !first ? () => go("settings") : undefined,
      ),
    );
    return;
  }
  if (page === "choose") {
    choose();
    return;
  }
  drawSidebar();
  updateSessionWarning();
  if (page === "conflicts") {
    const l = libsSelected(),
      pair = l?.Conflicts?.find((p) => p.Target === conflictTarget);
    if (l && pair)
      main.replaceChildren(
        compare(l, pair, async () => {
          page = "attention";
          await update(true);
        }),
      );
    else go("attention");
  } else if (page === "libraries") libraryPage();
  else if (page === "settings") settings();
  else if (page === "detail") {
    const l = libsSelected();
    if (l)
      main.replaceChildren(
        libraryDetail(
          l,
          detailTab,
          view,
          (t) => {
            detailTab = t;
          },
          () => go("libraries"),
          async () => {
            page = "libraries";
            await update(true);
          },
        ),
      );
    else go("libraries");
  } else activities();
}
async function update(force = false) {
  const next = await api.Snapshot();
  if (!next || (!force && view?.Revision === next.Revision)) return;
  const sessionChanged =
    !view ||
    view.SignedIn !== next.SignedIn ||
    view.LogoutPending !== next.LogoutPending;
  const notice = next.Notice !== view?.Notice ? next.Notice : "";
  if (
    !force &&
    !sessionChanged &&
    (document.querySelector("dialog[open],details.row-menu[open]") ||
      document.activeElement?.matches("input,select"))
  )
    return;
  view = next;
  if (!next.SignedIn) {
    selected = "";
    term = "";
    libTab = "全部";
    page = "libraries";
    sizes.clear();
  }
  if (
    force ||
    sessionChanged ||
    !["detail", "conflicts", "settings", "login", "choose"].includes(page)
  ) {
    const focus = document.activeElement;
    const label = focus?.getAttribute("aria-label"),
      lib = focus?.closest<HTMLElement>("[data-library]")?.dataset.library;
    const syncToggleLibrary =
      page === "libraries" && focus?.matches(".sync-toggle:focus-visible")
        ? selected
        : null;
    render();
    if (!sessionChanged && syncToggleLibrary !== null) {
      if (page === "libraries" && selected === syncToggleLibrary)
        main
          .querySelector<HTMLButtonElement>(".sync-toggle")
          ?.focus({ preventScroll: true });
    } else if (!force && !sessionChanged && label) {
      const match = Array.from(
        document.querySelectorAll<HTMLElement>("[aria-label]"),
      ).find(
        (x) =>
          x.getAttribute("aria-label") === label &&
          x.closest<HTMLElement>("[data-library]")?.dataset.library === lib,
      );
      match?.focus({ preventScroll: true });
    }
  } else if (!sidebar.hidden) {
    drawSidebar();
    updateSessionWarning();
  }
  if (notice) tell(notice);
}
Events.On("quick-open", (e: { data: unknown }) => {
  if (document.querySelector("dialog[open]")) return;
  const target = e.data as { page: string; id: string };
  if (["libraries", "attention", "settings"].includes(target.page)) {
    if (target.id) {
      selected = target.id;
      libTab = "全部";
      term = "";
    }
    go(target.page);
  }
});
Events.On("attention", (e: { data: unknown }) => {
  if (!document.querySelector("dialog[open]")) {
    selected = String(e.data);
    go("attention");
  }
});
Events.On("navigate", (e: { data: unknown }) => {
  if (
    !document.querySelector("dialog[open]") &&
    ["attention", "activity", "settings"].includes(String(e.data))
  )
    go(String(e.data));
});
void update().catch(() => tell("无法连接桌面主进程。"));
setInterval(() => {
  if (document.visibilityState === "visible")
    void update().catch(() => tell("无法连接桌面主进程。"));
}, 1500);
