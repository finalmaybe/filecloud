import {setLocalTime} from './local-time.js';

const directoryPath = location.pathname;
const workspace = document.querySelector('.file-workspace');
const status = document.querySelector('#directory-status');
const retry = document.querySelector('#directory-retry');
const footer = workspace.querySelector('.list-footer');
const filter = document.querySelector('#list-filter');
const deleteDialog = document.querySelector('#delete-dialog');
deleteDialog.querySelector('button[type="button"]').addEventListener('click', () => deleteDialog.close());
let nextPage;
const observer = new IntersectionObserver(entries => {
  if (entries.some(entry => entry.isIntersecting) && nextPage && !filter.value.trim()) loadDirectory(nextPage);
}, {rootMargin: '0px 0px 320px'});
filter.addEventListener('input', observeNextPage);
workspace.hidden = false;
loadDirectory(initialURL());

function initialURL() {
  const page = new URLSearchParams(location.search);
  const query = new URLSearchParams();
  for (const [key, param] of [['commit', 'CommitId'], ['path', 'Path'], ['offset', 'Offset']]) {
    if (page.has(key)) query.set(param, page.get(key));
  }
  const url = new URL(workspace.dataset.api, location.origin);
  url.search = query.toString();
  return url;
}

async function loadDirectory(url) {
  nextPage = null;
  observer.disconnect();
  retry.hidden = true;
  retry.onclick = () => loadDirectory(url);
  status.textContent = '正在读取目录项…';
  try {
    const response = await fetch(url, {redirect: 'error'});
    if (response.status === 401) { location.replace('/login'); return; }
    if (!response.ok) {
      const messages = {400: '目录或分页参数无效。', 404: '找不到该资料库、版本或目录。', 429: '目录读取繁忙，请稍后重试。'};
      status.textContent = messages[response.status] || '暂时无法读取文件列表。';
      retry.hidden = false;
      return;
    }
    const {Directory} = await response.json();
    renderDirectory(Directory);
    if (Directory.NextOffset) {
      nextPage = new URL(workspace.dataset.api, location.origin);
      nextPage.search = new URLSearchParams({CommitId: Directory.CommitId, Path: Directory.Path, Offset: Directory.NextOffset});
      observeNextPage();
    } else {
      status.textContent = Directory.Entries.length ? '已到目录末尾' : '';
    }
  } catch {
    status.textContent = '未能读取文件列表，请重试。';
    retry.hidden = false;
  }
}

function observeNextPage() {
  if (!nextPage) return;
  observer.disconnect();
  if (filter.value.trim()) {
    status.textContent = '清除筛选后可继续加载。';
  } else {
    status.textContent = '';
    observer.observe(footer);
  }
}

function formatSize(bytes) {
  let n = Number(bytes);
  for (const unit of ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB', 'ZiB']) {
    if (n < 1024) return `${Number(n.toFixed(1))} ${unit}`;
    n /= 1024;
  }
  return `${bytes} B`;
}

function renderDirectory({CommitId, Path, Parent, Entries}) {
  if (!CommitId) { workspace.hidden = true; document.querySelector('#unpublished').hidden = false; return; }
  const link = path => '?' + new URLSearchParams({commit: CommitId, path});
  if (Path !== '.') {
    const root = document.querySelector('#directory-root');
    const parent = document.querySelector('#directory-parent');
    root.href = link('.');
    parent.href = link(Parent);
    root.hidden = parent.hidden = document.querySelector('#path-separator').hidden = false;
    document.querySelector('#directory-title').textContent = Path;
  }
  if (!Entries.length) { workspace.hidden = true; document.querySelector('#empty-directory').hidden = false; return; }
  const table = workspace.querySelector('.files');
  for (const entry of Entries) {
    const path = Path === '.' ? entry.Name : `${Path}/${entry.Name}`;
    const folder = entry.Type === 'Directory';
    const row = document.querySelector(folder ? '#folder-row' : '#file-row').content.cloneNode(true);
    row.querySelector('.entry-name').textContent = entry.Name;
    const size = row.querySelector('.entry-size');
    size.textContent = formatSize(entry.Size);
    size.title = `${entry.Size} 字节`;
    const query = link(path);
    row.querySelector('.entry-link').href = folder ? query : `${directoryPath}/file-preview${query}`;
    row.querySelector('summary').setAttribute('aria-label', entry.Name + ' 的更多操作');
    row.querySelector('.move-trash').addEventListener('click', event => {
      const form = deleteDialog.querySelector('form');
      form.elements.namedItem('path').value = path;
      form.elements.namedItem('commit').value = CommitId;
      deleteDialog.querySelector('#delete-target').textContent = path;
      deleteDialog.querySelector('#delete-effect span').textContent = folder ? '整个目录将作为一条记录移入回收站。' : '文件将移入回收站。';
      const menu = event.currentTarget.closest('details');
      menu.open = false;
      menu.querySelector('summary').focus({preventScroll: true});
      deleteDialog.showModal();
    });
    if (!folder) {
      const versions = row.querySelector('.open-versions');
      versions.href = `${directoryPath}/file-history${query}`;
      versions.setAttribute('aria-label', '查看 ' + entry.Name + ' 的历史版本');
    }
    setLocalTime(row.querySelector('time'), entry.ModifiedAt);
    table.tBodies[0].append(row);
  }
  document.querySelector('#visible-count').textContent = table.tBodies[0].rows.length;
  if (filter.value.trim()) filter.dispatchEvent(new Event('input'));
  document.querySelector('#directory-count').hidden = false;
  filter.closest('label').hidden = false;
  table.hidden = false;
}
