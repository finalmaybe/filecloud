export * as api from "./bindings/github.com/mingming-cn/filecloud/desktop/workspace.js";
export * as prefs from "./bindings/github.com/mingming-cn/filecloud/desktop/preferences.js";
export type * from "./bindings/github.com/mingming-cn/filecloud/desktop/models.js";
import type { LibraryView } from "./bindings/github.com/mingming-cn/filecloud/desktop/models.js";
import { Clipboard } from "/wails/runtime.js";
const paths = {
  folder:
    "M2.5 7V5a1.5 1.5 0 0 1 1.5-1.5h5l2.5 2.7H20a1.5 1.5 0 0 1 1.5 1.5V19a1.5 1.5 0 0 1-1.5 1.5H4A1.5 1.5 0 0 1 2.5 19V7Zm.2 1.7h18.5",
  sync: "M21 4v6h-6 M3 20v-6h6 M4 9a8.5 8.5 0 0 1 15-3l2 4 M3 14l2 4a8.5 8.5 0 0 0 15-3",
  issue: "M12 2a10 10 0 1 1 0 20 10 10 0 1 1 0-20Zm0 5v6m0 3v.4",
  settings:
    "m9 3 1-2h4l1 2 2 1 2-.2 2 3-1 2 .4 2 1.6 1v3l-2 1-1 2 .2 2-3 1.7-2-1-2 .3-1 1.2H9l-1-2-2-1-2 .2-2-3 1-2-.4-2L1 12V9l2-1 1-2-.2-1.6 3-1.8L9 3Zm3 5a4 4 0 1 0 0 8 4 4 0 1 0 0-8Z",
  check: "M12 2a10 10 0 1 1 0 20 10 10 0 1 1 0-20ZM7.5 12l3 3 6-6",
  minus: "M12 2a10 10 0 1 1 0 20 10 10 0 1 1 0-20ZM7 12h10",
  pause: "M12 2a10 10 0 1 1 0 20 10 10 0 1 1 0-20ZM9 8v8m6-8v8",
  search: "M10 3a7 7 0 1 1 0 14 7 7 0 1 1 0-14Zm5 12 6 6",
  plus: "M12 4v16M4 12h16",
  chevron: "m9 5 7 7-7 7",
  back: "m12 5-7 7 7 7M5 12h15",
  file: "M5 2h9l5 5v15H5V2Zm8 0v6h6M8 12h8m-8 4h6",
  history: "M3 4v6h6M3.5 9a9 9 0 1 1 .5 8M12 6v6l4 3",
  info: "M12 2a10 10 0 1 1 0 20 10 10 0 1 1 0-20Zm0 9v6m0-11v1",
  copy: "M9 6h8l4 4v11H9V6ZM6 17H3V2h10l2 2",
  shield: "m12 2 9 4v6c0 5-4 8-9 11-5-3-9-6-9-11V6l9-4Zm-4 10 3 3 5-6",
  eye: "M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12Zm10-3a3 3 0 1 0 0 6 3 3 0 1 0 0-6Z",
  download: "M12 2v13m-5-5 5 5 5-5M3 16v5h18v-5",
  trash: "M3 6h18M8 6V3h8v3M5 6l1 15h12l1-15M10 10v7m4-7v7",
  lock: "M5 10h14v11H5V10Zm3 0V7a4 4 0 1 1 8 0v3",
  bell: "M3 17h18l-2-3V9a7 7 0 0 0-14 0v5l-2 3Zm6 3a3 3 0 0 0 6 0",
  power: "M12 2v10M7 4a9 9 0 1 0 10 0",
  device: "M4 4h16v13H4V4Zm-3 16h22",
  grid: "M3 3h7v7H3Zm11 0h7v7h-7ZM3 14h7v7H3Zm11 0h7v7h-7Z",
  list: "M3 5h18M3 12h18M3 19h18",
  external: "M14 3h7v7m0-7L11 13M10 4H3v17h17v-7",
  close: "m5 5 14 14M19 5 5 19",
};
export type Icon = keyof typeof paths;
export function icon(key: Icon, cls = "") {
  const ns = "http://www.w3.org/2000/svg",
    s = document.createElementNS(ns, "svg"),
    p = document.createElementNS(ns, "path");
  s.setAttribute("class", `icon ${cls}`);
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("aria-hidden", "true");
  p.setAttribute("d", paths[key]);
  s.append(p);
  return s;
}
export function n<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls = "",
  ...children: (Node | string)[]
) {
  const e = document.createElement(tag);
  e.className = cls;
  e.append(...children);
  return e;
}
export const box = (cls: string, ...children: (Node | string)[]) =>
  n("div", cls, ...children);
