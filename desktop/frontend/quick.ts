import { Window, Events } from "/wails/runtime.js";
import {
  api,
  prefs,
  n,
  box,
  btn,
  sub,
  icon,
  status,
  attention,
  tell,
  errorText,
} from "./ui.js";
const host = document.getElementById("quick")!;
async function show(page: string, id = "") {
  const main = Window.Get("workspace");
  await Window.Hide();
  await main.Show();
  await main.UnMinimise();
  await main.Focus();
  await Events.Emit("quick-open", { page, id });
}
let revision = -1;
async function refresh() {
  const v = await api.Snapshot();
  if (!v || v.Revision === revision) return;
  revision = v.Revision;
  const libs = v.Libraries || [],
    bound = libs
      .filter((l) => l.Phase === "bound")
      .sort((a, b) => Number(b.Running) - Number(a.Running)),
    paused = bound.length > 0 && bound.every((l) => l.Paused),
    issues = libs.filter(attention).length;
  const pause = btn(
    paused ? "继续自动检查" : "暂停后续同步",
    async () => {
      await api.PauseAll(!paused);
      await refresh();
    },
    "quick-item",
    "pause",
  );
  pause.disabled = !bound.length;
  host.replaceChildren(
    box(
      "quick-head",
      icon("folder", "blue"),
      box(
        "",
        n("h2", "", "Filecloud"),
        sub(
          !v.SignedIn
            ? "尚未登录"
            : bound.some((l) => l.Running)
              ? "同步进行中"
              : paused
                ? "自动检查已暂停"
                : "同步概览",
        ),
      ),
    ),
    box(
      "quick-libs",
      ...bound.map((l) => {
        const b = btn(
          l.Name,
          () => show("libraries", l.ID),
          "quick-item",
          "folder",
        );
        b.append(status(l));
        return b;
      }),
    ),
    btn(
      `${issues} 项需要处理`,
      () => show("attention"),
      "quick-item blue",
      "issue",
    ),
    box(
      "quick-group",
      btn("打开 Filecloud", () => show("libraries"), "quick-item", "folder"),
      pause,
    ),
    box(
      "quick-group",
      btn("设置…", () => show("settings"), "quick-item", "settings"),
      btn("退出 Filecloud", () => prefs.Quit(), "quick-item", "power"),
    ),
  );
}
function update() {
  void refresh().catch((e) => tell(errorText(e)));
}
window.addEventListener("focus", update);
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") void Window.Hide();
});
setInterval(() => {
  if (document.visibilityState === "visible") update();
}, 1500);
update();
