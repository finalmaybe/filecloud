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
  copy,
  dialog,
  type LibraryView,
  type View,
  type Preview,
  type Conflict,
} from "./ui.js";
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
        ...body,
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
  form.append(
    wizard(
      "连接你的 Filecloud",
      "登录你的 Filecloud 服务器，以连接资料库并在本机同步文件。",
      ["服务器", "账号"],
      0,
      [host.label, user.label, pass.label, device.label, msg],
      [...(back ? [btn("返回", back)] : []), submit],
    ),
  );
  form.addEventListener("focusin", (e) => {
    const second = e.target !== host.input;
    form
      .querySelectorAll(".steps > div")
      .forEach((x, i) => x.classList.toggle("current", i === Number(second)));
  });
  form.onsubmit = async (e) => {
    e.preventDefault();
    if (submit.disabled) return;
    submit.disabled = true;
    const secret = pass.input.value;
    pass.input.value = "";
    msg.textContent = "正在连接服务器…";
    try {
      await api.Login(
        host.input.value,
        user.input.value,
        secret,
        device.input.value,
      );
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
  let path = await api.PickDirectory(l.ID);
  while (path) {
    const c = await api.CheckDirectory(l.ID);
    const blocked = !c.Empty && c.Published,
      needsImport = !c.Empty && !c.Published,
      consent = toggle("导入现有本地文件", false);
    const body: HTMLElement[] = [
      line("选定的资料库", icon("folder", "blue"), l.Name),
      line("本地文件夹", path),
      line("文件系统", icon("check", "green"), c.Filesystem),
      line("目录检查", c.Empty ? "文件夹为空" : "文件夹中已有内容"),
    ];
    if (blocked)
      body.push(
        sub(
          "本地和资料库中都已有文件，尚未建立同步基线。请使用新的空文件夹下载资料库，再整理需要保留的本地内容。",
        ),
      );
    else {
      body.push(
        section(
          "连接结果",
          sub(
            c.Published
              ? "资料库文件 → 此文件夹。连接后将下载资料库中的文件。"
              : needsImport
                ? "此文件夹 → 从未发布过版本的资料库。首次同步将导入本地内容。"
                : "双方均为空，将建立绑定并创建初始空版本。",
          ),
        ),
      );
      if (needsImport) body.push(consent.label);
    }
    const ok = await dialog(
      blocked ? "请选择一个空文件夹" : "连接本机文件夹",
      body,
      blocked ? "选择其他文件夹" : "连接并同步",
      {
        valid: () => !needsImport || consent.input.checked,
        frame: (actions) =>
          wizard(
            blocked ? "请选择一个空文件夹" : "连接本机文件夹",
            blocked
              ? "使用新的空文件夹下载资料库，再整理本地内容。"
              : "将选定的资料库连接到本机的一个文件夹，用于存放并同步文件。",
            ["选择资料库", "本机文件夹", "确认"],
            blocked ? 1 : 2,
            body,
            actions,
            blocked,
          ),
      },
    );
    if (!ok) return false;
    if (blocked) {
      path = await api.PickDirectory(l.ID);
      continue;
    }
    await api.Bind(l.ID, path, needsImport && consent.input.checked);
    return true;
  }
  return false;
}
export async function unbind(l: LibraryView) {
  if (
    await dialog(
      "停止在此设备上同步？",
      [
        section(l.Name, sub(l.Worktree)),
        n("p", "", "本地文件和服务器中的资料库都会保留。"),
        sub("有未完成操作时，需要先完成当前任务。"),
      ],
      "解除绑定",
    )
  )
    await api.Unbind(l.ID);
}
export async function reviewDelete(l: LibraryView) {
  const v = await api.ReviewDeletion(l.ID);
  if (!v) return;
  const code = field(`输入上方 ${v.Candidate.length} 位确认码`),
    checked = toggle("我已核对本次删除范围", false);
  const ok = await dialog(
    "检测到大量删除",
    [
      section(l.Name, sub(v.Worktree)),
      line(
        `${v.Deleted} / ${v.Tracked} 个已跟踪路径`,
        v.Tracked ? `约 ${((v.Deleted * 100) / v.Tracked).toFixed(1)}%` : "—",
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
      valid: () => code.input.value === v.Candidate && checked.input.checked,
    },
  );
  if (ok) await api.ConfirmDeletion(v);
  else await api.CancelReview();
}
export async function reviewRestore(l: LibraryView, commit = "", path = "") {
  const v = await api.PreviewRestore(l.ID, commit, path);
  if (!v) {
    tell(
      commit
        ? "当前内容已与所选版本一致，无需创建新版本。"
        : "当前没有待核对的恢复预览。",
    );
    return false;
  }
  const code = field("输入确认码");
  const ok = await dialog(
    "预览恢复结果",
    [
      line("恢复位置", l.Name + " / " + v.SourcePath),
      line("历史版本", v.SourceCommit.slice(0, 12)),
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
    { valid: () => code.input.value === v.Candidate },
  );
  await api.FinishRestore(v, ok);
  if (ok) tell("恢复完成，已发布新的资料库版本。");
  return ok;
}
export function previewBody(v: Preview, name: string, thumbnail = false) {
  const el = box("preview");
  if (v.Media === "text/plain")
    el.append(n("pre", "", v.Content || "（空文件）"));
  else if (v.Media.startsWith("image/")) {
    const img = n("img");
    img.alt = name;
    img.src = `data:${v.Media};base64,${v.Content}`;
    el.append(img);
  } else if (v.Media === "application/pdf") {
    void renderPDF(el, v.Content, name, thumbnail).catch(() =>
      el.replaceChildren(sub("无法预览此 PDF，请下载原文件查看。")),
    );
  }
  if (v.Notice) el.append(sub(v.Notice));
  return el;
}
async function renderPDF(
  el: HTMLElement,
  content: string,
  name: string,
  thumbnail: boolean,
) {
  const { getDocument, GlobalWorkerOptions } = await import(
    "/pdfjs/pdf.min.mjs"
  );
  if (!el.isConnected) return;
  GlobalWorkerOptions.workerSrc = "/pdfjs/pdf.worker.min.mjs";
  const task = getDocument({
    data: Uint8Array.from(atob(content), (c) => c.charCodeAt(0)),
    cMapUrl: "/pdfjs/cmaps/",
    cMapPacked: true,
    standardFontDataUrl: "/pdfjs/standard_fonts/",
    disableFontFace: true,
    isEvalSupported: false,
    useWasm: false,
  });
  const d = el.closest("dialog");
  d?.addEventListener("close", () => void task.destroy(), { once: true });
  const doc = await task.promise;
  let index = 1;
  const controls = box("pdf-tools"),
    canvas = n("canvas");
  canvas.setAttribute("aria-label", name);
  el.replaceChildren(controls, canvas);
  const draw = async () => {
    const p = await doc.getPage(index),
      base = p.getViewport({ scale: 1 }),
      width = thumbnail ? 300 : Math.min(1100, window.innerWidth),
      viewport = p.getViewport({
        scale: Math.min(width / base.width, 1400 / base.height),
      });
    canvas.width = Math.ceil(viewport.width);
    canvas.height = Math.ceil(viewport.height);
    controls.replaceChildren(sub("正在绘制…"));
    await p.render({ canvas, viewport }).promise;
    if (!thumbnail) {
      const prev = btn("上一页", () => {
          index--;
          return draw();
        }),
        next = btn("下一页", () => {
          index++;
          return draw();
        });
      prev.disabled = index === 1;
      next.disabled = index === doc.numPages;
      controls.replaceChildren(
        prev,
        n("span", "", `${index} / ${doc.numPages}`),
        next,
      );
    } else controls.remove();
  };
  try {
    await draw();
  } finally {
    if (thumbnail) await task.destroy();
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
      sub("你可以在本地文件夹中整理两个文件，保存后的更改会在下一轮同步。"),
      actions,
    );
  function select(pair: Conflict) {
    list.replaceChildren(
      ...(l.Conflicts || []).map((p) =>
        btn(
          p.Source + " · " + l.Name,
          () => select(p),
          p.Target === pair.Target ? "choice selected" : "choice",
          "file",
        ),
      ),
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
          n("strong", "filename", name),
          body,
        ),
      };
    });
    panels.replaceChildren(...bodies.map((b) => b.panel));
    actions.replaceChildren(
      ...[false, true].map((side) =>
        btn(
          side ? "打开冲突副本" : "打开原文件",
          async () => {
            const name = side ? pair.Target : pair.Source;
            await dialog(
              name,
              [previewBody(await api.ConflictPreview(l.ID, pair, side), name)],
              "",
              { wide: true, cancel: "关闭" },
            );
          },
          "",
          "file",
        ),
      ),
      btn("打开本地文件夹", () => api.OpenWorktree(l.ID), "primary", "folder"),
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
                  j + from >= start && j + from < lines[i].length - end
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
      .catch((e) => panels.replaceChildren(sub(errorText(e))));
  }
  select(initial);
  return pane;
}
