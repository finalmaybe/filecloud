import {
  box,
  btn,
  bytes,
  errorText,
  fileType,
  icon,
  n,
  sub,
  type Preview,
} from "./ui.js";

export interface PreviewItem {
  name: string;
  load: () => Promise<Preview>;
  size?: string;
}

export function showPreview(items: PreviewItem[], start = 0) {
  const d = n("dialog", "viewer"),
    stage = box("viewer-stage"),
    title = n("strong"),
    tools = box("viewer-tools"),
    footer = sub(""),
    close = btn("关闭预览", () => d.close(), "bare", "close", true);
  let index = start;
  const show = async (next: number) => {
    index = next;
    const item = items[index];
    title.textContent = item.name;
    d.setAttribute("aria-label", item.name + " · 快速预览");
    stage.replaceChildren(sub("正在读取…"));
    footer.textContent = "";
    tools.replaceChildren();
    if (items.length > 1) {
      const prev = btn("上一张", () => show(index - 1), "bare", "back", true),
        after = btn("下一张", () => show(index + 1), "bare", "chevron", true);
      prev.disabled = index === 0;
      after.disabled = index === items.length - 1;
      tools.append(prev, after);
    }
    try {
      const v = await item.load();
      if (!d.open || index !== next) return;
      const size = item.size ?? (v.ModifiedAt ? String(v.Size) : "");
      footer.textContent = [
        v.Media === "text/plain" ? "文本预览" : "",
        v.Notice,
        size ? bytes(size) : "",
      ]
        .filter(Boolean)
        .join(" · ");
      const body = previewBody(v, item.name, false, tools);
      stage.replaceChildren(body);
      const img = body.querySelector("img");
      if (img) {
        const dimensions = () => {
          if (index === next)
            footer.textContent = `${img.naturalWidth} × ${img.naturalHeight}${size ? " · " + bytes(size) : ""}`;
        };
        img.onload = dimensions;
        if (img.complete && img.naturalWidth) dimensions();
      }
    } catch (e) {
      if (index === next) stage.replaceChildren(sub(errorText(e)));
    }
  };
  d.append(box("viewer-bar", close, title, tools), stage, footer);
  d.onkeydown = (e) => {
    if (items.length < 2) return;
    if (e.key === "ArrowLeft" && index > 0) {
      e.preventDefault();
      void show(index - 1);
    } else if (e.key === "ArrowRight" && index + 1 < items.length) {
      e.preventDefault();
      void show(index + 1);
    }
  };
  const done = new Promise<void>(
    (resolve) =>
      (d.onclose = () => {
        d.remove();
        resolve();
      }),
  );
  document.body.append(d);
  d.showModal();
  close.focus();
  void show(start);
  return done;
}

export function previewBody(
  v: Preview,
  name: string,
  thumbnail = false,
  tools?: HTMLElement,
) {
  const el = box("preview");
  if (v.Media === "text/plain") {
    const text = v.Content || "（空文件）";
    const numbers = n(
      "pre",
      "line-numbers",
      Array.from({ length: text.split("\n").length }, (_, i) =>
        String(i + 1),
      ).join("\n"),
    );
    numbers.setAttribute("aria-hidden", "true");
    el.append(box("numbered-text", numbers, n("pre", "text-content", text)));
  } else if (v.Media.startsWith("image/")) {
    const img = n("img");
    img.alt = name;
    img.src = `data:${v.Media};base64,${v.Content}`;
    el.append(img);
  } else if (v.Media === "application/pdf") {
    void renderPDF(el, v.Content, name, thumbnail, tools).catch(() =>
      el.replaceChildren(sub("无法预览此 PDF，请下载原文件查看。")),
    );
  } else
    el.append(
      icon(fileType(name).icon),
      sub(v.Notice || "此文件不支持快速预览。"),
    );
  return el;
}
async function renderPDF(
  el: HTMLElement,
  content: string,
  name: string,
  thumbnail: boolean,
  toolbar?: HTMLElement,
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
  const dialog = el.closest("dialog");
  dialog?.addEventListener("close", () => void task.destroy(), { once: true });
  const doc = await task.promise;
  let index = 1,
    zoom = 1;
  const canvas = n("canvas");
  canvas.setAttribute("aria-label", name);
  el.replaceChildren(canvas);
  el.classList.add("pdf-page");
  const tools = toolbar ?? box("pdf-tools");
  if (!toolbar && !thumbnail) el.prepend(tools);
  const draw = async () => {
    const page = await doc.getPage(index),
      base = page.getViewport({ scale: 1 }),
      fit = Math.min(
        (thumbnail ? 300 : Math.max(200, el.clientWidth)) / base.width,
        1400 / base.height,
      ),
      v = page.getViewport({ scale: fit * zoom });
    canvas.width = Math.ceil(v.width);
    canvas.height = Math.ceil(v.height);
    if (!thumbnail) tools.replaceChildren(sub("正在绘制…"));
    await page.render({ canvas, viewport: v }).promise;
    if (thumbnail) return;
    const pageNo = n("input", "page-number");
    pageNo.type = "number";
    pageNo.min = "1";
    pageNo.max = String(doc.numPages);
    pageNo.value = String(index);
    pageNo.setAttribute("aria-label", "PDF 页码");
    pageNo.onchange = () => {
      if (!pageNo.reportValidity() || !pageNo.value) return;
      index = Number(pageNo.value);
      void draw().catch((e) => el.replaceChildren(sub(errorText(e))));
    };
    const prev = btn(
        "上一页",
        () => {
          index--;
          return draw();
        },
        "bare",
        "back",
        true,
      ),
      next = btn(
        "下一页",
        () => {
          index++;
          return draw();
        },
        "bare",
        "chevron",
        true,
      ),
      less = btn(
        "缩小",
        () => {
          zoom -= 0.25;
          return draw();
        },
        "bare",
        "minus",
        true,
      ),
      more = btn(
        "放大",
        () => {
          zoom += 0.25;
          return draw();
        },
        "bare",
        "plus",
        true,
      );
    prev.disabled = index === 1;
    next.disabled = index === doc.numPages;
    less.disabled = zoom <= 0.5;
    more.disabled = zoom >= 2;
    tools.replaceChildren(
      prev,
      pageNo,
      n("span", "", `/ ${doc.numPages}`),
      next,
      less,
      btn(
        `${zoom * 100}%`,
        () => {
          zoom = 1;
          return draw();
        },
        "bare",
      ),
      more,
    );
  };
  try {
    await draw();
  } finally {
    if (thumbnail) await task.destroy();
  }
}
