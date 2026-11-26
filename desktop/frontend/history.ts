import {
  api,
  n,
  box,
  btn,
  sub,
  empty,
  load,
  date,
  childPath,
  fileType,
  type LibraryView,
  type Commit,
  type FileEntry,
} from "./ui.js";
import { reviewRestore } from "./flows.js";

export function history(
  l: LibraryView,
  host: HTMLElement,
  deviceName: string,
  changed: () => void,
) {
  if (l.Phase !== "bound") {
    host.replaceChildren(
      empty(
        "同步该资料库后查看历史",
        "请选择本地文件夹并同步该资料库，再查看或恢复历史版本。",
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
    let target: string | undefined;
    const list = box("snapshot-list");
    const restore = btn(
      "预览恢复",
      async () => {
        if (
          target !== undefined &&
          (await reviewRestore(l, c.CommitId, target))
        ) {
          changed();
          history(l, host, deviceName, changed);
        }
      },
      "primary",
    );
    restore.disabled = true;
    tree.replaceChildren(
      n("h2", "", date(c.CreatedAt)),
      sub("从这个版本选择一个文件或文件夹"),
      list,
      box("actions end", restore),
    );
    const branch = (dir: string, container: HTMLElement) => {
      const children = box("tree-children");
      const page = async (offset: number) => {
        const d = await api.Files(l.ID, c.CommitId, dir, offset);
        if (!offset) container.replaceChildren(children);
        children.querySelector(":scope > .more")?.remove();
        for (const e of d.Entries || [])
          children.append(item(childPath(dir, e.Name), e));
        if (d.NextOffset)
          children.append(btn("加载更多", () => page(d.NextOffset), "more"));
        if (!children.children.length) children.append(sub("目录为空。"));
      };
      load(container, () => page(0));
    };
    const item = (path: string, e: Pick<FileEntry, "Name" | "Type">) => {
      const folder = e.Type === "Directory",
        kind = fileType(e.Name, e.Type),
        entry = box("tree-row"),
        nested = box("nested");
      nested.hidden = true;
      const name = btn(
        e.Name,
        () => {
          target = path;
          restore.disabled = false;
          list.querySelector(".selected")?.classList.remove("selected");
          entry.classList.add("selected");
        },
        "bare tree-name",
        kind.icon,
      );
      if (folder) {
        const expand = btn(
          "展开 " + e.Name,
          () => {
            nested.hidden = !nested.hidden;
            expand.setAttribute("aria-expanded", String(!nested.hidden));
            if (!nested.hidden && !nested.childElementCount)
              branch(path, nested);
          },
          "bare expander",
          "chevron",
          true,
        );
        expand.setAttribute("aria-expanded", "false");
        entry.append(expand);
        if (path === ".") {
          nested.hidden = false;
          expand.setAttribute("aria-expanded", "true");
          branch(path, nested);
        }
      }
      entry.append(name);
      return box("tree-node", entry, nested);
    };
    list.append(item(".", { Name: l.Name, Type: "Directory" }));
  };
  const entry = (c: Commit, current: boolean) => {
    const b = btn(date(c.CreatedAt), () => select(c, b), "version");
    if (current) b.append(n("small", "tag", "当前版本"));
    const source =
      c.DeviceId && c.DeviceId === l.DeviceId
        ? deviceName || "此设备"
        : c.DeviceId
          ? "设备 " + c.DeviceId.slice(0, 8)
          : "";
    b.append(
      sub([c.Message || "同步更新", source].filter(Boolean).join(" · ")),
    );
    b.title = date(c.CreatedAt) + " · " + c.CommitId;
    return b;
  };
  const page = async (cursor: string) => {
    const h = await api.History(l.ID, cursor);
    if (!cursor) host.replaceChildren(box("history-layout", rail, tree));
    rail.querySelector(".more")?.remove();
    for (const c of h.Commits || []) {
      const b = entry(c, first);
      rail.append(b);
      if (first) {
        first = false;
        select(c, b);
      }
      if (c.Parents?.length === 2) {
        const sources = n("details", "sources", n("summary", "", "合并来源")),
          content = box("");
        sources.append(content);
        rail.append(sources);
        const add = async (id: string, first = false) => {
          const m = await api.MergeSource(l.ID, id);
          if (first) content.replaceChildren();
          content.append(entry(m, false));
          if (m.Parents?.length === 2) {
            const next = btn(
              "展开更早来源",
              async () => {
                await add(m.Parents![1]);
                next.remove();
              },
              "link",
            );
            content.append(next);
          }
        };
        sources.ontoggle = () => {
          if (sources.open && !content.children.length)
            load(content, () => add(c.Parents![1], true));
        };
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
