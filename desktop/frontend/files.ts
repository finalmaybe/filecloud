import {
  api,
  n,
  box,
  btn,
  icon,
  sub,
  section,
  empty,
  search,
  filterRows,
  noMatch,
  bytes,
  date,
  dialog,
  tell,
  errorText,
  errorCode,
  pickRow,
  fileType,
  menu,
  canOpenDocument,
  load,
  childPath,
  parentPath,
  type LibraryView,
  type FileEntry,
  type FileVersion,
} from "./ui.js";
import { previewBody, showPreview, type PreviewItem } from "./preview.js";

export function files(
  l: LibraryView,
  host: HTMLElement,
  side: HTMLElement,
  bar: HTMLElement,
  back: () => void,
) {
  let grid = false;
  const nav = (dir: string, commit: string) => {
    const pane = box(""),
      results = box(""),
      header = box(
        "file-cols table-head",
        ...["名称", "修改时间", "大小", "类型", ""].map((t) =>
          n("span", "", t),
        ),
      );
    const entries: FileEntry[] = [];
    let anchor = commit,
      term = "",
      chosen: HTMLElement | undefined;
    const info = btn(
      "显示或隐藏检查器",
      () => {
        side.hidden = !side.hidden;
        info.setAttribute("aria-pressed", String(!side.hidden));
      },
      "square",
      "info",
      true,
    );
    info.disabled = true;
    info.setAttribute("aria-pressed", "false");
    const filter = search("筛选已加载项目", "", (v) => {
      term = v;
      filterRows(results, v);
      if (chosen?.hidden) {
        chosen.classList.remove("selected");
        chosen = undefined;
        side.hidden = true;
        side.replaceChildren();
        info.disabled = true;
        info.setAttribute("aria-pressed", "false");
      }
    });
    const list = btn("列表视图", () => layout(false), "square", "list", true),
      tiles = btn("网格视图", () => layout(true), "square", "grid", true);
    function layout(on: boolean) {
      grid = on;
      results.classList.toggle("file-grid", on);
      header.hidden = on;
      for (const [b, active] of [
        [list, !on],
        [tiles, on],
      ] as const) {
        b.classList.toggle("active", active);
        b.setAttribute("aria-pressed", String(active));
      }
    }
    layout(grid);
    bar.replaceChildren(
      dir === "."
        ? btn("返回资料库", back, "", "back", true)
        : btn("上一级", () => nav(parentPath(dir), anchor), "", "back", true),
      box(
        "crumb",
        [l.Name, ...(dir === "." ? [] : dir.split("/"))].join(" / "),
      ),
      filter,
      box("segments view-modes", list, tiles),
      info,
    );
    if (l.Phase === "bound")
      bar.append(
        btn(
          "打开本地文件夹",
          () => api.OpenWorktree(l.ID),
          "primary square",
          "folder",
          true,
        ),
      );
    host.replaceChildren(pane);
    side.hidden = true;
    side.replaceChildren();
    const quick = (e: FileEntry) => {
      const group =
        fileType(e.Name, e.Type).icon === "image"
          ? entries.filter(
              (x) =>
                x.Type === "File" &&
                fileType(x.Name).icon === "image" &&
                x.Name.toLowerCase().includes(term.trim().toLowerCase()),
            )
          : [e];
      const items: PreviewItem[] = group.map((x) => ({
        name: x.Name,
        size: x.Size,
        load: () => api.PreviewFile(l.ID, anchor, childPath(dir, x.Name)),
      }));
      return showPreview(items, group.indexOf(e));
    };
    const add = async (offset: number) => {
      const d = await api.Files(l.ID, anchor, dir, offset);
      anchor = d.CommitId;
      if (!offset) pane.replaceChildren(header, results);
      pane.querySelector(".more")?.remove();
      results.querySelector(".no-match")?.remove();
      for (const e of d.Entries || []) {
        entries.push(e);
        const folder = e.Type === "Directory",
          path = childPath(dir, e.Name),
          kind = fileType(e.Name, e.Type);
        const download = () => api.DownloadFile(l.ID, anchor, path),
          open = () => (folder ? nav(path, anchor) : quick(e)),
          local = () =>
            showPreview([
              {
                name: e.Name + " · 本机文件",
                load: () => api.LocalPreview(l.ID, path),
              },
            ]);
        const row = box(
          "file-cols file-row",
          box("identity", icon(kind.icon, kind.color), n("strong", "", e.Name)),
          n("span", "", date(e.ModifiedAt)),
          n("span", "", folder ? "—" : bytes(e.Size)),
          n("span", "", kind.label),
        );
        row.dataset.match = e.Name;
        const inspect = () => {
          chosen?.classList.remove("selected");
          chosen = row;
          row.classList.add("selected");
          side.hidden = false;
          info.disabled = false;
          info.setAttribute("aria-pressed", "true");
          const cover = box(
              "document-cover",
              icon(kind.icon, kind.color),
              n("strong", "", kind.label),
            ),
            details = box("");
          side.replaceChildren(details);
          details.append(
            cover,
            n("h2", "", e.Name),
            sub(kind.label + " · " + bytes(e.Size)),
            box("metadata", icon("folder"), path.split("/").join(" / ")),
            box("metadata", icon("history"), "修改于 " + date(e.ModifiedAt)),
            folder
              ? btn("打开文件夹", open, "", "folder")
              : box(
                  "actions",
                  btn("快速预览", open, "", "eye"),
                  btn("下载文件", download, "", "download"),
                ),
          );
          if (folder) return;
          if (
            Number(e.Size) <= 12 * 1024 * 1024 &&
            ["pdf", "image"].includes(kind.icon)
          )
            void api
              .PreviewFile(l.ID, anchor, path)
              .then((v) => {
                if (v.Media && cover.isConnected)
                  cover.replaceChildren(previewBody(v, e.Name, true));
              })
              .catch(() => {});
          if (l.Phase === "bound")
            details.append(
              btn(
                canOpenDocument(e.Name) ? "在本机打开" : "预览本机文件",
                canOpenDocument(e.Name)
                  ? () => api.OpenLocalDocument(l.ID, path)
                  : local,
                "link",
                "device",
              ),
            );
          const versions = section("历史版本");
          details.append(versions);
          fileHistory(l, anchor, path, e.ModifiedAt, versions, () =>
            nav(dir, ""),
          );
        };
        const options = [
          btn(
            folder ? "打开文件夹" : "快速预览",
            open,
            "menu-link",
            folder ? "folder" : "eye",
          ),
        ];
        if (!folder)
          options.push(btn("下载文件", download, "menu-link", "download"));
        if (l.Phase === "bound") {
          if (!folder)
            options.push(btn("查看本机文件", local, "menu-link", "device"));
          options.push(
            btn(
              "打开文件位置",
              () => api.RevealLocal(l.ID, path),
              "menu-link",
              "folder",
            ),
          );
        }
        row.append(menu(e.Name + "的更多操作", options));
        pickRow(row, inspect, e.Name);
        row.title = folder ? "双击打开文件夹" : "空格键快速预览";
        row.addEventListener(
          "keydown",
          (ev) => {
            if (ev.target === row && ev.key === " " && !folder) {
              ev.preventDefault();
              void quick(e).catch((e) => tell(errorText(e)));
            }
          },
          true,
        );
        row.ondblclick = (ev) => {
          if (!(ev.target as Element).closest("details,button"))
            void Promise.resolve(open()).catch((e) => tell(errorText(e)));
        };
        results.append(row);
      }
      if (!entries.length)
        results.replaceChildren(
          empty(
            "此目录为空",
            "此处显示已发布的资料库文件，不是本地未同步的修改。",
          ),
        );
      else {
        results.append(noMatch());
        filterRows(results, term);
      }
      if (d.NextOffset)
        pane.append(btn("加载更多项目", () => add(d.NextOffset), "more"));
    };
    load(pane, () => add(0));
  };
  nav(".", "");
}