export const sub = (txt: string) => n("p", "sub", txt);
export const heading = (name: string, desc = "", ...tools: HTMLElement[]) =>
  n(
    "header",
    "pagehead",
    box("", n("h1", "", name), ...(desc ? [sub(desc)] : [])),
    box("tools", ...tools),
  );
export const line = (title: string, ...right: (Node | string)[]) =>
  box("line", n("span", "", title), box("value", ...right));
export const section = (name: string, ...children: HTMLElement[]) =>
  n("section", "section", n("h3", "", name), ...children);
export const empty = (title: string, desc: string, ic: Icon = "folder") =>
  box("empty", icon(ic), n("strong", "", title), sub(desc));
export const date = (s: string) =>
  s
    ? new Date(s).toLocaleString("zh-CN", {
        month: "numeric",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "暂无记录";
export function bytes(s: string | number) {
  let v = Number(s),
    u = 0;
  const units = ["B", "KB", "MB", "GB", "TB"];
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${Number(v.toFixed(1))} ${units[u]}`;
}
const labels: Record<string, string> = {
  no_change: "本轮同步无变化",
  completed: "同步完成",
  scan_completed: "已完成本地扫描",
  conflict_saved: "已保留冲突副本",
  import_required: "此目录已有文件。请核对首次导入，或选择空文件夹。",
  no_common_base: "本地与远端没有共同基线。请选择新的空文件夹。",
  delete_confirmation: "等待删除确认",
  stale_candidate: "确认已失效，请重新核对。",
  sync_first: "本机、同步基线与远端尚未一致。请先同步，再预览恢复。",
  auth_required: "登录已过期，本地文件已保留。请重新登录并刷新绑定凭据。",
  busy: "已有任务正在进行",
  offline: "离线待重试",
  retry_later: "服务暂不可用，等待重试",
  dav_locked: "文件正在网络盘中编辑，本地修改已保留。",
  interrupted: "同步结果未知，请检查后重试",
  unknown: "操作未完成，请检查连接、账号或目录后重试。",
  missing: "目录或文件不可用",
  unsupported_fs:
    "不支持此文件系统。请选择本机 ext4、APFS 或固定 NTFS 文件夹。",
  status_unavailable: "本机状态不可读，请检查工作目录。",
  sync: "等待继续同步",
  checkout: "等待继续落盘",
  restore: "等待继续恢复",
  "delete-confirmation": "等待删除确认",
  "restore-confirmation": "历史恢复待确认",
};
export const describe = (s: string) => labels[s] || s;
export function attention(l: LibraryView) {
  return Boolean(
    l.Problem ||
      l.Phase === "binding" ||
      l.Pending.endsWith("-confirmation") ||
      l.Conflicts?.length,
  );
}
export function state(l: LibraryView): [Icon, string, string] {
  if (l.Running) return ["sync", "正在同步…", "blue spinning"];
  if (l.Pending.endsWith("-confirmation") || l.Problem)
    return [
      "issue",
      describe(l.Pending.endsWith("-confirmation") ? l.Pending : l.Problem),
      "orange",
    ];
  if (l.Phase === "binding") return ["issue", "绑定未完成", "orange"];
  if (l.Phase !== "bound") return ["minus", "未连接", ""];
  if (l.Paused) return ["pause", "自动检查已暂停", ""];
  if (l.Pending && l.Pending !== "none")
    return ["sync", describe(l.Pending), "blue"];
  if (l.LastSuccess) return ["check", "上次同步成功", "green"];
  return ["folder", "已连接", "blue"];
}
export function status(l: LibraryView) {
  const [ic, text, cls] = state(l);
  return box("status", icon(ic, cls), n("span", "", text));
}
export function tell(msg: string) {
  const e = document.getElementById("notice")!;
  e.hidden = !msg;
  e.replaceChildren(n("span", "", msg));
  if (msg) e.append(btn("关闭提示", () => tell(""), "bare", "close", true));
}
export function errorText(e: unknown) {
  return describe(e instanceof Error ? e.message : String(e));
}
export type Action = () => unknown;
export function btn(
  label: string,
  fn: Action,
  cls = "",
  ic?: Icon,
  only = false,
) {
  const b = n("button", cls);
  b.type = "button";
  b.title = label;
  b.setAttribute("aria-label", label);
  if (ic) b.append(icon(ic));
  if (!only) b.append(label);
  b.onclick = async () => {
    if (b.getAttribute("aria-busy") === "true") return;
    b.setAttribute("aria-busy", "true");
    try {
      await fn();
    } catch (e) {
      const modal = b.closest("dialog");
      if (modal) {
        let msg = modal.querySelector<HTMLElement>("[role=alert]");
        if (!msg) {
          msg = n("p", "feedback");
          msg.setAttribute("role", "alert");
          modal.append(msg);
        }
        msg.textContent = errorText(e);
      } else tell(errorText(e));
    } finally {
      b.removeAttribute("aria-busy");
    }
  };
  return b;
}
export function tabs(
  names: string[],
  active: string,
  choose: (name: string) => unknown,
  cls = "tabs",
) {
  return box(
    cls,
    ...names.map((t) => {
      const b = btn(t, () => choose(t), active === t ? "active" : "");
      b.setAttribute("aria-pressed", String(t === active));
      return b;
    }),
  );
}
export function field(label: string, type = "text", value = "") {
  const i = n("input");
  i.type = type;
  i.value = value;
  return { input: i, label: n("label", "field", n("span", "", label), i) };
}
export function toggle(label: string, checked: boolean) {
  const i = n("input");
  i.type = "checkbox";
  i.checked = checked;
  return { input: i, label: n("label", "checkline", i, label) };
}
export function search(
  label: string,
  value: string,
  change: (s: string) => void,
) {
  const i = n("input");
  i.type = "search";
  i.placeholder = label;
  i.setAttribute("aria-label", label);
  i.value = value;
  i.oninput = () => change(i.value);
  return n("label", "search", icon("search"), i);
}
export const copy = (s: string) =>
  btn("复制", () => Clipboard.SetText(s), "bare", "copy", true);
export function dialog(
  title: string,
  body: HTMLElement[],
  accept: string,
  opts: {
    danger?: boolean;
    valid?: () => boolean;
    wide?: boolean;
    frame?: (actions: HTMLButtonElement[]) => HTMLElement;
    cancel?: string;
  } = {},
) {
  return new Promise<boolean>((resolve) => {
    const d = n("dialog", opts.wide ? "wide" : "");
    d.setAttribute("aria-label", title);
    const no = btn(opts.cancel || "取消", () => d.close("no")),
      yes = btn(
        accept,
        () => d.close("yes"),
        opts.danger ? "danger" : "primary",
      );
    const check = () => {
      yes.disabled = opts.valid ? !opts.valid() : false;
    };
    d.oninput = check;
    check();
    const actions = accept ? [no, yes] : [no];
    if (opts.frame) {
      d.className = "wizard-dialog";
      d.append(opts.frame(actions));
    } else
      d.append(
        n("h2", "", title),
        box("dialog-body", ...body),
        box("actions end", ...actions),
      );
    d.onclose = () => {
      d.remove();
      resolve(d.returnValue === "yes");
    };
    document.body.append(d);
    d.showModal();
    no.focus();
  });
}
export function filterRows(container: HTMLElement, term: string) {
  const q = term.trim().toLowerCase();
  let found = false;
  container.querySelectorAll<HTMLElement>("[data-match]").forEach((r) => {
    r.hidden = !r.dataset.match!.toLowerCase().includes(q);
    if (!r.hidden) found = true;
  });
  const msg = container.querySelector<HTMLElement>(".no-match");
  if (msg) msg.hidden = found;
}
export const noMatch = () => n("p", "no-match", "没有匹配的项目。");
export function pickRow(row: HTMLElement, fn: () => void, label: string) {
  row.tabIndex = 0;
  row.setAttribute("aria-label", label);
  row.onclick = (e) => {
    if (!(e.target as Element).closest("button,input,details")) fn();
  };
  row.onkeydown = (e) => {
    if (e.target === row && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      fn();
    }
  };
}
