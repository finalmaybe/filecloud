import {
  api,
  n,
  box,
  btn,
  icon,
  tabs,
  sub,
  section,
  line,
  bytes,
  field,
  dialog,
  tell,
  load,
  type LibraryView,
  type View,
} from "./ui.js";
import { bindLibrary, unbind } from "./flows.js";
import { files } from "./files.js";
import { history } from "./history.js";
import { trash } from "./trash.js";
export const detailTabs = ["文件", "版本历史", "回收站", "设置"];
export function libraryDetail(
  l: LibraryView,
  initial: string,
  view: View,
  remember: (tab: string) => void,
  back: () => void,
  changed: () => Promise<void>,
) {
  const root = box("detail-host");
  let filePage: HTMLElement | undefined;
  const select = (tab: string) => {
    remember(tab);
    if (tab === "文件" && filePage) {
      root.replaceChildren(filePage);
      return;
    }
    const main = n("section", "pane"),
      inside = box("detail-body"),
      side = n("aside", "inspector"),
      page = box("workspace", main, side),
      bar = box("toolbar");
    side.hidden = true;
    const title = n(
      "h1",
      "identity",
      ...(tab === "文件" ? [] : [icon("folder", "blue")]),
      l.Name,
    );
    main.append(
      box(
        "detail-heading",
        title,
        ...(tab === "文件" ? [sub("资料库当前版本")] : []),
      ),
    );
    if (tab === "文件") main.append(bar);
    else if (tab === "版本历史")
      main.append(
        box(
          "crumb history-crumb",
          btn("返回资料库", back, "bare", "home", true),
          icon("chevron"),
          tab,
        ),
      );
    main.append(tabs(detailTabs, tab, select), inside);
    root.replaceChildren(page);
    if (tab === "文件") {
      filePage = page;
      files(l, inside, side, bar, back);
    } else if (tab === "版本历史")
      history(l, inside, view.DeviceName, () => {
        filePage = undefined;
      });
    else if (tab === "回收站")
      trash(l, inside, side, () => {
        filePage = undefined;
      });
    else retention(l, inside, view, changed);
  };
  select(initial);
  return root;
}

function period(label: string, days: number, defaults: number) {
  const select = n("select"),
    custom = field("自定义天数", "number"),
    wrap = box("period");
  custom.input.min = "1";
  custom.input.max = "36500";
  custom.input.required = true;
  for (const d of [
    0,
    ...new Set(
      [7, 30, 60, 90, 180, 365, ...(days > 0 ? [days] : [])].sort(
        (a, b) => a - b,
      ),
    ),
    -2,
  ]) {
    const o = n(
      "option",
      "",
      d === 0 ? "跟随服务器默认" : d === -2 ? "自定义…" : `${d} 天`,
    );
    o.value = String(d);
    select.append(o);
  }
  select.value = String(Math.max(0, days));
  select.setAttribute("aria-label", label);
  custom.label.hidden = true;
  select.onchange = () => {
    custom.label.hidden = select.value !== "-2";
    if (!custom.label.hidden) custom.input.focus();
  };
  wrap.append(
    line(label, select),
    custom.label,
    sub(defaults < 0 ? "服务器默认：永久保留" : `服务器默认：${defaults} 天`),
  );
  return {
    wrap,
    select,
    value: () =>
      Number(select.value === "-2" ? custom.input.value : select.value),
    valid: () => select.value !== "-2" || custom.input.reportValidity(),
  };
}
function retention(
  l: LibraryView,
  host: HTMLElement,
  v: View,
  changed: () => Promise<void>,
) {
  const policy = section("资料库保留设置");
  const bound = l.Phase === "bound",
    idle = bound && !l.Running && l.Pending === "none";
  const binding = section(
    "本机绑定",
    line("本机位置", l.Worktree || "尚未连接"),
  );
  const actions = box("actions");
  if (bound) {
    actions.append(
      btn("打开本地文件夹", () => api.OpenWorktree(l.ID), "", "folder"),
      btn("刷新绑定凭据", async () => {
        await api.RetryBinding(l.ID);
        tell("绑定凭据已刷新。");
      }),
    );
    const remove = btn("解除绑定", async () => {
      if (await unbind(l)) await changed();
    });
    remove.disabled = !idle;
    actions.append(remove);
    binding.append(
      actions,
      sub(
        idle
          ? "解绑保留本地文件和远端资料库。"
          : "有未完成操作，请先完成当前任务，再解除绑定。",
      ),
    );
  } else
    binding.append(
      btn(
        l.Phase === "binding" ? "继续上次绑定" : "连接本地文件夹",
        async () => {
          if (l.Phase === "binding") {
            await api.RetryBinding(l.ID);
            await changed();
          } else if (await bindLibrary(l)) await changed();
        },
        "",
        "folder",
      ),
    );
  host.append(
    binding,
    policy,
    section(
      "高级",
      line(
        "WebDAV 应用密码",
        btn("在网页中管理", () => api.AppPasswords(l.ID), "", "external"),
      ),
      sub("需要服务器启用网页控制台，并在浏览器中独立登录。"),
    ),
  );
  const form = box("");
  policy.append(form);
  const show = async () => {
    const p = await api.LibraryInfo(l.ID),
      trash = period("回收站保留时间", p.TrashDays, v.DefaultTrashDays),
      hist = period("历史版本保留时间", p.HistoryDays, v.DefaultHistoryDays);
    const permanent = btn(
      "永久保留历史",
      () => {
        const on = permanent.getAttribute("aria-checked") !== "true";
        permanent.setAttribute("aria-checked", String(on));
        hist.select.disabled = on;
        hist.wrap.querySelectorAll("input").forEach((i) => (i.disabled = on));
      },
      "switch",
    );
    permanent.textContent = "";
    permanent.setAttribute("role", "switch");
    permanent.setAttribute("aria-checked", String(p.HistoryDays === -1));
    hist.select.disabled = p.HistoryDays === -1;
    form.replaceChildren(
      trash.wrap,
      hist.wrap,
      line("永久保留历史", permanent),
      sub("当前版本始终保留；回收站新设置只影响之后删除的项目。"),
      box(
        "actions end",
        btn(
          "保存更改",
          async () => {
            if (!trash.valid() || !hist.valid()) return;
            const next = {
                TrashDays: trash.value(),
                HistoryDays:
                  permanent.getAttribute("aria-checked") === "true"
                    ? -1
                    : hist.value(),
              },
              impact = await api.Retention(l.ID, next, false, "");
            const days = (d: number) =>
              (d || v.DefaultHistoryDays) < 0
                ? "永久"
                : `${d || v.DefaultHistoryDays} 天`;
            if (
              impact.Token &&
              !(await dialog(
                "缩短历史保留时间",
                [
                  line("资料库", l.Name),
                  line(
                    "历史保留期",
                    days(p.HistoryDays) + " → " + days(next.HistoryDays),
                  ),
                  line("预计过期快照", String(impact.Count)),
                  line("相关独占内容", bytes(impact.Bytes)),
                  line("受影响的已恢复记录", String(impact.RestoredRecords)),
                  sub("到期历史内容将无法恢复，清理会在后台运行。"),
                ],
                "确认更改",
              ))
            )
              return;
            await api.Retention(l.ID, next, true, impact.Token);
            tell("保留设置已保存。");
            load(form, show);
          },
          "primary",
        ),
      ),
    );
  };
  load(form, show);
}
