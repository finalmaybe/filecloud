const table = document.querySelector('.libraries');
const info = document.querySelector('#info-dialog');
const dialog = document.querySelector('#library-dialog');
const opener = document.querySelector('#create-library');
const form = dialog.querySelector('form');
const submit = form.querySelector('[type="submit"]');
const error = document.querySelector('#create-error');
const createID = crypto.randomUUID();

loadLibraries();
opener.disabled = false;
if (!('commandForElement' in opener)) {
  opener.addEventListener('click', () => dialog.showModal());
  document.querySelector('#cancel-create').addEventListener('click', () => dialog.close());
}
dialog.addEventListener('close', () => opener.focus());
info.querySelector('.info-heading button').addEventListener('click', () => info.close());

form.addEventListener('submit', async event => {
  event.preventDefault();
  if (submit.disabled) return;
  submit.disabled = true;
  submit.textContent = '正在创建…';
  error.hidden = true;
  try {
    const {Library} = await libraryRequest(`/v1/libraries/${createID}`, {
      method: 'PUT', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({Name: form.elements.name.value}),
    });
    location.assign(`/web/libraries/${Library.LibraryId}`);
  } catch (err) {
    error.textContent = err.message;
    error.hidden = false;
    submit.disabled = false;
    submit.textContent = '确定创建';
  }
});

async function loadLibraries() {
  const status = document.querySelector('#list-status');
  status.hidden = false;
  const params = new URLSearchParams({PageSize: '50'});
  const cursor = new URLSearchParams(location.search).get('cursor');
  if (cursor) params.set('PageToken', cursor);
  try {
    const {Libraries, NextPageToken} = await libraryRequest(`/v1/libraries?${params}`);
    for (const library of Libraries) appendLibrary(library);
    const empty = Libraries.length === 0;
    table.hidden = empty;
    document.querySelector('#empty-libraries').hidden = !empty;
    document.querySelector('.list-footer').hidden = empty;
    document.querySelector('#list-filter').closest('label').hidden = empty;
    document.querySelector('#visible-count').textContent = Libraries.length;
    if (NextPageToken) {
      const next = document.querySelector('#next-page');
      next.href = '?' + new URLSearchParams({cursor: NextPageToken});
      next.hidden = false;
    }
    status.hidden = true;
  } catch (err) {
    status.textContent = err.message;
    document.querySelector('#list-retry').hidden = false;
  }
}

function appendLibrary(library) {
  const row = document.querySelector('#library-row').content.firstElementChild.cloneNode(true);
  const path = `/web/libraries/${library.LibraryId}`;
  row.querySelector('.entry-link').href = path;
  row.querySelector('.entry-name').textContent = library.Name;
  row.querySelector('time').textContent = library.CreatedAt.slice(0, 10);
  setLibraryState(row, library);
  const toggle = row.querySelector('summary');
  toggle.setAttribute('aria-label', library.Name + ' 的更多操作');
  row.querySelector('button').addEventListener('click', () => {
    info.querySelector('#info-name').textContent = library.Name;
    info.querySelector('#info-created').textContent = library.CreatedAt.slice(0, 19).replace('T', ' ') + ' UTC';
    info.querySelector('#library-id').textContent = library.LibraryId;
    info.querySelector('.bind-library-id').textContent = library.LibraryId;
    info.querySelector('#info-files').href = path;
    const head = info.querySelector('#head-id');
    head.textContent = library.HeadCommitId || '';
    head.closest('dd').hidden = info.querySelector('#head-label').hidden = !library.HeadCommitId;
    setLibraryState(info, library);
    row.querySelector('details').open = false;
    toggle.focus({preventScroll: true});
    info.showModal();
  });
  table.tBodies[0].append(row);
}

function setLibraryState(container, library) {
  const state = container.querySelector('.state');
  state.classList.toggle('published', !!library.HeadCommitId);
  state.textContent = library.HeadCommitId ? '已发布' : '尚未发布';
}

async function libraryRequest(path, options) {
  let response;
  try {
    response = await fetch(path, {redirect: 'error', ...options});
  } catch {
    throw new Error('未能连接服务，请重试。');
  }
  if (response.status === 401) location.replace('/web/login');
  if (!response.headers.get('content-type')?.includes('application/json')) {
    throw new Error('未能读取资料库服务响应，请稍后重试。');
  }
  const data = await response.json();
  if (response.ok) return data;
  if (data.RetCode === 3000) throw new Error('已有同名资料库，请换一个名称。');
  if (data.RetCode === 3001) throw new Error('此创建请求已用于其他名称，请重新打开列表。');
  if (response.status === 400) throw new Error(options ? '请输入有效的资料库名称（1–128 字）。' : '分页链接已失效，请重新打开第一页。');
  throw new Error('服务暂不可用，请稍后重试。');
}
