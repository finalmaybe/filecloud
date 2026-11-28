import {
  api,
  n,
  box,
  btn,
  icon,
  tabs,
  sub,
  empty,
  search,
  filterRows,
  noMatch,
  date,
  field,
  tell,
  errorText,
  fileType,
  parentPath,
  childPath,
  line,
  menu,
  dialog,
  load,
  pickRow,
  type LibraryView,
  type TrashEntry,
} from "./ui.js";

const trashSelectionLimit = 100;

export function trash(
  l: LibraryView,
  host: HTMLElement,
  side: HTMLElement,
  changed: () => void,
) {
  let type = "全部",
    term = "",
    next = 0,
    inspected: HTMLElement | undefined,
    rows: TrashEntry[] = [];
  const table = box(""),
    tools = box("toolbar trash-tools"),
    selected = new Set<number>(),
    checks = new Map<number, { row: HTMLElement; check: HTMLInputElement }>();
  const batch = btn(
    "永久删除所选",
    () => {
      updateSelection();
      const ids = [...selected];
      if (ids.length) return confirmDelete(ids, `已选择 ${ids.length} 个条目`);
    },
    "",
    "trash",
  );
  batch.setAttribute("aria-haspopup", "dialog");
  const count = n("span");
  batch.append(count);
  const updateSelection = () => {
    for (const id of selected) {
      const entry = checks.get(id);
      if (!entry || entry.row.hidden) selected.delete(id);
    }
    const full = selected.size >= trashSelectionLimit;
    for (const [id, { check }] of checks) {
      check.checked = selected.has(id);
      check.disabled = full && !check.checked;
      check.title = check.disabled
        ? `每次最多选择 ${trashSelectionLimit} 个条目`
        : "";
    }
    batch.disabled = selected.size === 0;
    count.textContent = `（${selected.size}）`;
    batch.setAttribute("aria-label", `永久删除所选${count.textContent}`);
  };
  const confirmDelete = async (ids: number[], target: string) => {
    const accepted = await dialog(
      "永久删除回收站条目？",
      [
        n("strong", "crumb", target),
        sub("删除后无法从回收站恢复，当前文件和快照不受影响。"),
      ],
      "确认永久删除",
      {
        danger: true,
        symbol: "trash",
        onAccept: () => api.DeleteTrash(l.ID, ids),
      },
    );
    if (!accepted) return;
    const deleted = new Set(ids);
    rows = rows.filter((entry) => !deleted.has(entry.ID));
    draw();
    tell(`已永久删除 ${ids.length} 个回收站条目。`);
  };
  const clear = () => {
    side.hidden = true;
    side.replaceChildren();
    inspected?.classList.remove("selected");
    inspected = undefined;
  };
  const draw = () => {
    clear();
    checks.clear();
    tools.replaceChildren(
      tabs(
        ["全部", "文件", "文件夹", "历史版本"],
        type,
        (t) => {
          type = t;
          draw();
        },
        "segments",
      ),
      search("筛选已加载的名称或路径", term, (v) => {
        term = v;
        filterRows(table, v);
        if (inspected?.hidden) clear();
        updateSelection();
      }),
      batch,
    );
    table.replaceChildren();
    for (const e of rows) {
      const category =
        e.Kind === "Directory"
          ? "文件夹"
          : e.Kind === "Version"
            ? "历史版本"
            : "文件";
      if (type !== "全部" && type !== category) continue;
      const leaf = e.Path.split("/").pop()!,
        parent = parentPath(e.Path),
        ver = e.Kind === "Version",
        action = ver
          ? "放回版本列表"
          : e.Kind === "Directory"
            ? "恢复整个目录"
            : "恢复文件",
        kind = fileType(leaf, e.Kind);
      const days = Math.max(
          0,
          Math.ceil((Date.parse(e.ExpiresAt) - Date.now()) / 86400000),
        ),
        expires = n("span", "", days ? `剩余 ${days} 天` : "即将到期");
      expires.title = date(e.ExpiresAt);
      const check = n("input", "trash-check");
      check.type = "checkbox";
      check.setAttribute("aria-label", `选择 ${e.Path}`);
      const row = box(
        "trash-cols file-row",
        box(
          "identity",
          check,
          icon(kind.icon, kind.color),
          box("", n("strong", "", leaf), ...(ver ? [sub("历史版本")] : [])),
        ),
        n("span", "", parent === "." ? l.Name : parent),
        n("span", "", date(e.DeletedAt)),
        expires,
      );
      row.dataset.match = e.Path;
      checks.set(e.ID, { row, check });
      check.onchange = () => {
        if (!check.checked) selected.delete(e.ID);
        else if (!row.hidden && selected.size < trashSelectionLimit)
          selected.add(e.ID);
        updateSelection();
      };
      const inspect = () => {
        inspected?.classList.remove("selected");
        inspected = row;
        row.classList.add("selected");
        side.hidden = false;
        const name = field("恢复后的名称", "text", leaf),
          dir = field("目录（相对于资料库）", "text", parent),
          feedback = sub(""),
          submit = btn(
            action,
            async () => {
              try {
                await api.RestoreTrash(l.ID, e.ID, ver ? "" : dest());
                changed();
                tell(
                  ver
                    ? "已放回版本列表，当前文件保持不变。"
                    : "已恢复所选项目，下轮同步到其他设备。",
                );
                await page(0);
              } catch (err) {
                feedback.className = "feedback";
                feedback.textContent = errorText(err);
                if (!ver && errorText(err).includes("同名"))
                  submit.disabled = true;
              }
            },
            "primary",
          );
        const dest = () => childPath(dir.input.value || ".", name.input.value);
        const check = async () => {
          const target = dest();
          submit.disabled = true;
          feedback.className = "sub";
          if (
            !name.input.value ||
            name.input.value.includes("/") ||
            !dir.input.value
          ) {
            feedback.textContent = "请填写恢复名称和目录，根目录用「.」表示。";
            return;
          }
          feedback.textContent = "正在检查恢复位置…";
          try {
            const free = await api.CheckRestoreLocation(l.ID, target);
            if (target !== dest()) return;
            feedback.className = free ? "green" : "feedback";
            feedback.textContent = free
              ? "此名称可以使用"
              : "该位置已有同名项目，请修改恢复名称或目录。";
            submit.disabled = !free;
          } catch (e) {
            if (target !== dest()) return;
            const fallback =
              (e instanceof Error ? e.message : String(e)) ===
              "check_unavailable";
            feedback.className = "feedback";
            feedback.textContent = fallback
              ? "当前服务端将在恢复时检查名称，同名不会覆盖。"
              : errorText(e);
            submit.disabled = !fallback;
          }
        };
        const controls = box("actions end", btn("取消", clear), submit);
        side.replaceChildren(
          n("h2", "", action),
          box(
            "inspector-head",
            icon(kind.icon, kind.color),
            n("strong", "", leaf),
          ),
          line("原位置", l.Name + " / " + e.Path),
          line("删除时间", date(e.DeletedAt)),
          line("到期时间", date(e.ExpiresAt)),
        );
        if (ver)
          side.append(
            sub("只放回该路径的版本列表，不替换当前文件。"),
            feedback,
            controls,
          );
        else {
          side.append(
            n("h3", "", "恢复位置"),
            name.label,
            dir.label,
            feedback,
            sub("不会覆盖现有文件或合并目录。"),
            controls,
          );
          let timer = 0;
          const edit = () => {
            submit.disabled = true;
            feedback.textContent = "等待检查名称…";
            clearTimeout(timer);
            timer = window.setTimeout(() => {
              if (feedback.isConnected) void check();
            }, 200);
          };
          name.input.oninput = dir.input.oninput = edit;
          void check();
        }
      };
      if (ver) expires.append(btn("放回版本列表", inspect, "record-action"));
      pickRow(row, inspect, e.Path);
      const remove = btn(
        "永久删除",
        () => confirmDelete([e.ID], e.Path),
        "menu-link trash-delete",
        "trash",
      );
      remove.setAttribute("aria-haspopup", "dialog");
      row.append(
        menu(leaf + "的更多操作", [
          btn(action, inspect, "menu-link", "history"),
          remove,
        ]),
      );
      table.append(row);
    }
    if (table.children.length) {
      table.append(noMatch());
      filterRows(table, term);
    } else
      table.append(
        empty(
          "没有此类回收站记录",
          "已删除项目会显示原位置、删除时间与到期时间。",
          "trash",
        ),
      );
    updateSelection();
    host.replaceChildren(
      tools,
      box(
        "trash-cols table-head",
        ...["名称", "原位置", "删除时间", "到期", ""].map((t) =>
          n("span", "", t),
        ),
      ),
      table,
      ...(next ? [btn("加载更多", () => page(next), "more")] : []),
      box(
        "metadata",
        icon("info"),
        "恢复的文件或目录会在下一轮同步到其他设备。",
      ),
    );
  };
  const page = async (before: number) => {
    const p = await api.Trash(l.ID, before);
    if (!before) rows = [];
    rows.push(...(p.Entries || []));
    next = p.Next;
    draw();
  };
  load(host, () => page(0));
}
