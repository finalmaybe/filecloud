import * as api from "./bindings/github.com/mingming-cn/filecloud/desktop/workspace.js";
import * as prefs from "./bindings/github.com/mingming-cn/filecloud/desktop/preferences.js";
import type {
  LibraryView,
  View,
} from "./bindings/github.com/mingming-cn/filecloud/desktop/models.js";
import { Events } from "/wails/runtime.js";

const el = <T extends HTMLElement>(id: string) =>
  document.getElementById(id) as T;
const content = el("content"),
  dialog = el<HTMLDialogElement>("modal");
let view: View | null = null,
  page = "libraries",
  selected = "",
  busy = false,
  message = "";
const labels: Record<string, string> = {
  no_change: "本轮同步无变化",
  import_required: "本地目录非空；请选择空目录，或明确同意首次导入。",
  no_common_base: "远端已有版本且本机目录非空，请改选空目录。",
  scan_completed: "已完成本地扫描",
  completed: "本次同步已完成",
  delete_confirmation: "删除保护等待确认",
  stale_candidate: "变更已更新，请重新核对",
  auth_required: "需要重新登录并刷新绑定凭据",
  busy: "其他操作正在进行",
  offline: "无法联系服务",
  retry_later: "服务暂不可用",
  interrupted: "结果未知，请检查本机状态",
  unknown: "操作未完成，请检查后重试",
  missing: "目录或文件不可用",
  unsupported_fs: "文件系统不受支持",
  status_unavailable: "本机状态不可读，请检查目录",
  none: "无待处理操作",
  sync: "待继续同步",
  checkout: "待继续落盘",
  restore: "待继续已确认恢复",
  "delete-confirmation": "删除保护等待确认",
  "restore-confirmation": "历史恢复待确认，请回 CLI 处理",
};
function node<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  txt = "",
  cls = "",
) {
  const n = document.createElement(tag);
  n.textContent = txt;
  if (cls) n.className = cls;
  return n;
}
function tell(t: string) {
  message = t;
  el("notice").textContent = t || view?.Notice || "";
}
async function action(f: () => Promise<unknown>) {
  if (busy) return;
  busy = true;
  tell("");
  try {
    await f();
  } catch (e) {
    const m = e instanceof Error ? e.message : String(e);
    tell(labels[m] || m);
  } finally {
    busy = false;
    await update();
  }
}
function button(txt: string, fn: () => Promise<unknown>, cls = "") {
  const n = node("button", txt, cls);
  n.onclick = () => void action(fn);
  return n;
}
function question(
  title: string,
  body: HTMLElement[],
  yes: string,
  danger = false,
) {
  return new Promise<boolean>((resolve) => {
    el("modal-title").textContent = title;
    el("modal-body").replaceChildren(...body);
    const cancel = node("button", "暂不处理");
    cancel.autofocus = true;
    const accept = node("button", yes, danger ? "danger" : "primary");
    cancel.onclick = () => dialog.close("cancel");
    accept.onclick = () => dialog.close("yes");
    el("modal-actions").replaceChildren(cancel, accept);
    dialog.returnValue = "";
    dialog.onclose = () => resolve(dialog.returnValue === "yes");
    dialog.showModal();
    cancel.focus();
  });
}
function field(label: string, type = "text", value = "") {
  const l = node("label", label),
    i = node("input");
  i.type = type;
  i.value = value;
  i.required = true;
  l.append(i);
  return { l, i };
}
function loginForm() {
  const f = node("form", "", "formpanel");
  const host = field("服务地址", "url", view?.Server || ""),
    user = field("账号"),
    key = field("密码", "password"),
    device = field("设备名称", "text", "桌面客户端");
  host.i.placeholder = "https://";
  key.i.autocomplete = "current-password";
  user.i.autocomplete = "username";
  const submit = node("button", "登录", "primary");
  f.append(
    node("h2", "连接到 Filecloud"),
    host.l,
    user.l,
    key.l,
    device.l,
    submit,
  );
  f.onsubmit = (e) => {
    e.preventDefault();
    const pass = key.i.value;
    key.i.value = "";
    void action(async () => {
      await api.Login(host.i.value, user.i.value, pass, device.i.value);
      page = "libraries";
      render();
    });
  };
  return f;
}
function attention(l: LibraryView) {
  return (
    l.Phase === "binding" ||
    !!l.Problem ||
    ["delete-confirmation", "restore-confirmation"].includes(l.Pending)
  );
}
function status(l: LibraryView) {
  return (
    [
      l.Running ? "同步中" : "",
      l.Problem && labels[l.Problem],
      l.Paused ? "已暂停" : "",
      l.Pending && labels[l.Pending],
      l.Phase === "binding" ? "绑定未完成" : "",
    ]
      .filter(Boolean)
      .filter((x, i, a) => a.indexOf(x) === i)
      .join(" · ") || "未绑定"
  );
}
function libName(l: LibraryView) {
  return (view?.Libraries || []).some((o) => o.ID !== l.ID && o.Name === l.Name)
    ? `${l.Name} · ${l.ID.slice(0, 8)}`
    : l.Name;
}
async function bind(l: LibraryView) {
  const path = await api.PickDirectory(l.ID);
  if (!path) return;
  const check = node("input");
  check.type = "checkbox";
  const opt = node(
    "label",
    "若目录非空，将本机文件首次导入从未发布过版本的资料库",
  );
  opt.prepend(check);
  if (
    await question(
      "绑定工作目录",
      [
        node("strong", libName(l)),
        node("p", path),
        opt,
        node("p", "已有远端版本时请选择空目录。取消不会开始绑定。", "sub"),
      ],
      "绑定目录",
    )
  )
    await api.Bind(l.ID, path, check.checked);
}
async function review(l: LibraryView) {
  const r = await api.ReviewDeletion(l.ID);
  if (!r) return;
  if (
    await question(
      "核对删除影响",
      [
        node("strong", libName(l)),
        node("p", r.Worktree, "sub"),
        node("p", `待删除路径：${r.Deleted} / ${r.Tracked}`, "stat"),
        node(
          "p",
          "确认后，这些删除会发布到资料库，并在其他设备下次同步时生效。",
        ),
      ],
      "确认同步删除",
      true,
    )
  )
    await api.ConfirmDeletion(r);
  else await api.CancelReview();
}
function detail(l: LibraryView) {
  const d = node("section", "", "detail"),
    bar = node("div", "", "actions");
  d.append(
    node("h2", libName(l)),
    node("p", l.Worktree || "尚未选择本机工作目录"),
    node("p", status(l), "sub"),
    bar,
  );
  if (l.Phase === "bound") {
    bar.append(button("打开工作目录", () => api.OpenWorktree(l.ID)));
    if (l.Pending === "delete-confirmation")
      bar.append(button("查看影响", () => review(l), "primary"));
    else if (l.Pending !== "restore-confirmation")
      bar.append(button("立即同步", () => api.RequestSync(l.ID), "primary"));
    bar.append(
      button(l.Paused ? "继续自动同步" : "暂停自动同步", () =>
        api.SetPaused(l.ID, !l.Paused),
      ),
      button("版本历史", async () => {
        selected = l.ID;
        page = "history";
        await history(l);
      }),
    );
    const extra = node("details");
    extra.append(
      node("summary", "绑定操作"),
      button("刷新绑定凭据", () => api.RetryBinding(l.ID)),
    );
    if (!l.Running && l.Pending === 'none') extra.append(button("解除绑定", async () => {
        if (
          await question(
            "解除绑定？",
            [
              node("p", l.Worktree),
              node("p", "停止同步，保留工作目录中的文件和服务端资料库。"),
            ],
            "解除绑定",
            true,
          )
        )
          await api.Unbind(l.ID);
      }),
    );
    d.dataset.library = l.ID;
    d.append(extra);
  } else {
    bar.append(button("选择绑定目录", () => bind(l), "primary"));
    if (l.Phase === "binding")
      bar.append(button("继续上次绑定", () => api.RetryBinding(l.ID)));
  }
  return d;
}
function libraryList(libs: LibraryView[]) {
  const warn = libs.filter(attention);
  if (warn.length) {
    const w = node("div", "", "warning");
    w.append(
      node("strong", `${warn.length} 个资料库需要处理`),
      button(
        "查看事项",
        async () => {
          page = "attention";
          render();
        },
        "link",
      ),
    );
    content.append(w);
  }
  const grid=node('div','','library-layout'), collection=node('section');grid.append(collection);content.append(grid);
  if (!libs.some(l=>l.ID===selected)) selected=(libs.find(l=>l.Phase==='bound')||libs[0])?.ID||'';
  for (const bound of [true, false]) {
    const group = libs.filter((l) => !!l.Phase === bound);
    collection.append(
      node("h2", `${bound ? "已绑定或绑定中" : "未绑定"} · ${group.length}`),
    );
    if (!group.length) {
      collection.append(
        node(
          "p",
          bound ? "尚无本机绑定。" : "没有已加载的未绑定资料库。",
          "empty",
        ),
      );
      continue;
    }
    const list = node("div", "", "list");
    for (const l of group) {
      const row = node(
          "div",
          "",
          "row" + (l.ID === selected ? " selected" : ""),
        ),
        label = node("div");
      row.dataset.library=l.ID;
      label.append(
        node("strong", libName(l)),
        node("span", status(l), "pill"),
        node("p", l.Worktree || "尚未选择本机工作目录", "sub"),
      );
      row.append(
        label,
        button(bound ? "详情" : "绑定目录", async () => {
          selected = l.ID;
          if (bound) render();
          else await bind(l);
        }),
      );
      list.append(row);
    }
    collection.append(list);
  }
  if (view?.More)
    collection.append(button("加载更多资料库", () => api.Refresh(true)));
  collection.append(
    button("创建资料库", async () => {
      const f = field("资料库名称");
      const trash = field("回收站保留天数", "number");
      const history = field("历史快照保留天数", "number");
      for (const [i, days] of [[trash.i, view?.DefaultTrashDays], [history.i, view?.DefaultHistoryDays]] as const) {
        i.required = false;
        i.min = "1";
        i.max = "36500";
        i.placeholder = days ? `跟随全局默认（${days} 天）` : "留空跟随服务端默认";
      }
      if (await question("创建资料库", [f.l, trash.l, history.l], "创建")) {
        if (!f.i.value.trim() || !trash.i.checkValidity() || !history.i.checkValidity())
          throw new Error("请填写名称；保留天数应为 1–36500，或留空跟随默认。");
        await api.CreateLibrary(f.i.value.trim(), Number(trash.i.value), Number(history.i.value));
      }
    }),
  );
  const chosen = libs.find((l) => l.ID === selected);
  if (chosen) grid.append(detail(chosen));
}
function activities(libs: LibraryView[]) {
  const issues = libs.filter(attention);
  if (issues.length) {
    content.append(node("h2", "需要处理"));
    for (const l of issues) content.append(detail(l));
  } else if (page === "attention")
    content.append(node("p", "暂无需要处理的事项。", "empty"));
  if (page === "attention") return;
  content.append(node("h2", "本机时间"));
  const list = node("div", "", "list");
  for (const a of [...(view?.Activity || [])].reverse()) {
    const l = libs.find((x) => x.ID === a.LibraryID);
    const row = node("div", "", "row");
    row.append(
      node("strong", l ? libName(l) : a.LibraryID.slice(0, 8)),
      node("span", labels[a.Kind] || a.Kind),
      node("time", new Date(a.Time).toLocaleString(), "sub"),
    );
    list.append(row);
  }
  content.append(
    list.children.length ? list : node("p", "还没有本机同步活动。", "empty"),
  );
}
async function history(l: LibraryView, cursor = "") {
  const h = await api.History(l.ID, cursor);
  if (!cursor) {
    content.replaceChildren(node("h2", libName(l) + " · 主线历史"));
    el("title").textContent = "版本历史";
    content.append(
      button("返回资料库", async () => {
        page = "libraries";
        render();
      }),
    );
  }
  content.querySelector("[data-more]")?.remove();
  for (const c of h.Commits || []) {
    const row = node("div", "", "row");
    row.append(
      node("strong", c.Message || "提交"),
      node("code", c.CommitId.slice(0, 12)),
      node("time", new Date(c.CreatedAt).toLocaleString()),
    );
    content.append(row);
  }
  if (!h.Commits?.length) content.append(node("p", "暂无主线提交。"));
  if (h.NextPageToken) {
    const more = button("下一页", () => history(l, h.NextPageToken));
    more.dataset.more = "";
    content.append(more);
  }
}
function render() {
  if (!view) return;
  const libs = view.Libraries || [];
  el("count").textContent = String(libs.filter(attention).length || "");
  document.querySelectorAll<HTMLButtonElement>("[data-page]").forEach((b) => {
    b.setAttribute("aria-current", b.dataset.page === page ? "page" : "false");
  });
  el("notice").textContent = message || view.Notice;
  el("refresh").hidden = !view.SignedIn;
  const title =
    page === "activity"
      ? "最近活动"
      : page === "attention"
        ? "需要处理"
        : page === "settings"
          ? "设置与账号"
          : "我的资料库";
  el("title").textContent = title;
  el("description").textContent = view.SignedIn
    ? view.CatalogFresh
      ? "此设备的资料库与同步状态"
      : "显示本机记录；远端资料库尚未刷新。"
    : "连接服务，把资料库同步到自己的工作目录。";
  content.replaceChildren();
  if (view.LogoutPending) {
    content.append(
      node("p", "本机已停止同步；远端注销尚未完成。"),
      button("重试注销", () => api.Logout()),
      button("仅忘记本机会话", async () => {
        if (
          await question(
            "仅清除本机会话？",
            [node("p", "远端会话可能仍有效，本机将不再重试撤销。")],
            "清除",
          )
        )
          await api.ForgetUnavailableSession();
      }),
    );
    return;
  }
  if (!view.SignedIn) {
    content.append(loginForm());
    return;
  }
  if (page === "settings") {
    content.append(
      node("h2", "当前服务"),
      node("p", view.Server),
      loginForm(),
      button("登录系统后启动：切换", async () => {
        const on = await prefs.AutoStart();
        await prefs.SetAutoStart(!on);
        tell(on ? "已关闭登录启动" : "已开启登录启动");
      }),
      button("启用系统通知", async () => {
        tell(
          (await prefs.EnableNotifications())
            ? "系统通知已启用"
            : "系统未允许通知，仍可查看需要处理。",
        );
      }),
      button("退出账号", async () => {
        if (
          await question(
            "退出当前账号？",
            [node("p", "停止此账号的自动同步并撤销当前会话，保留工作文件。")],
            "退出账号",
          )
        )
          await api.Logout();
      }),
    );
    return;
  }
  if (page === "libraries") libraryList(libs);
  else activities(libs);
}
async function update() {
  try {
    const latest = await api.Snapshot();
    if (!latest || latest.Revision === view?.Revision) return;
    if (page === 'settings' && latest.SignedIn === view?.SignedIn && document.activeElement instanceof HTMLInputElement) return;
    const focused=document.activeElement;
    const restore=focused instanceof HTMLButtonElement && content.contains(focused) ? {label:focused.textContent, library:focused.closest<HTMLElement>('[data-library]')?.dataset.library}:null;
    view = latest;
    if (page !== "history" && !dialog.open) {render();if(restore) Array.from(content.querySelectorAll('button')).find(b=>b.textContent===restore.label && b.closest<HTMLElement>('[data-library]')?.dataset.library===restore.library)?.focus({preventScroll:true})} 
  } catch {
    tell("无法连接桌面主进程。");
  }
}
for (const b of document.querySelectorAll<HTMLButtonElement>("[data-page]"))
  b.onclick = () => {
    page = b.dataset.page!;
    render();
  };
el("settings").onclick = () => {
  page = "settings";
  render();
};
el("refresh").onclick = () => void action(() => api.Refresh(false));
el("quit").onclick = () =>
  void action(async () => {
    if (
      await question(
        "退出 Filecloud？",
        [node("p", "本轮同步结束后退出。仅关闭窗口可继续后台同步。")],
        "退出应用",
      )
    )
      await prefs.Quit();
  });
Events.On("attention", (ev: { data: unknown }) => {
  selected = String(ev.data);
  page = "attention";
  render();
});
void update();
setInterval(() => {
  if ((!busy || page==='libraries' || page==='activity') && !dialog.open && document.visibilityState === "visible")
    void update();
}, 1500);
