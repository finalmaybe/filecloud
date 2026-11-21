const canvas = document.querySelector('.viewer-body');
enablePictureZoom(canvas);
canvas.addEventListener('error', event => {
  if (event.target.matches('.picture-target img')) canvas.replaceChildren(message('图片无法显示，可下载原文件查看。'));
}, true);
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
    if (body.contains(event.target) && body.querySelector('.viewer-zoom:checked')) return;
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
    event.preventDefault();
    step(event.key === 'ArrowLeft' ? -1 : 1);
  });

  // Only a fitted picture owns horizontal swipes. Zoomed pictures keep native panning.
  let touchStart;
  body.addEventListener('touchstart', event => {
    touchStart = event.touches.length === 1 && event.target.closest('.picture-scroll') && !body.querySelector('.viewer-zoom:checked') ? event.touches[0] : null;
  }, {passive: true});
  body.addEventListener('touchend', event => {
    if (!touchStart) return;
    const dx = event.changedTouches[0].clientX - touchStart.clientX;
    const dy = event.changedTouches[0].clientY - touchStart.clientY;
    touchStart = null;
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

function enablePictureZoom(body) {
  let point;
  body.addEventListener('click', event => {
    if (!event.target.closest('.picture-target')) return;
    const img = body.querySelector('img').getBoundingClientRect();
    point = {x: event.clientX, y: event.clientY, rx: (event.clientX - img.left) / img.width, ry: (event.clientY - img.top) / img.height};
  });
  body.addEventListener('change', event => {
    if (!event.target.matches('.viewer-zoom')) return;
    if (event.target.checked) {
      const scroll = body.querySelector('.picture-scroll');
      if (point) {
        // Keep the detail that was clicked under the pointer after switching to 1:1.
        const img = scroll.querySelector('img').getBoundingClientRect();
        scroll.scrollBy(img.left + img.width * point.rx - point.x, img.top + img.height * point.ry - point.y);
      } else {
        scroll.scrollTo((scroll.scrollWidth - scroll.clientWidth) / 2, (scroll.scrollHeight - scroll.clientHeight) / 2);
      }
    }
    point = null;
  });
}

function message(text) {
  const status = document.createElement('p');
  status.className = 'viewer-message';
  status.setAttribute('role', 'status');
  status.textContent = text;
  return status;
}
