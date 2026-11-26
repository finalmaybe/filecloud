import {
  api,
  n,
  box,
  btn,
  icon,
  tabs,
  sub,
  section,
  heading,
  empty,
  search,
  filterRows,
  noMatch,
  line,
  bytes,
  date,
  field,
  toggle,
  dialog,
  tell,
  errorText,
  pickRow,
  type LibraryView,
  type Directory,
  type FileEntry,
  type Commit,
  type View,
  type TrashEntry,
} from "./ui.js";
import { previewBody, reviewRestore, unbind } from "./flows.js";
export const detailTabs = ["文件", "版本历史", "回收站", "设置"];
const join = (dir: string, name: string) =>
  dir === "." ? name : `${dir}/${name}`;
const parent = (path: string) => path.split("/").slice(0, -1).join("/") || ".";
const leaf = (path: string) => path.split("/").pop()!;
const kind = (e: FileEntry) =>
  e.Type === "Directory"
    ? "文件夹"
    : e.Name.includes(".")
      ? e.Name.split(".").pop()!.toUpperCase() + " 文件"
      : "文件";
function load(host: HTMLElement, work: () => Promise<void>) {
  host.replaceChildren(sub("正在读取…"));
  void work().catch((e) =>
    host.replaceChildren(
      empty("暂时无法读取", errorText(e), "info"),
      btn("重试", () => load(host, work)),
    ),
  );
}
export function libraryDetail(
  l: LibraryView,
  tab: string,
  view: View,
  go: (tab: string) => void,
  back: () => void,
  changed: () => Promise<void>,
) {
  const root = box("workspace"),
    main = n("section", "pane"),
    inside = box("detail-body"),
    inspector = n("aside", "inspector");
  inspector.hidden = true;
  root.append(main, inspector);
  const toolbar = box(
    "toolbar",
    btn("返回资料库", back, "bare", "back", true),
    n("span", "crumb", "资料库 / " + tab),
  );
  main.append(
    heading(l.Name, tab === "文件" ? "资料库当前版本" : ""),
    toolbar,
    tabs(detailTabs, tab, go),
    inside,
  );
  if (tab === "文件") files(l, inside, inspector, toolbar);
  else if (tab === "版本历史") history(l, inside);
  else if (tab === "回收站") trash(l, inside, inspector);
  else retention(l, inside, view, changed);
  return root;
}
function files(
  l: LibraryView,
  host: HTMLElement,
  side: HTMLElement,
  bar: HTMLElement,
) {
  let grid = false,
    showInfo = true;
  const nav = (dir: string, commit: string) => {
    const pane = box(""),
      crumb = box("crumb", l.Name + (dir === "." ? "" : " / " + dir));
    const results = box(""),
      trail = bar.firstElementChild!;
    let term = "";
    const filter = search("筛选已加载项目", "", (v) => {
      term = v;
      filterRows(results, v);
    });
    const layout = (on: boolean) => {
      grid = on;
      results.classList.toggle("file-grid", on);
    };
    bar.replaceChildren(
      trail,
      ...(dir !== "."
        ? [btn("上一级", () => nav(parent(dir), commit), "bare", "back", true)]
        : []),
      crumb,
      filter,
      btn("列表视图", () => layout(false), "square", "list", true),
      btn("网格视图", () => layout(true), "square", "grid", true),
      btn(
        "显示或隐藏检查器",
        () => {
          showInfo = !showInfo;
          side.hidden = !showInfo || !side.children.length;
        },
        "square",
        "info",
        true,
      ),
    );
    if (l.Phase === "bound")
      bar.append(
        btn(
          "打开本地文件夹",
          () => api.OpenWorktree(l.ID),
          "square",
          "folder",
          true,
        ),
      );
    host.replaceChildren(pane);
    side.hidden = true;
    side.replaceChildren();
    let anchor = commit;
    const add = async (offset: number) => {
      const d = await api.Files(l.ID, anchor, dir, offset);
      anchor = d.CommitId;
      if (!offset) {
        pane.replaceChildren(
          box(
            "file-cols table-head",
            n("span", "", "名称"),
            n("span", "", "修改时间"),
            n("span", "", "大小"),
            n("span", "", "类型"),
          ),
          results,
        );
        layout(grid);
      } else pane.querySelector(".more")?.remove();
      for (const e of d.Entries || []) {
        const isDir = e.Type === "Directory",
          file = join(dir, e.Name);
        const row = n(
          "button",
          "file-cols file-row",
          box(
            "identity",
            icon(isDir ? "folder" : "file", isDir ? "blue" : "file-icon"),
            n("strong", "", e.Name),
          ),
          n("span", "", date(e.ModifiedAt)),
          n("span", "", bytes(e.Size)),
          n("span", "", kind(e)),
        );
        row.type = "button";
        row.dataset.match = e.Name;
        const inspect = () => {
          results.querySelector(".selected")?.classList.remove("selected");
          row.classList.add("selected");
          showInfo = true;
          side.hidden = false;
          const details = box("");
          side.replaceChildren(details);
          let loaded: ReturnType<typeof api.PreviewFile> | undefined;
          const getPreview = () =>
            (loaded ??= api.PreviewFile(l.ID, anchor, file));
          const preview = async () => {
            const v = await getPreview();
            await dialog(e.Name, [previewBody(v, e.Name)], "", {
              wide: true,
              cancel: "关闭",
            });
          };
          row.onkeydown = (ev) => {
            if (ev.key === " " && !isDir) {
              ev.preventDefault();
              void preview().catch((x) => tell(errorText(x)));
            }
          };
          details.append(
            box(
              "document-cover",
              icon(isDir ? "folder" : "file", "blue"),
              n("strong", "", isDir ? "文件夹" : kind(e)),
            ),
            n("h2", "", e.Name),
            sub(kind(e) + " · " + bytes(e.Size)),
            box("metadata", icon("folder"), file),
            box("metadata", icon("history"), "修改于 " + date(e.ModifiedAt)),
            isDir
              ? btn("打开文件夹", () => nav(file, anchor), "", "folder")
              : box(
                  "actions",
                  btn("快速预览", preview, "", "eye"),
                  btn(
                    "下载文件",
                    () => api.DownloadFile(l.ID, anchor, file),
                    "",
                    "download",
                  ),
                ),
          );
          if (!isDir) {
            if (
              Number(e.Size) <= 6 * 1024 * 1024 &&
              /\.(pdf|png|jpe?g|gif|webp)$/i.test(e.Name)
            ) {
              const cover = details.querySelector(".document-cover")!;
              void getPreview()
                .then((v) => {
                  if (v.Media && cover.isConnected)
                    cover.replaceChildren(previewBody(v, e.Name, true));
                })
                .catch(() => {
                  loaded = undefined;
                });
            }
            if (l.Phase === "bound")
              details.append(
                btn(
                  "查看本机文件",
                  async () => {
                    const local = await api.LocalPreview(l.ID, file);
                    await dialog(
                      e.Name + " · 本机文件",
                      [
                        sub(l.Worktree + " / " + file),
                        previewBody(local, e.Name),
                      ],
                      "",
                      { wide: true, cancel: "关闭" },
                    );
                  },
                  "link",
                  "device",
                ),
              );
            const versions = section("历史版本");
            details.append(versions);
            const more = async (cursor: string) => {
              const h = await api.FileVersions(l.ID, anchor, file, cursor);
              versions.querySelector(".more")?.remove();
              for (const v of h.Versions || []) {
                const label = date(v.CreatedAt);
                versions.append(
                  line(
                    (v.IsCurrent ? "当前版本 · " : "") + label,
                    ...(!v.IsCurrent
                      ? [
                          btn(
                            "恢复此版本",
                            async () => {
                              if (
                                await dialog(
                                  "恢复此版本？",
                                  [
                                    line("文件", file),
                                    line("版本时间", label),
                                    sub(
                                      "仅恢复当前同路径文件，其他文件保持不变。",
                                    ),
                                  ],
                                  "恢复此版本",
                                )
                              ) {
                                const head = await api.RestoreVersion(
                                  l.ID,
                                  v.CommitId,
                                  file,
                                  anchor,
                                );
                                tell(
                                  head === anchor
                                    ? "内容和修改时间相同，未创建新版本。"
                                    : "已恢复所选文件版本。",
                                );
                                nav(dir, "");
                              }
                            },
                            "link",
                          ),
                        ]
                      : []),
                  ),
                );
              }
              if (h.NextPageToken)
                versions.append(
                  btn("查看更多版本", () => more(h.NextPageToken), "more"),
                );
            };
            void more("").catch((e) => versions.append(sub(errorText(e))));
          }
        };
        row.onclick = inspect;
        row.ondblclick = () => {
          if (isDir) nav(file, anchor);
        };
        results.append(row);
      }
      if (!results.children.length)
        results.append(
          empty(
            "此目录为空",
            "此处显示已发布的资料库文件，不是本地未同步的修改。",
          ),
        );
      else if (!results.querySelector(".no-match")) results.append(noMatch());
      if (results.querySelector("[data-match]")) filterRows(results, term);
      if (d.NextOffset)
        pane.append(btn("加载更多项目", () => add(d.NextOffset), "more"));
    };
    load(pane, () => add(0));
  };
  nav(".", "");
}
function history(l: LibraryView, host: HTMLElement) {
  if (l.Phase !== "bound") {
    host.append(
      empty(
        "连接文件夹后查看历史",
        "主线历史与快照恢复使用此设备的绑定凭据。",
        "history",
      ),
    );
    return;
  }
  const rail = box("timeline"),
    tree = box("tree");
  let first = true;
  const select = (c: Commit, row: HTMLElement) => {
    rail.querySelector(".selected")?.classList.remove("selected");
    row.classList.add("selected");
    const browse = (dir: string) => {
      const panel = box("");
      tree.replaceChildren(
        n("h2", "", date(c.CreatedAt)),
        sub("从这个版本选择一个文件或文件夹"),
        panel,
      );
      let selected = dir;
      const restore = btn(
        "预览恢复",
        async () => {
          if (await reviewRestore(l, c.CommitId, selected)) {
            host.replaceChildren();
            history(l, host);
          }
        },
        "primary",
      );
      const list = box("snapshot-list"),
        where = box("toolbar", n("strong", "", dir === "." ? l.Name : dir));
      if (dir !== ".")
        where.prepend(
          btn("上一级", () => browse(parent(dir)), "bare", "back", true),
        );
      const page = async (offset: number) => {
        const d = await api.Files(l.ID, c.CommitId, dir, offset);
        if (!offset)
          panel.replaceChildren(where, list, box("actions end", restore));
        list.querySelector(".more")?.remove();
        for (const e of d.Entries || []) {
          const p = join(dir, e.Name),
            isDir = e.Type === "Directory";
          const item = box(
            "tree-row",
            btn(
              e.Name,
              () => {
                selected = p;
                list.querySelector(".selected")?.classList.remove("selected");
                item.classList.add("selected");
              },
              "bare",
              isDir ? "folder" : "file",
            ),
          );
          if (isDir)
            item.append(
              btn("进入 " + e.Name, () => browse(p), "bare", "chevron", true),
            );
          list.append(item);
        }
        if (d.NextOffset)
          list.append(btn("加载更多", () => page(d.NextOffset), "more"));
        if (!list.children.length) list.append(sub("此快照目录为空。"));
      };
      load(panel, () => page(0));
    };
    browse(".");
  };
  const page = async (cursor: string) => {
    const h = await api.History(l.ID, cursor);
    if (!cursor) {
      host.replaceChildren(box("history-layout", rail, tree));
      rail.replaceChildren();
    }
    rail.querySelector(".more")?.remove();
    for (const c of h.Commits || []) {
      const row = btn(date(c.CreatedAt), () => select(c, row), "version");
      row.append(
        sub(c.Message || "同步更新"),
        n("code", "sub", c.CommitId.slice(0, 12)),
      );
      if (first) row.prepend(n("small", "tag", "当前版本"));
      rail.append(row);
      if (first) {
        first = false;
        select(c, row);
      }
      if ((c.Parents || []).length === 2) {
        const sources = box("sources");
        rail.append(sources);
        const addSource = (id: string) => {
          const b = btn(
            "展开合并来源",
            async () => {
              const m = await api.MergeSource(l.ID, id);
              const source = btn(
                date(m.CreatedAt),
                () => select(m, source),
                "version",
              );
              source.append(sub(m.Message || "合并来源"));
              b.replaceWith(source);
              if (m.Parents?.length === 2) addSource(m.Parents[1]);
            },
            "link",
          );
          sources.append(b);
        };
        addSource(c.Parents![1]);
      }
    }
    if (h.NextPageToken)
      rail.append(btn("加载更早版本", () => page(h.NextPageToken), "more"));
    if (!rail.children.length)
      host.replaceChildren(
        empty("暂无版本", "首次发布后，主线版本会出现在这里。", "history"),
      );
  };
  load(host, () => page(""));
}
function trash(l: LibraryView, host: HTMLElement, side: HTMLElement) {
  let type = "全部",
    term = "";
  const table = box(""),
    tools = box("toolbar");
  const rows: TrashEntry[] = [];
  let next = 0;
  const draw = () => {
    tools.replaceChildren(
      tabs(
        ["全部", "文件", "文件夹", "历史版本"],
        type,
        (t) => {
          type = t;
          side.hidden = true;
          draw();
        },
        "segments",
      ),
      search("筛选本页名称或路径", term, (v) => {
        term = v;
        filterRows(table, v);
      }),
    );
    table.replaceChildren();
    for (const e of rows) {
      const name =
        e.Kind === "Directory"
          ? "文件夹"
          : e.Kind === "Version"
            ? "历史版本"
            : "文件";
      if (type !== "全部" && type !== name) continue;
      const row = n(
        "button",
        "trash-cols file-row",
        box(
          "identity",
          icon(e.Kind === "Directory" ? "folder" : "file", "blue"),
          leaf(e.Path),
        ),
        n("span", "", parent(e.Path)),
        n("span", "", date(e.DeletedAt)),
        n("span", "", date(e.ExpiresAt)),
      );
      row.dataset.match = e.Path;
      row.onclick = () => {
        table.querySelector(".selected")?.classList.remove("selected");
        row.classList.add("selected");
        side.hidden = false;
        const ver = e.Kind === "Version",
          action = ver
            ? "放回版本列表"
            : e.Kind === "Directory"
              ? "恢复整个目录"
              : "恢复文件";
        const dest = field("恢复位置（相对路径）", "text", e.Path);
        const feedback = sub(
          ver
            ? "仅恢复版本列表记录，当前文件保持不变。"
            : "同名时请改名或取消，不会覆盖或合并。",
        );
        const restore = btn(
          action,
          async () => {
            try {
              await api.RestoreTrash(l.ID, e.ID, ver ? "" : dest.input.value);
              side.hidden = true;
              tell("已恢复所选条目。");
              await page(0);
            } catch (e) {
              feedback.className = "feedback";
              feedback.textContent = errorText(e);
            }
          },
          "primary",
        );
        side.replaceChildren(
          n("h2", "", action),
          box("identity", icon("file", "blue"), leaf(e.Path)),
          section(
            "恢复位置",
            sub(l.Name + " / " + e.Path),
            ...(!ver ? [dest.label] : []),
            feedback,
          ),
          box(
            "actions end",
            btn("取消", () => {
              side.hidden = true;
            }),
            restore,
          ),
        );
      };
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
    host.replaceChildren(
      tools,
      box(
        "trash-cols table-head",
        ...["名称", "原位置", "删除时间", "到期"].map((t) => n("span", "", t)),
      ),
      table,
      ...(next ? [btn("加载更多", () => page(next), "more")] : []),
      sub("恢复的文件或目录会在下一轮同步到其他设备。"),
    );
  };
  const page = async (before: number) => {
    const p = await api.Trash(l.ID, before);
    if (!before) rows.length = 0;
    rows.push(...(p.Entries || []));
    next = p.Next;
    draw();
  };
  load(host, () => page(0));
}
function retention(
  l: LibraryView,
  host: HTMLElement,
  v: View,
  changed: () => Promise<void>,
) {
  const policy = section("资料库保留设置");
  host.append(
    section(
      "本机绑定",
      line("本机位置", l.Worktree || "尚未连接"),
      ...(l.Phase === "bound"
        ? [
            box(
              "actions",
              btn("打开本地文件夹", () => api.OpenWorktree(l.ID), "", "folder"),
              btn("刷新绑定凭据", () => api.RetryBinding(l.ID)),
              ...(!l.Running && l.Pending === "none"
                ? [
                    btn("解除绑定", async () => {
                      await unbind(l);
                      await changed();
                    }),
                  ]
                : []),
            ),
            sub("解绑保留本地文件和远端资料库。"),
          ]
        : []),
    ),
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
  load(form, async () => {
    const p = await api.LibraryInfo(l.ID),
      trash = field(
        "回收站保留时间（天）",
        "number",
        p.TrashDays ? String(p.TrashDays) : "",
      ),
      hist = field(
        "历史版本保留时间（天）",
        "number",
        p.HistoryDays > 0 ? String(p.HistoryDays) : "",
      ),
      perm = toggle("永久保留历史", p.HistoryDays === -1);
    for (const [f, d] of [
      [trash, v.DefaultTrashDays],
      [hist, v.DefaultHistoryDays],
    ] as const) {
      f.input.min = "1";
      f.input.max = "36500";
      f.input.placeholder = `跟随服务器默认${d > 0 ? " · " + d + " 天" : ""}`;
    }
    hist.input.disabled = perm.input.checked;
    perm.input.onchange = () => {
      hist.input.disabled = perm.input.checked;
    };
    form.replaceChildren(
      trash.label,
      hist.label,
      perm.label,
      sub("当前版本始终保留；回收站新设置只影响之后删除的项目。"),
      btn(
        "保存更改",
        async () => {
          if (!trash.input.reportValidity() || !hist.input.reportValidity())
            return;
          const next = {
              TrashDays: Number(trash.input.value),
              HistoryDays: perm.input.checked ? -1 : Number(hist.input.value),
            },
            impact = await api.Retention(l.ID, next, false, "");
          if (
            impact.Token &&
            !(await dialog(
              "缩短历史保留时间",
              [
                line("资料库", l.Name),
                line(
                  "新的保留期",
                  `${next.HistoryDays || v.DefaultHistoryDays} 天`,
                ),
                line("预计过期快照", String(impact.Count)),
                line("关联独占内容", bytes(impact.Bytes)),
                line("受影响的已恢复记录", String(impact.RestoredRecords)),
                sub("到期历史内容将无法恢复，清理会在后台运行。"),
              ],
              "确认更改",
            ))
          )
            return;
          await api.Retention(l.ID, next, true, impact.Token);
          tell("保留设置已保存。");
        },
        "primary",
      ),
    );
  });
}
