const canvas = document.querySelector('.viewer-body');
enablePicture(canvas);
checkPDFSupport(canvas);

const dialog = document.querySelector('#file-viewer');
if (dialog) enableViewer(dialog, canvas);
else document.addEventListener('keydown', event => {
  if (event.key === 'Escape') document.querySelector('.viewer-exit').click();
});

function enableViewer(viewer, body) {
  const title = viewer.querySelector('#viewer-name');
  const locationLabel = viewer.querySelector('.viewer-location');
  const count = viewer.querySelector('.viewer-count');
  const download = viewer.querySelector('.viewer-download');
  const exit = viewer.querySelector('.viewer-exit');
  const previous = viewer.querySelector('.viewer-prev');
  const next = viewer.querySelector('.viewer-next');
  let links = [], index, opener, request;

  document.querySelector('.files').addEventListener('click', event => {
    const link = event.target.closest('.open-preview');
    if (!link || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    opener = link;
    links = [...document.querySelectorAll('.files tr:not([hidden]) .open-preview')];
    history.pushState({fileViewer: true}, '', link.href);
    show(links.indexOf(link));
  });

  window.addEventListener('popstate', event => {
    if (event.state?.fileViewer) {
      const restored = links.findIndex(link => link.href === location.href);
      if (restored >= 0) show(restored);
    } else if (viewer.open) {
      request.abort();
      viewer.close();
      body.replaceChildren();
      opener.focus({preventScroll: true});
    }
  });
  exit.addEventListener('click', () => history.back());
  viewer.addEventListener('cancel', event => { event.preventDefault(); history.back(); });
  viewer.addEventListener('click', event => { if (event.target === viewer) history.back(); });
  previous.addEventListener('click', () => step(-1));
  next.addEventListener('click', () => step(1));
  viewer.addEventListener('keydown', event => {
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    if (body.contains(event.target) && body.querySelector('.picture-scroll.is-zoomed')) return;
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
    event.preventDefault();
    step(event.key === 'ArrowLeft' ? -1 : 1);
  });

  // Only a fitted picture owns horizontal swipes. Zoomed pictures keep native panning.
  let touchStart;
  body.addEventListener('touchstart', event => {
    const scroll = event.target.closest('.picture-scroll:not(.is-zoomed)');
    touchStart = event.touches.length === 1 && scroll ? {scroll, point: event.touches[0]} : null;
  }, {passive: true});
  body.addEventListener('touchcancel', () => { touchStart = null; });
  body.addEventListener('touchend', event => {
    if (!touchStart) return;
    const {scroll, point} = touchStart;
    touchStart = null;
    if (scroll !== event.target.closest('.picture-scroll:not(.is-zoomed)')) return;
    const dx = event.changedTouches[0].clientX - point.clientX;
    const dy = event.changedTouches[0].clientY - point.clientY;
    if (Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy) * 1.5) {
      event.preventDefault();
      step(dx < 0 ? 1 : -1);
    }
  }, {passive: false});

  function step(delta) {
    const target = index + delta;
    if (!links[target]) return;
    history.replaceState({fileViewer: true}, '', links[target].href);
    show(target);
  }

  async function show(target) {
    index = target;
    const link = links[index];
    const url = new URL(link.href);
    const path = url.searchParams.get('path');
    title.textContent = link.querySelector('.entry-name').textContent;
    title.title = path;
    const slash = path.lastIndexOf('/');
    locationLabel.textContent = slash < 0 ? '文件预览' : '文件预览 / ' + path.slice(0, slash);
    count.textContent = `${index + 1} / ${links.length}`;
    const focus = document.activeElement;
    if (viewer.open && (body.contains(focus) || (focus === previous && index === 0) || (focus === next && index === links.length - 1))) {
      exit.focus({preventScroll: true});
    }
    previous.disabled = index === 0;
    next.disabled = index === links.length - 1;
    viewer.querySelector('.viewer-nav').hidden = links.length < 2;
    url.searchParams.set('mode', 'download');
    download.href = url.href;
    body.replaceChildren(message('正在打开文件…'));
    if (!viewer.open) viewer.showModal();
    request?.abort();
    request = new AbortController();
    const {signal} = request;
    try {
      url.searchParams.set('mode', 'stage');
      const response = await fetch(url, {signal, redirect: 'error'});
      if (!response.ok) throw new Error();
      // The same escaped Go template serves the standalone page and this fragment.
      const stage = document.createElement('template');
      stage.innerHTML = await response.text();
      if (signal.aborted) return;
      checkPDFSupport(stage.content);
      body.replaceChildren(stage.content);
      enablePicture(body);
    } catch {
      if (signal.aborted) return;
      const status = message('预览未能打开。');
      const fallback = document.createElement('a');
      fallback.href = link.href;
      fallback.textContent = '在独立页面重试';
      status.append(fallback);
      body.replaceChildren(status);
    }
  }
}

