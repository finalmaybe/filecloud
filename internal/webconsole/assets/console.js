const navToggle = document.querySelector('#nav-toggle');
if (navToggle) {
  navToggle.hidden = false;
  navToggle.addEventListener('click', () => {
    navToggle.setAttribute('aria-expanded', String(navToggle.getAttribute('aria-expanded') !== 'true'));
  });
  document.querySelector('#sidebar').addEventListener('keydown', event => {
    if (event.key === 'Escape' && navToggle.offsetWidth) {
      navToggle.setAttribute('aria-expanded', 'false');
      navToggle.focus();
    }
  });
}

const entries = document.querySelector('.entry-table tbody');
if (entries) {
  const filter = document.querySelector('#list-filter');
  filter.closest('label').hidden = false;
  filter.addEventListener('input', () => {
    const query = filter.value.trim().toLowerCase();
    let visible = 0;
    for (const row of entries.rows) {
      row.hidden = !row.querySelector('.entry-name').textContent.toLowerCase().includes(query);
      if (!row.hidden) visible++;
    }
    document.querySelector('#visible-count').textContent = visible;
    document.querySelector('#filter-empty').hidden = visible !== 0;
  });

  const dismissOutside = event => {
    const open = entries.querySelector('.row-actions[open]');
    if (open && !open.contains(event.target)) open.open = false;
  };
  document.addEventListener('click', dismissOutside);
  document.addEventListener('focusin', dismissOutside);
  entries.addEventListener('keydown', event => {
    const open = event.target.closest('.row-actions[open]');
    if (open && event.key === 'Escape') {
      open.open = false;
      open.querySelector('summary').focus();
    }
  });
}

const versionSidebar = document.querySelector('#version-sidebar');
if (versionSidebar) enableVersionSidebar(versionSidebar);

function enableVersionSidebar(sidebar) {
  const workspace = sidebar.closest('.file-workspace');
  const close = sidebar.querySelector('#close-versions');
  const content = sidebar.querySelector('#version-content');
  const status = sidebar.querySelector('#version-status');
  const fallback = sidebar.querySelector('#version-fallback');
  let selectedRow, opener, request;

  workspace.addEventListener('click', async event => {
    const link = event.target.closest('.open-versions, .version-more');
    if (!link || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    const more = link.classList.contains('version-more');
    if (!more) {
      selectedRow?.classList.remove('viewing-versions');
      selectedRow = link.closest('tr');
      selectedRow.classList.add('viewing-versions');
      sidebar.querySelector('#version-filename').textContent = selectedRow.querySelector('.entry-name').textContent;
      const menu = link.closest('.row-actions');
      opener = menu ? menu.querySelector('summary') : link;
      if (menu) menu.open = false;
      content.replaceChildren();
      sidebar.hidden = false;
      sidebar.scrollTop = 0;
      close.focus();
    }

    request?.abort();
    request = new AbortController();
    const {signal} = request;
    const url = new URL(link.href);
    url.searchParams.set('panel', '1');
    fallback.hidden = true;
    status.textContent = '正在读取版本记录…';
    content.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(url, {signal, redirect: 'error'});
      if (!response.ok) throw new Error();
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      const page = doc.querySelector('.version-page');
      if (!page) throw new Error();
      if (signal.aborted) return;
      const focusPage = more && document.activeElement === link;
      if (more) link.closest('.version-continuation').remove();
      content.append(page);
      if (focusPage) page.focus();
      status.textContent = '';
    } catch {
      if (signal.aborted) return;
      status.textContent = '未能读取这批记录。可打开该文件的历史版本重试。';
      fallback.href = link.href;
      fallback.hidden = false;
    } finally {
      if (!signal.aborted) content.removeAttribute('aria-busy');
    }
  });

  close.addEventListener('click', () => {
    request?.abort();
    sidebar.hidden = true;
    selectedRow.classList.remove('viewing-versions');
    (selectedRow.hidden ? document.querySelector('#list-filter') : opener).focus();
  });
  sidebar.addEventListener('keydown', event => { if (event.key === 'Escape') close.click(); });
}

const libraryTable = document.querySelector('.libraries');
if (libraryTable) {
  const infoDialog = document.querySelector('#info-dialog');
  const content = document.querySelector('#info-content');
  libraryTable.addEventListener('click', event => {
    const trigger = event.target.closest('button[aria-controls="info-dialog"]');
    if (!trigger) return;
    const menu = trigger.closest('.row-actions');
    content.replaceChildren(menu.querySelector('template').content.cloneNode(true),
      document.querySelector('#binding-template').content.cloneNode(true));
    content.querySelector('.bind-library-id').textContent = content.querySelector('#library-id').textContent;
    menu.open = false;
    // The menu item is now hidden; let the dialog restore focus to the visible row control.
    menu.querySelector('summary').focus({preventScroll: true});
    infoDialog.showModal();
  });
  infoDialog.querySelector('.info-heading button').addEventListener('click', () => infoDialog.close());
}

const libraryDialog = document.querySelector('#library-dialog');
if (libraryDialog) {
  const opener = document.querySelector('#create-library');
  if (!('commandForElement' in opener)) {
    opener.addEventListener('click', () => libraryDialog.showModal());
    document.querySelector('#cancel-create').addEventListener('click', () => libraryDialog.close());
  }
  if (libraryDialog.open) {
    // A rejected POST renders the form open; promote it to a modal with native focus handling.
    libraryDialog.removeAttribute('open');
    libraryDialog.showModal();
  }
  libraryDialog.addEventListener('close', () => opener.focus());
}

const cancelReset = document.querySelector('#cancel-reset');
if (cancelReset) document.addEventListener('keydown', event => {if (event.key === 'Escape') cancelReset.click();});
const probe = document.querySelector('#probe');
probe?.addEventListener('click', async () => {
  probe.disabled = true;
  await Promise.all([['live','/healthz','存活'], ['ready','/readyz','已就绪']].map(async ([id, url, ok]) => {
    let state = '未知';
    try {
      const res = await fetch(url, {cache:'no-store', credentials:'omit', redirect:'error', signal:AbortSignal.timeout(5000)});
      const data = await res.json();
      if (res.ok && data.RetCode === 0) state = ok;
      else if (id === 'ready' && res.status === 503 && data.RetCode === 5001) state = '未就绪';
    } catch { /* No probe response: the storage state is unknown. */ }
    document.getElementById(id).textContent = state;
  }));
  document.getElementById('checked').textContent = new Date().toISOString();
  probe.disabled = false;
});

document.addEventListener('submit', event => {
  if (event.submitter) {event.submitter.disabled = true; event.submitter.textContent = '正在提交…';}
});
window.addEventListener('pageshow', event => {if (event.persisted) location.reload();});
document.addEventListener('click', async (event) => {
  const button = event.target.closest('button[data-copy]');
  if (!button) return;
  const text = document.getElementById(button.dataset.copy);
  try {
    await navigator.clipboard.writeText(text.textContent);
    button.textContent = '已复制';
    document.querySelector('#feedback').textContent = '已复制到剪贴板';
  } catch {
    const range = document.createRange();
    range.selectNodeContents(text);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    button.textContent = '已选中，请手动复制';
    document.querySelector('#feedback').textContent = button.textContent;
  }
});
