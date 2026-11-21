const workspace = document.querySelector('.file-workspace');
const status = document.querySelector('#directory-status');
loadDirectory();

async function loadDirectory() {
  const page = new URLSearchParams(location.search);
  const query = new URLSearchParams();
  for (const [key, param] of [['commit', 'CommitId'], ['path', 'Path'], ['offset', 'Offset']]) {
    if (page.has(key)) query.set(param, page.get(key));
  }
  const url = new URL(workspace.dataset.api, location.origin);
  url.search = query.toString();
  status.hidden = false;
  try {
    const response = await fetch(url, {redirect: 'error'});
    if (response.status === 401) { location.replace('/web/login'); return; }
    if (!response.ok) {
      const messages = {400: '目录或分页参数无效。', 404: '找不到该资料库、版本或目录。', 429: '目录读取繁忙，请稍后重试。'};
      status.textContent = messages[response.status] || '暂时无法读取文件列表。';
      document.querySelector('#directory-retry').hidden = false;
      return;
    }
    const {Directory} = await response.json();
    renderDirectory(Directory);
    status.hidden = true;
  } catch {
    status.textContent = '未能读取文件列表，请重试。';
    document.querySelector('#directory-retry').hidden = false;
  }
}

function renderDirectory({CommitId, Path, Parent, Entries, NextOffset}) {
  if (!CommitId) { document.querySelector('#unpublished').hidden = false; return; }
  const link = (path, offset) => {
    const params = new URLSearchParams({commit: CommitId, path});
    if (offset) params.set('offset', offset);
    return '?' + params;
  };
  if (Path !== '.') {
    const root = document.querySelector('#directory-root');
    const parent = document.querySelector('#directory-parent');
    root.href = link('.');
    parent.href = link(Parent);
    root.hidden = parent.hidden = document.querySelector('#path-separator').hidden = false;
    document.querySelector('#directory-title').textContent = Path;
  }
  if (!Entries.length) { document.querySelector('#empty-directory').hidden = false; return; }
  const table = workspace.querySelector('.files');
  for (const entry of Entries) {
    const path = Path === '.' ? entry.Name : `${Path}/${entry.Name}`;
    const folder = entry.Type === 'Directory';
    const row = document.querySelector(folder ? '#folder-row' : '#file-row').content.cloneNode(true);
    row.querySelector('.entry-name').textContent = entry.Name;
    const href = folder ? link(path) : `${location.pathname}/file-history${link(path)}`;
    for (const anchor of row.querySelectorAll('a')) anchor.href = href;
    if (!folder) {
      row.querySelector('summary').setAttribute('aria-label', entry.Name + ' 的更多操作');
      row.querySelector('.action-list a').setAttribute('aria-label', '查看 ' + entry.Name + ' 的历史版本');
    }
    const time = row.querySelector('time');
    time.dateTime = entry.ModifiedAt;
    time.textContent = entry.ModifiedAt.slice(0, 19).replace('T', ' ');
    table.tBodies[0].append(row);
  }
  if (NextOffset) {
    const next = document.querySelector('#directory-next');
    next.href = link(Path, NextOffset);
    next.hidden = false;
  }
  document.querySelector('#visible-count').textContent = Entries.length;
  document.querySelector('#list-filter').closest('label').hidden = false;
  table.hidden = workspace.hidden = false;
}
