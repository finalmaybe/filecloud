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
  filter.closest('label').hidden = entries.closest('table').hidden;
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
    const pageURL = new URL(link.href);
    const query = new URLSearchParams({CommitId: pageURL.searchParams.get('commit'), Path: pageURL.searchParams.get('path')});
    if (more) query.set('PageToken', pageURL.searchParams.get('cursor'));
    fallback.hidden = true;
    status.textContent = '正在读取版本记录…';
    content.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(`${sidebar.dataset.api}?${query}`, {signal, redirect: 'error'});
      if (!response.ok) throw new Error();
      const {FileHistory} = await response.json();
      if (signal.aborted) return;
      const page = renderVersionPage(FileHistory, pageURL);
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

function renderVersionPage(history, pageURL) {
  const page = document.querySelector('#version-page-template').content.firstElementChild.cloneNode(true);
  if (history.Current) {
    const intro = document.querySelector('#current-version-template').content.cloneNode(true);
    intro.querySelector('.version-path').textContent = history.Path;
    fillRevision(intro, history.Current);
    page.prepend(intro);
  }
  const list = page.querySelector('.version-list');
  for (const version of history.Versions) {
    const row = document.querySelector('#version-entry-template').content.firstElementChild.cloneNode(true);
    row.querySelector('strong').textContent = {added: '新增文件', content: '内容更新', 'modified-time': '修改时间变更'}[version.Change];
    setVersionTime(row.querySelector('time'), version.CreatedAt);
    fillRevision(row, version);
    if (version.Message) {
      const message = row.querySelector('.version-message');
      message.querySelector('span').textContent = version.Message;
      message.hidden = false;
    }
    list.append(row);
  }
  list.hidden = !history.Versions.length;
  page.querySelector('.no-changes').hidden = !!history.Versions.length;
  const more = page.querySelector('.version-more');
  if (history.NextPageToken) {
    pageURL.searchParams.set('cursor', history.NextPageToken);
    more.href = pageURL.href;
    page.querySelector('.history-end').hidden = true;
  } else {
    more.remove();
  }
  return page;
}

function fillRevision(root, revision) {
  root.querySelector('.file-id').textContent = revision.FileId;
  root.querySelector('.commit-id').textContent = revision.CommitId;
  setVersionTime(root.querySelector('.modified-at time'), revision.ModifiedAt);
}

function setVersionTime(time, value) {
  time.dateTime = value;
  time.textContent = value.slice(0, 19).replace('T', ' ') + ' UTC';
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
  if (!event.defaultPrevented && event.submitter) {event.submitter.disabled = true; event.submitter.textContent = '正在提交…';}
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