function checkPDFSupport(stage) {
  if (navigator.pdfViewerEnabled === false) stage.querySelector('.viewer-pdf')?.replaceWith(message('此浏览器不能内嵌 PDF，可下载原文件查看。'));
}

function enablePicture(body) {
  const picture = body.querySelector('.viewer-picture');
  if (!picture) return;
  const scroll = picture.querySelector('.picture-scroll');
  const img = scroll.querySelector('img');
  const zoomIn = picture.querySelector('.picture-zoom-in');
  const zoomOut = picture.querySelector('.picture-zoom-out');
  const original = picture.querySelector('.picture-original');
  const fit = picture.querySelector('.picture-fit');
  const zoomStep = 1.25;
  let scale = 1, drag, dragged = false;

  function ready() {
    img.style.setProperty('--picture-natural-width', `${img.naturalWidth}px`);
    img.style.setProperty('--picture-ratio', img.naturalWidth / img.naturalHeight);
    zoomIn.disabled = original.disabled = false;
  }
  function unavailable() {
    picture.replaceWith(message('图片无法显示，可下载原文件查看。'));
  }
  if (img.complete) {
    if (!img.naturalWidth) { unavailable(); return; }
    ready();
  } else {
    img.addEventListener('load', ready, {once: true});
    img.addEventListener('error', unavailable, {once: true});
  }

  function originalScale() {
    return Math.max(1, img.naturalWidth / picture.clientWidth, img.naturalHeight / picture.clientHeight);
  }

  function zoomTo(target, point) {
    if (zoomIn.disabled) return;
    const next = Math.min(Math.max(5, originalScale()), Math.max(1, target));
    if (next === scale) return;
    const before = img.getBoundingClientRect();
    const viewport = scroll.getBoundingClientRect();
    const x = point?.clientX ?? viewport.left + scroll.clientWidth / 2;
    const y = point?.clientY ?? viewport.top + scroll.clientHeight / 2;
    scale = next;
    scroll.classList.toggle('is-zoomed', scale > 1);
    img.style.setProperty('--picture-scale', scale);
    zoomOut.disabled = fit.disabled = scale === 1;
    if (scale === 1) {
      scroll.scrollTo(0, 0);
      return;
    }
    // Keep the same image point under the pointer (or the center for toolbar actions).
    const after = img.getBoundingClientRect();
    scroll.scrollBy(after.left + after.width * (x - before.left) / before.width - x,
      after.top + after.height * (y - before.top) / before.height - y);
  }

  zoomIn.addEventListener('click', () => zoomTo(scale * zoomStep));
  zoomOut.addEventListener('click', () => zoomTo(scale / zoomStep));
  original.addEventListener('click', () => zoomTo(originalScale()));
  fit.addEventListener('click', () => zoomTo(1));
  scroll.addEventListener('wheel', event => {
    if (zoomIn.disabled || event.ctrlKey || event.metaKey || event.shiftKey || !event.deltaY || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
    event.preventDefault();
    zoomTo(event.deltaY < 0 ? scale * zoomStep : scale / zoomStep, event);
  }, {passive: false});

  img.addEventListener('pointerdown', event => {
    if (!event.isPrimary || event.button !== 0) return;
    dragged = false;
    if (scale === 1 || event.pointerType === 'touch') return;
    drag = {id: event.pointerId, x: event.clientX, y: event.clientY, left: scroll.scrollLeft, top: scroll.scrollTop};
    img.setPointerCapture(event.pointerId);
  });
  img.addEventListener('pointermove', event => {
    if (event.pointerId !== drag?.id) return;
    const dx = event.clientX - drag.x, dy = event.clientY - drag.y;
    if (!dragged && Math.hypot(dx, dy) < 4) return;
    dragged = true;
    scroll.classList.add('is-dragging');
    scroll.scrollTo(drag.left - dx, drag.top - dy);
  });
  img.addEventListener('lostpointercapture', event => {
    if (event.pointerId !== drag?.id) return;
    drag = null;
    scroll.classList.remove('is-dragging');
  });
  img.addEventListener('click', event => {
    if (dragged || zoomIn.disabled) return;
    const native = originalScale();
    zoomTo(scale > 1 ? 1 : native > 1 ? native : zoomStep, event);
  });
}

function message(text) {
  const status = document.createElement('p');
  status.className = 'viewer-message';
  status.setAttribute('role', 'status');
  status.textContent = text;
  return status;
}
