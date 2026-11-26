import {
  api,
  box,
  btn,
  n,
  field,
  sub,
  section,
  icon,
  line,
  toggle,
  tell,
  errorText,
  errorCode,
  canOpenDocument,
  revealLabel,
  copy,
  dialog,
  type LibraryView,
  type View,
  menu,
  onDisk,
  bytes,
  date,
  type BindingCheck,
  type Conflict,
} from "./ui.js";
import { previewBody, showPreview } from "./preview.js";
import { Clipboard } from "/wails/runtime.js";
export function wizard(
  title: string,
  desc: string,
  steps: string[],
  step: number,
  body: HTMLElement[],
  actions: HTMLElement[],
  warning = false,
) {
  return box(
    "wizard",
    box("window-title", "Filecloud"),
    box(
      "wizard-grid",
      box(
        "wizard-intro",
        icon("folder", warning ? "orange" : "blue"),
        n("h2", "", title),
        sub(desc),
      ),
      box(
        "wizard-body",
        box(
          "steps",
          ...steps.map((s, i) =>
            box(i === step ? "current" : "", n("b", "", String(i + 1)), s),
          ),
        ),
        box("wizard-fields", ...body),
        box("actions end", ...actions),
      ),
    ),
  );
}
export function login(
  view: View,
  done: () => Promise<void>,
  back?: () => void,
) {
  const host = field("服务器地址", "url", view.Server),
    user = field("用户名", "text", view.Username),
    pass = field("密码", "password"),
    device = field("设备名称", "text", view.DeviceName || "桌面客户端");
  host.input.placeholder = "https://cloud.example.com";
  user.input.autocomplete = "username";
  pass.input.autocomplete = "current-password";
  for (const f of [host, user, pass, device]) f.input.required = true;
  const eye = btn(
    "显示或隐藏密码",
    () => {
      pass.input.type = pass.input.type === "password" ? "text" : "password";
    },
    "bare",
    "eye",
    true,
  );
  pass.label.append(eye);
  const msg = sub("账号由管理员创建。");
  const submit = n("button", "primary", "登录");
  submit.type = "submit";
  const form = n("form", "wizard-host");
  const connection = box("connection-status");
  let verified = "";
  const serverStep = () => {
    verified = "";
    connection.replaceChildren();
    pass.input.value = "";
    submit.textContent = "继续";
    form.replaceChildren(
      wizard(
        "连接你的 Filecloud",
        "输入服务器地址，检查服务连接后登录账号。",
        ["服务器", "账号"],
        0,
        [host.label, connection],
        [...(back ? [btn("返回", back)] : []), submit],
      ),
    );
    host.input.focus();
  };
  const accountStep = () => {
    connection.replaceChildren(icon("check", "green"), "服务连接正常");
    submit.textContent = "登录";
    host.input.readOnly = true;
    form.replaceChildren(
      wizard(
        "连接你的 Filecloud",
        "登录你的 Filecloud 服务器，以连接资料库并在本机同步文件。",
        ["服务器", "账号"],
        1,
        [host.label, connection, user.label, pass.label, device.label, msg],
        [
          btn("返回", () => {
            host.input.readOnly = false;
            serverStep();
          }),
          submit,
        ],
      ),
    );
    user.input.focus();
  };
  serverStep();
  form.onsubmit = async (e) => {
    e.preventDefault();
    if (submit.disabled) return;
    submit.disabled = true;
    if (!verified) {
      const server = host.input.value.trim();
      host.input.readOnly = true;
      connection.textContent = "正在检查服务连接…";
      try {
        await api.CheckServer(server);
        verified = server;
        accountStep();
      } catch (e) {
        host.input.readOnly = false;
        connection.textContent = "无法连接 Filecloud，请检查服务器地址后重试。";
        host.input.focus();
      } finally {
        submit.disabled = false;
      }
      return;
    }
    const secret = pass.input.value;
    pass.input.value = "";
    msg.textContent = "正在连接服务器…";
    try {
      await api.Login(verified, user.input.value, secret, device.input.value);
      await done();
    } catch (e) {
      msg.className = "feedback";
      msg.textContent = errorText(e);
    } finally {
      submit.disabled = false;
    }
  };
  return form;
}
export async function createLibrary(view: View) {
  const name = field("资料库名称"),
    trash = field("回收站保留天数", "number"),
    history = field("历史版本保留天数", "number");
  for (const [f, d] of [
    [trash, view.DefaultTrashDays],
    [history, view.DefaultHistoryDays],
  ] as const) {
    f.input.min = "1";
    f.input.max = "36500";
    f.input.placeholder = d ? `跟随服务器默认 · ${d} 天` : "跟随服务器默认";
  }
  if (
    await dialog(
      "新建资料库",
      [name.label, trash.label, history.label],
      "创建",
      {
        valid: () =>
          Boolean(
            name.input.value.trim() &&
              trash.input.checkValidity() &&
              history.input.checkValidity(),
          ),
      },
    )
  ) {
    await api.CreateLibrary(
      name.input.value.trim(),
      Number(trash.input.value),
      Number(history.input.value),
    );
    return true;
  }
  return false;
}
export async function bindLibrary(l: LibraryView) {
  const state: { check?: BindingCheck } = {},
    location = n("span", "", "尚未选择"),
    checks = box("bind-checks"),
    consent = toggle("导入现有本地文件", false);
  const steps = ["选择资料库", "本机文件夹", "确认"];
  const valid = () =>
    !!state.check &&
    (state.check.Empty || (!state.check.Published && consent.input.checked));
  const pick = btn(
    "选择本地文件夹",
    async () => {
      state.check = undefined;
      consent.input.checked = false;
      checks.replaceChildren();
      location.textContent = "尚未选择";
      refresh();
      const path = await api.PickDirectory(l.ID);
      if (!path) return;
      location.textContent = path;
      checks.replaceChildren(sub("正在检查目录与资料库…"));
      try {
        const c = await api.CheckDirectory(l.ID);
        state.check = c;
        const blocked = !c.Empty && c.Published;
        checks.replaceChildren(
          box(
            "metadata",
            icon("check", "green"),
            "本机 " + c.Filesystem + " 文件系统",
          ),
          box(
            "metadata",
            icon("check", "green"),
            c.Empty ? "文件夹为空" : `${c.LocalItems} 个本地项目`,
          ),
          box("metadata", icon("check", "green"), "具备读写权限"),
        );
        if (blocked) {
          checks.append(
            n("strong", "orange", "请选择一个空文件夹"),
            sub(
              "本地已有文件，资料库已发布过版本，尚未建立同步基线。请使用新的空文件夹下载资料库，再整理本地内容。",
            ),
          );
          pick.textContent = "选择其他文件夹";
        } else {
          checks.append(
            section(
              "连接结果",
              sub(
                c.Published
                  ? "资料库文件 → 此文件夹。连接后将下载资料库中的文件。"
                  : c.Empty
                    ? "双方均为空，将创建初始空版本。"
                    : "此文件夹 → 全新资料库。首次同步将导入本地内容。",
              ),
            ),
          );
          if (!c.Empty) checks.append(consent.label);
          pick.textContent = "重新选择…";
        }
        refresh();
      } catch (e) {
        checks.replaceChildren(n("p", "feedback", errorText(e)));
        pick.textContent = "选择其他文件夹";
        refresh();
      }
    },
    "",
  );
  const refresh = () => {
    const d = checks.closest("dialog");
    if (!d) return;
    d.querySelectorAll(".steps>div").forEach((s, i) =>
      s.classList.toggle(
        "current",
        i ===
          (state.check && (state.check.Empty || !state.check.Published)
            ? 2
            : 1),
      ),
    );
    const blocked = state.check?.Published && !state.check.Empty;
    d.querySelector(".wizard-intro h2")!.textContent = blocked
      ? "请选择一个空文件夹"
      : "连接本机文件夹";
    d.querySelector(".wizard-intro .icon")!.classList.toggle(
      "orange",
      !!blocked,
    );
    pick.setAttribute("aria-label", pick.textContent || "选择本地文件夹");
    checks.dispatchEvent(new Event("input", { bubbles: true }));
  };
  const body = [
    line("选定的资料库", icon("folder", "blue"), l.Name),
    line("本地文件夹", location, pick),
    checks,
  ];
  const ok = await dialog("连接本机文件夹", body, "连接并同步", {
    cancel: "上一步",
    valid,
    frame: (actions) =>
      wizard(
        "连接本机文件夹",
        "将选定的资料库连接到本机的一个文件夹，用于存放并同步文件。",
        steps,
        1,
        body,
        actions,
      ),
  });
  if (!ok || !state.check) return false;
  await api.Bind(
    l.ID,
    state.check.Path,
    !state.check.Empty && consent.input.checked,
  );
  return true;
}
export async function unbind(l: LibraryView) {
  if (
    await dialog(
      "停止在此设备上同步？",
      [
        box(
          "inspector-head",
          icon("folder", "blue"),
          box("", n("strong", "", l.Name), sub(l.Worktree)),
        ),
        n("p", "", "本地文件和服务器中的资料库都会保留。"),
        sub("有未完成操作时，需要先完成当前任务。"),
      ],
      "解除绑定",
    )
  ) {
    await api.Unbind(l.ID);
    return true;
  }
  return false;
}
export async function reviewDelete(l: LibraryView) {
  for (;;) {
    const v = await api.ReviewDeletion(l.ID);
    if (!v) return;
    const code = field(`输入上方 ${v.Candidate.length} 位确认码`),
      checked = toggle("我已核对本次删除范围", false);
    const ok = await dialog(
      "检测到大量删除",
      [
        box(
          "inspector-head",
          icon("folder", "blue"),
          box("", n("strong", "", l.Name), sub(v.Worktree)),
        ),
        box(
          "group",
          line(
            `${v.Deleted} / ${v.Tracked} 个已跟踪路径`,
            v.Tracked
              ? `约 ${((v.Deleted * 100) / v.Tracked).toFixed(1)}%`
              : "—",
          ),
        ),
        sub("确认后，将向资料库发布这些删除，其他设备在下次同步时接收。"),
        section(
          "请先核对本地文件夹",
          btn("打开本地文件夹", () => api.OpenWorktree(l.ID), "", "folder"),
        ),
        section(
          "确认本次候选",
          box("path", n("code", "", v.Candidate), copy(v.Candidate)),
          code.label,
          checked.label,
        ),
      ],
      "确认本次删除",
      {
        danger: true,
        sheet: true,
        symbol: "shield",
        valid: () => code.input.value === v.Candidate && checked.input.checked,
      },
    );
    if (!ok) {
      await api.CancelReview();
      return;
    }
    try {
      await api.ConfirmDeletion(v);
      return;
    } catch (e) {
      if (errorCode(e) !== "stale_candidate") throw e;
      if (
        !(await dialog(
          "确认已失效",
          [
            line("资料库", l.Name),
            sub(
              "文件或删除候选已变化。重新同步检查后，再核对新的范围和确认码。",
            ),
          ],
          "重新核对",
        ))
      )
        return;
      const latest = await api.Snapshot();
      if (
        latest?.Libraries?.find((x) => x.ID === l.ID)?.Pending !==
        "delete-confirmation"
      ) {
        if (
          !(await dialog(
            "重新同步并检查？",
            [
              line("资料库", l.Name),
              sub(
                "旧候选已丢弃。开始新一轮正常同步会发布无需保护的变更；若再次触发删除保护，将先显示新候选。",
              ),
            ],
            "开始新一轮同步",
          ))
        )
          return;
        try {
          await api.RequestSync(l.ID);
          tell("本轮同步已完成，没有需要确认的删除候选。");
          return;
        } catch (e) {
          if (errorCode(e) !== "delete_confirmation") throw e;
        }
      }
    }
  }
}
export async function reviewRestore(l: LibraryView, commit = "", path = "") {
  for (;;) {
    try {
      const v = await api.PreviewRestore(l.ID, commit, path);
      if (!v) {
        tell(
          commit
            ? "当前内容已与所选版本一致，无需创建新版本。"
            : "当前没有待核对的恢复预览。",
        );
        return false;
      }
      commit = v.SourceCommit;
      path = v.SourcePath;
      const code = field("输入确认码");
      const ok = await dialog(
        "预览恢复结果",
        [
          line("恢复位置", l.Name + " / " + v.SourcePath),
          line("历史版本", v.SourceCommit.slice(0, 12)),
          line("本机文件夹", v.Worktree),
          box(
            "metadata",
            icon("check", "green"),
            "预览以已同步的基线为准，确认时将重新核对。",
          ),
          sub(
            "确认后将历史内容恢复到原位置，发布一个新版本。历史目录中没有的当前项目会保留。",
          ),
          box(
            "group",
            line("新建", String(v.Created)),
            line("更新", String(v.Updated)),
            line("类型替换", String(v.Replaced)),
            line("将移除的子项目", String(v.Removed)),
            line("保留当前独有项", String(v.Preserved)),
          ),
          box("path", v.Candidate, copy(v.Candidate)),
          code.label,
        ],
        "确认并恢复",
        {
          valid: () => code.input.value === v.Candidate,
          sheet: true,
          symbol: "history",
        },
      );
      await api.FinishRestore(v, ok);
      if (ok) tell("恢复完成，已发布新的资料库版本。");
      return ok;
    } catch (e) {
      const code = errorCode(e);
      if (code !== "sync_first" && code !== "stale_candidate") throw e;
      const stale = code === "stale_candidate";
      if (
        !(await dialog(
          stale ? "预览已失效" : "需要先同步",
          [
            line("资料库", l.Name),
            line("恢复位置", path || "原位置"),
            sub(
              "先同步本地、基线与远端，然后重新预览。新的候选需要再次输入确认码。",
            ),
          ],
          "先同步并重新预览",
        ))
      )
        return false;
      await api.RequestSync(l.ID);
    }
  }
}
export function compare(l: LibraryView, initial: Conflict, back: () => void) {
  const list = box("conflict-list"),
    panels = box("compare"),
    actions = box("actions"),
    pane = n(
      "section",
      "pane",
      box(
        "inspector-head",
        icon("shield", "blue"),
        box(
          "",
          n("h1", "", "已保留两个版本"),
          sub("原文件与本地冲突副本都已保存，同步可继续。"),
        ),
      ),
      list,
      panels,
      box(
        "metadata compare-note",
        icon("info"),
        "你可以在本地文件夹中整理两个文件，保存后的更改会在下一轮同步。",
      ),
      actions,
    );
  function select(pair: Conflict) {
    list.replaceChildren(
      ...(l.Conflicts || []).map((p) => {
        const b = btn(
          p.Source,
          () => select(p),
          "conflict-choice" + (p.Target === pair.Target ? " selected" : ""),
        );
        const parts = p.Source.split("/"),
          name = parts.pop()!;
        b.setAttribute("aria-pressed", String(p.Target === pair.Target));
        b.replaceChildren(
          n("span", "selection-mark"),
          icon("file", "blue"),
          box(
            "grow",
            n("strong", "", name),
            sub([l.Name, ...parts].join(" / ")),
          ),
          box("status", icon("info", "blue"), "待整理"),
        );
        return b;
      }),
    );
    const bodies = [false, true].map((side) => {
      const name = side ? pair.Target : pair.Source;
      const body = box("preview", sub("正在读取…"));
      return {
        body,
        name,
        panel: section(
          side ? "你的冲突副本" : "资料库中的文件",
          sub(side ? "保留的本地修改 · 本机内容" : "保留原文件名 · 本机内容"),
          box(
            "file-panel-head",
            n("strong", "filename", name),
            menu("文件操作", [
              btn(
                "快速预览",
                () =>
                  showPreview([
                    { name, load: () => api.ConflictPreview(l.ID, pair, side) },
                  ]),
                "menu-link",
                "eye",
              ),
              btn(
                revealLabel(),
                () => api.RevealLocal(l.ID, name),
                "menu-link",
                "folder",
              ),
              btn(
                "复制文件路径",
                () => Clipboard.SetText(onDisk(l, name)),
                "menu-link",
                "copy",
              ),
            ]),
          ),
          body,
        ),
      };
    });
    panels.replaceChildren(...bodies.map((b) => b.panel));
    actions.replaceChildren(
      ...[false, true].map((side) =>
        btn(
          (canOpenDocument(side ? pair.Target : pair.Source)
            ? "打开"
            : "预览") + (side ? "冲突副本" : "原文件"),
          async () => {
            const name = side ? pair.Target : pair.Source;
            if (canOpenDocument(name)) {
              await api.OpenLocalDocument(l.ID, name);
              return;
            }
            await showPreview([
              { name, load: () => api.ConflictPreview(l.ID, pair, side) },
            ]);
          },
          "",
          "file",
        ),
      ),
      btn(
        revealLabel(),
        () => api.RevealLocal(l.ID, pair.Target),
        "primary",
        "folder",
      ),
      btn("标记已整理", async () => {
        await api.DismissConflict(l.ID, pair);
        back();
      }),
      btn("稍后处理", back),
    );
    void Promise.all([
      api.ConflictPreview(l.ID, pair, false),
      api.ConflictPreview(l.ID, pair, true),
    ])
      .then((values) => {
        const text = values.every((v) => v.Media === "text/plain");
        const lines = text ? values.map((v) => v.Content.split("\n")) : [];
        let start = 0,
          end = 0;
        if (text) {
          while (
            start < Math.min(lines[0].length, lines[1].length) &&
            lines[0][start] === lines[1][start]
          )
            start++;
          while (
            end < Math.min(lines[0].length, lines[1].length) - start &&
            lines[0].at(-1 - end) === lines[1].at(-1 - end)
          )
            end++;
        }
        bodies.forEach(({ body, name }, i) => {
          if (values[i].ModifiedAt)
            body.before(
              sub(date(values[i].ModifiedAt) + " · " + bytes(values[i].Size)),
            );
          if (!text) {
            body.replaceChildren(previewBody(values[i], name, true));
            return;
          }
          const from = Math.max(0, Math.min(start - 4, lines[i].length - 80));
          const preview = n("ol", "diff");
          preview.start = from + 1;
          lines[i]
            .slice(from, from + 80)
            .forEach((s, j) =>
              preview.append(
                n(
                  "li",
                  j + from >= start &&
                    j + from < lines[i].length - end &&
                    s !== lines[1 - i][j + from]
                    ? "changed"
                    : "",
                  s || " ",
                ),
              ),
            );
          body.replaceChildren(
            preview,
            sub("文本节选 · 可在本地文件夹中查看全文。"),
          );
        });
      })
      .catch((e) =>
        bodies.forEach((b) => b.body.replaceChildren(sub(errorText(e)))),
      );
  }
  select(initial);
  return pane;
}
