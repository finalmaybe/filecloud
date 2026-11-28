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
  let index = start,
    request = 0,
    stopImage: (() => void) | undefined;
  const prev = btn(
      "上一张",
      () => {
        void show(index - 1);
      },
      "bare viewer-prev",
      "chevron",
      true,
    ),
    after = btn(
      "下一张",
      () => {
        void show(index + 1);
      },
      "bare",
      "chevron",
      true,
    );
  let dismissPress: {
    id: number;
    x: number;
    y: number;
    surface: Element;
  } | null = null;
  const dismissSurface = ({ clientX, clientY }: MouseEvent) => {
    const { left, right, top, bottom } = d.getBoundingClientRect();
    if (
      clientX < left ||
      clientX >= right ||
      clientY < top ||
      clientY >= bottom
    )
      return d;
    const hit = document.elementFromPoint(clientX, clientY);
    return hit === stage || hit?.classList.contains("viewer-image")
      ? hit
      : null;
  };
  const moved = (e: PointerEvent, press: { x: number; y: number }) =>
    Math.hypot(e.clientX - press.x, e.clientY - press.y) > 5;
  d.onpointerdown = (e) => {
    if (!e.isPrimary) return;
    const surface = e.button === 0 ? dismissSurface(e) : null;
    dismissPress = surface
      ? { id: e.pointerId, x: e.clientX, y: e.clientY, surface }
      : null;
  };
  d.onpointermove = (e) => {
    if (dismissPress?.id === e.pointerId && moved(e, dismissPress))
      dismissPress = null;
  };
  d.onpointerup = (e) => {
    if (
      dismissPress?.id === e.pointerId &&
      (moved(e, dismissPress) || dismissSurface(e) !== dismissPress.surface)
    )
      dismissPress = null;
  };
  d.onpointercancel = (e) => {
    if (dismissPress?.id === e.pointerId) dismissPress = null;
  };
  d.onclick = (e) => {
    const surface = dismissPress?.surface;
    dismissPress = null;
    if (e.detail && surface && dismissSurface(e) === surface) d.close();
  };
  const show = async (next: number) => {
    const current = ++request;
    stopImage?.();
    stopImage = undefined;
    dismissPress = null;
    const focused = document.activeElement;
    const directionButton = next < index ? prev : after;
    const navFocus =
      focused === prev ? prev : focused === after ? after : directionButton;
    const restoreFocus = tools.contains(focused);
    index = next;
    const item = items[index];
    title.textContent = item.name;
    d.setAttribute("aria-label", item.name + " · 快速预览");
    stage.replaceChildren(sub("正在读取…"));
    footer.textContent = "";
    tools.replaceChildren();
    if (items.length > 1) {
      prev.disabled = index === 0;
      after.disabled = index === items.length - 1;
      tools.append(prev, after);
      if (restoreFocus) {
        const available = navFocus.disabled
          ? navFocus === prev
            ? after
            : prev
          : navFocus;
        available.focus({ preventScroll: true });
      }
    }
    try {
      const v = await item.load();
      if (!d.open || request !== current) return;
      const size = item.size ?? (v.ModifiedAt ? String(v.Size) : "");
      footer.textContent = [
        previewLabels[v.Media],
        v.Notice,
        size ? bytes(size) : "",
      ]
        .filter(Boolean)
        .join(" · ");
      const body = previewBody(v, item.name, false, tools);
      stage.replaceChildren(body);
      const img = body.querySelector("img");
      if (img) {
        stopImage = zoomPreviewImage(body, img, tools, () => {
          footer.textContent = `${img.naturalWidth} × ${img.naturalHeight}${size ? " · " + bytes(size) : ""}`;
        });
      }
    } catch (e) {
      if (d.open && request === current)
        stage.replaceChildren(sub(errorText(e)));
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
        stopImage?.();
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

function zoomPreviewImage(
  frame: HTMLElement,
  img: HTMLImageElement,
  tools: HTMLElement,
  onLoad: () => void,
) {
  const minZoom = 0.25,
    maxZoom = 4;
  let zoom = 1,
    width = 0,
    height = 0,
    x = 0,
    y = 0;
  let drag: { id: number; x: number; y: number } | null = null;
  const less = btn(
      "缩小图片",
      () => setZoom(zoom - 0.25),
      "bare",
      "minusLine",
      true,
    ),
    reset = btn("重置图片缩放", () => setZoom(1), "bare"),
    more = btn("放大图片", () => setZoom(zoom + 0.25), "bare", "plus", true);
  const draw = () => {
    const maxX =
        zoom <= 1 ? 0 : Math.max(0, (width * zoom - frame.clientWidth) / 2),
      maxY =
        zoom <= 1 ? 0 : Math.max(0, (height * zoom - frame.clientHeight) / 2);
    x = Math.min(maxX, Math.max(-maxX, x));
    y = Math.min(maxY, Math.max(-maxY, y));
    img.style.transform = `translate(-50%, -50%) translate(${x}px, ${y}px) scale(${zoom})`;
    frame.classList.toggle("pannable", maxX > 0 || maxY > 0);
  };
  const updateTools = () => {
    reset.disabled = !width;
    if (
      (zoom === minZoom && document.activeElement === less) ||
      (zoom === maxZoom && document.activeElement === more)
    )
      reset.focus({ preventScroll: true });
    less.disabled = !width || zoom === minZoom;
    more.disabled = !width || zoom === maxZoom;
    const percent = `${Math.round(zoom * 100)}%`;
    reset.textContent = percent;
    reset.setAttribute("aria-label", `重置图片缩放（当前 ${percent}）`);
  };
  const setZoom = (next: number, anchorX = 0, anchorY = 0) => {
    const scale = Math.min(maxZoom, Math.max(minZoom, next));
    if (scale === zoom) return;
    const ratio = scale / zoom;
    x = anchorX + (x - anchorX) * ratio;
    y = anchorY + (y - anchorY) * ratio;
    zoom = scale;
    draw();
    updateTools();
  };
  const fit = () => {
    if (!img.naturalWidth) return;
    const ratio = Math.min(
      1,
      frame.clientWidth / img.naturalWidth,
      frame.clientHeight / img.naturalHeight,
    );
    width = img.naturalWidth * ratio;
    height = img.naturalHeight * ratio;
    img.style.width = `${width}px`;
    img.style.height = `${height}px`;
    draw();
  };
  frame.classList.add("viewer-image");
  img.draggable = false;
  tools.append(less, reset, more);
  updateTools();
  const loaded = () => {
    fit();
    updateTools();
    onLoad();
  };
  img.onload = loaded;
  if (img.complete && img.naturalWidth) loaded();
  const resize = new ResizeObserver(fit);
  resize.observe(frame);

  frame.addEventListener(
    "wheel",
    (e) => {
      if (!width || !e.deltaY) return;
      e.preventDefault();
      const {
          left,
          top,
          width: frameWidth,
          height: frameHeight,
        } = frame.getBoundingClientRect(),
        unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? frameHeight : 1;
      setZoom(
        zoom * Math.exp((-e.deltaY * unit) / 450),
        e.clientX - left - frameWidth / 2,
        e.clientY - top - frameHeight / 2,
      );
    },
    { passive: false },
  );
  img.onpointerdown = (e) => {
    if (!e.isPrimary || e.button !== 0 || !frame.classList.contains("pannable"))
      return;
    img.setPointerCapture(e.pointerId);
    drag = { id: e.pointerId, x: e.clientX, y: e.clientY };
    frame.classList.add("panning");
  };
  img.onpointermove = (e) => {
    if (drag?.id !== e.pointerId) return;
    x += e.clientX - drag.x;
    y += e.clientY - drag.y;
    drag.x = e.clientX;
    drag.y = e.clientY;
    draw();
  };
  img.onlostpointercapture = () => {
    drag = null;
    frame.classList.remove("panning");
  };
  return () => {
    resize.disconnect();
    img.onload = null;
  };
}

const wordMedia =
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
const previewLabels: Record<string, string> = {
  [wordMedia]: "Word 正文",
  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "Excel 工作簿",
  "text/plain": "文本预览",
};

export function previewBody(
  v: Preview,
  name: string,
  thumbnail = false,
  tools?: HTMLElement,
) {
  const el = box("preview");
  if (v.Sheets?.length) {
    if (thumbnail && v.Notice) el.append(sub(v.Notice));
    el.append(...v.Sheets.map(sheetPreview));
  } else if (v.Media === wordMedia) {
    el.tabIndex = 0;
    el.setAttribute("role", "region");
    el.setAttribute("aria-label", "Word 正文");
    if (thumbnail && v.Notice) el.append(sub(v.Notice));
    el.append(box("office-word", v.Content));
  } else if (v.Media === "text/plain") {
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

function sheetPreview(
  sheet: NonNullable<Preview["Sheets"]>[number],
  index: number,
) {
  const section = n("details", "office-sheet", n("summary", "", sheet.Name));
  section.open = index === 0;
  if (!sheet.Columns?.length) {
    section.append(sub("此工作表的预览区域为空。"));
    return section;
  }
  const table = n("table");
  table.setAttribute("aria-label", sheet.Name);
  const head = table.createTHead().insertRow();
  for (const label of ["行", ...sheet.Columns]) {
    const th = n("th", "", label);
    th.scope = "col";
    head.append(th);
  }
  const rows = table.createTBody();
  for (const [i, cells] of (sheet.Rows ?? []).entries()) {
    const row = rows.insertRow(),
      th = n("th", "", String(i + 1));
    th.scope = "row";
    row.append(th);
    for (const value of cells ?? []) row.insertCell().textContent = value;
  }
  const grid = box("office-grid", table);
  grid.tabIndex = 0;
  grid.setAttribute("role", "region");
  grid.setAttribute("aria-label", sheet.Name + " 表格区域");
  section.append(grid);
  return section;
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