function fileHistory(
  l: LibraryView,
  anchor: string,
  path: string,
  modified: string,
  section: HTMLElement,
  reload: () => void,
) {
  const rows = box("file-versions");
  section.append(rows);
  let expanded = false,
    cursor = "";
  const versions: FileVersion[] = [];
  const draw = () => {
    rows.replaceChildren(
      box(
        "version-line selected",
        icon("history"),
        n("span", "grow", "当前版本 · " + date(modified)),
      ),
    );
    for (const v of expanded ? versions : versions.slice(0, 2)) {
      const label = date(v.CreatedAt);
      const row = box(
        "version-line",
        icon("history"),
        n("span", "grow", label),
      );
      row.append(
        menu("历史版本操作", [
          btn(
            "恢复此版本",
            async () => {
              if (
                !(await dialog(
                  "恢复此版本？",
                  [
                    sub(l.Name),
                    n("strong", "", path),
                    sub("版本时间：" + label),
                    sub("仅恢复当前同路径文件；其他文件保持不变。"),
                  ],
                  "恢复此版本",
                ))
              )
                return;
              let head: string;
              try {
                head = await api.RestoreVersion(l.ID, v.CommitId, path, anchor);
              } catch (e) {
                if (errorCode(e) !== "stale_candidate") throw e;
                if (
                  await dialog(
                    "文件版本已变化",
                    [
                      sub(path),
                      sub("请重新读取当前版本，再选择要恢复的历史记录。"),
                    ],
                    "重新读取",
                  )
                )
                  reload();
                return;
              }
              tell(
                head === anchor
                  ? "内容和修改时间相同，未创建新版本。"
                  : "已恢复所选文件版本。",
              );
              reload();
            },
            "menu-link",
            "history",
          ),
        ]),
      );
      rows.append(row);
    }
    if ((!expanded && versions.length > 2) || cursor)
      rows.append(
        btn(
          "查看更多版本",
          async () => {
            if (expanded || versions.length <= 2) await page(cursor);
            expanded = true;
            draw();
          },
          "link",
          "chevron",
        ),
      );
  };
  const page = async (token: string) => {
    const h = await api.FileVersions(l.ID, anchor, path, token);
    versions.push(...(h.Versions || []).filter((v) => !v.IsCurrent));
    cursor = h.NextPageToken;
  };
  load(rows, async () => {
    await page("");
    draw();
  });
}
