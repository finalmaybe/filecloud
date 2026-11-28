import {fileIcon} from './file-icon.js';
import {setLocalTime} from './local-time.js';

for (const time of document.querySelectorAll('time[datetime]')) setLocalTime(time);
for (const file of document.querySelectorAll('.trash-list [data-file-icon]')) {
  const name = file.querySelector('.entry-name').textContent;
  file.querySelector('use').setAttribute('href', `#i-${fileIcon(name)}`);
}

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

const retentionForm = document.querySelector('#retention-form');
if (retentionForm) {
  const permanent = retentionForm.elements.namedItem('permanent');
  const historyDays = retentionForm.elements.namedItem('history');
  const syncHistoryDays = () => { historyDays.disabled = permanent.checked; };
  syncHistoryDays();
  permanent.addEventListener('change', syncHistoryDays);
}

const entries = document.querySelector('.entry-table tbody');
if (entries) {
  const filter = document.querySelector('#list-filter');
  filter.closest('label').hidden = entries.closest('table').hidden;
  const updateFilter = () => {
    const query = filter.value.trim().toLowerCase();
    let visible = 0;
    for (const row of entries.rows) {
      const searchable = row.dataset.path ?? row.querySelector('.entry-name').textContent;
      row.hidden = !searchable.toLowerCase().includes(query);
      if (!row.hidden) visible++;
    }
    document.querySelector('#visible-count').textContent = visible;
    document.querySelector('#filter-empty').hidden = visible !== 0;
  };
  filter.addEventListener('input', updateFilter);
  if (entries.closest('.trash-list')) {
    updateFilter();
    enableTrashDeletion(entries, filter);
  }
}

function enableTrashDeletion(entries, filter) {
  const batch = document.getElementById('trash-delete-selected');
  const dialog = document.getElementById('trash-delete-dialog');
  const form = dialog.querySelector('form');
  const target = document.getElementById('trash-delete-target');
  const ids = document.getElementById('trash-delete-ids');
  const checks = [...entries.querySelectorAll('.trash-select')];
  let opener;

  const selected = () => checks.filter(check => check.checked && !check.closest('tr').hidden);
  const updateSelection = () => {
    for (const check of checks) {
      if (check.closest('tr').hidden) check.checked = false;
    }
    const count = selected().length;
    batch.disabled = count === 0;
    batch.textContent = `永久删除所选（${count}）`;
  };
  const confirmDelete = (items, label, trigger) => {
    target.textContent = label;
    ids.replaceChildren(...items.map(check => {
      const input = document.createElement('input');
      input.type = 'hidden';
      input.name = 'id';
      input.value = check.value;
      return input;
    }));
    form.elements.namedItem('filter').value = filter.value;
    opener = trigger;
    dialog.showModal();
  };
  for (const check of checks) check.hidden = false;
  for (const button of entries.querySelectorAll('.trash-delete-one')) {
    button.hidden = false;
    button.addEventListener('click', () => {
      const row = button.closest('tr');
      if (row.hidden) return;
      const menu = button.closest('.row-actions');
      menu.open = false;
      const summary = menu.querySelector('summary');
      summary.focus({preventScroll: true});
      confirmDelete([row.querySelector('.trash-select')], row.dataset.path, summary);
    });
  }
  batch.hidden = false;
  batch.addEventListener('click', () => {
    const items = selected();
    if (items.length) confirmDelete(items, `已选择 ${items.length} 个条目`, batch);
  });
  entries.addEventListener('change', updateSelection);
  filter.addEventListener('input', updateSelection);
  updateSelection();
  dialog.querySelector('button[type="button"]').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => opener?.focus({preventScroll: true}));
}

const openMenu = '.account-menu[open], .row-actions[open]';
const dismissOutside = event => {
  const open = document.querySelector(openMenu);
  if (open && !open.contains(event.target)) open.open = false;
};
document.addEventListener('click', dismissOutside);
document.addEventListener('focusin', dismissOutside);
document.addEventListener('keydown', event => {
  if (event.key !== 'Escape') return;
  const open = document.querySelector(openMenu);
  if (open) {
    event.preventDefault();
    open.open = false;
    open.querySelector('summary').focus();
  }
});

const issuedCredential = document.getElementById('issued-credential');
if (issuedCredential) {
  // The server renders it open so the one-time password is also visible without JavaScript.
  issuedCredential.removeAttribute('open');
  const done = issuedCredential.querySelector('form');
  done.method = 'dialog';
  issuedCredential.addEventListener('close', () => location.replace(done.action));
  issuedCredential.showModal();
}

const listDialogs = document.querySelector('.trash-list, [data-list-dialogs]');
if (listDialogs) {
  listDialogs.addEventListener('click', event => {
    const button = event.target.closest('button[commandfor]');
    if (!button) return;
    event.preventDefault();
    const dialog = document.getElementById(button.getAttribute('commandfor'));
    if (button.getAttribute('command') === 'close') {
      dialog.close();
      return;
    }
    const menu = button.closest('.row-actions');
    if (menu) {
      menu.open = false;
      menu.querySelector('summary').focus({preventScroll: true});
    }
    dialog.querySelector('form')?.reset();
    dialog.showModal();
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
  sidebar.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !sidebar.querySelector(':popover-open')) close.click();
  });
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
    setLocalTime(row.querySelector('time'), version.CreatedAt);
    fillRevision(row, version);
    const base = pageURL.pathname;
    const params = new URLSearchParams({commit: version.CommitId, path: history.Path});
    row.querySelector('.version-download').href = `${base}/download?${params}`;
    const actions = row.querySelector('.version-actions');
    actions.id = `version-menu-${version.CommitId}`;
    const toggle = row.querySelector('.version-menu-toggle');
    toggle.setAttribute('popovertarget', actions.id);
    const label = row.querySelector('.version-label');
    label.id = `version-label-${version.CommitId}`;
    toggle.setAttribute('aria-describedby', label.id);
    if (version.IsCurrent) {
      for (const choice of actions.querySelectorAll('details')) choice.remove();
    } else {
      actions.elements.namedItem('path').value = history.Path;
      actions.elements.namedItem('commit').value = version.CommitId;
      actions.elements.namedItem('anchor').value = history.AnchorCommitId;
      actions.querySelector('.version-restore').formAction = `${base}/restore`;
    }
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
  setLocalTime(root.querySelector('.modified-at time'), revision.ModifiedAt);
}

const versionConfirm = document.querySelector('#version-confirm');
if (versionConfirm) enableVersionConfirmation(versionConfirm);

function enableVersionConfirmation(dialog) {
  const confirm = dialog.querySelector('[type="submit"]');
  const cancel = dialog.querySelector('[type="button"]');
  const error = dialog.querySelector('[role="alert"]');
  const notice = document.querySelector('#version-notice');
  let noticeTimer;
  cancel.addEventListener('click', () => dialog.close());
  dialog.addEventListener('cancel', event => { if (cancel.disabled) event.preventDefault(); });
  document.addEventListener('click', event => {
    const menu = event.target.closest('.version-actions');
    if (!menu) return;
    if (event.target.closest('.version-download')) {
      menu.hidePopover();
      return;
    }
    const choice = event.target.closest('summary');
    if (!choice) return;
    event.preventDefault();
    const details = choice.parentElement;
    const source = details.querySelector('button');
    const row = menu.closest('.version-row');
    dialog.querySelector('h2').textContent = `${choice.textContent}？`;
    dialog.querySelector('strong').textContent = menu.elements.namedItem('path').value;
    setLocalTime(dialog.querySelector('time'), row.querySelector('time').dateTime);
    dialog.querySelector('code').textContent = menu.elements.namedItem('commit').value.slice(0, 12);
    dialog.querySelector('#version-confirm-effect').textContent = details.querySelector('p').textContent;
    confirm.textContent = source.textContent;
    const deleting = details.classList.contains('destructive');
    error.textContent = '';
    confirm.classList.toggle('destructive', deleting);
    confirm.type = deleting ? 'button' : 'submit';
    confirm.onclick = deleting ? () => deleteVersion(menu) : null;
    confirm.setAttribute('form', menu.id);
    if (!deleting) confirm.formAction = source.formAction;
    menu.hidePopover();
    row.querySelector('.version-menu-toggle').focus({preventScroll: true});
    dialog.showModal();
  });

  async function deleteVersion(menu) {
    const data = new FormData(menu);
    const query = new URLSearchParams({Path: data.get('path'), AnchorCommitId: data.get('anchor')});
    const url = `${menu.closest('[data-api]').dataset.api}/${data.get('commit')}?${query}`;
    cancel.disabled = confirm.disabled = true;
    confirm.textContent = '正在删除…';
    error.textContent = '';
    try {
      const response = await fetch(url, {method: 'DELETE', redirect: 'error'});
      if (response.status !== 204) {
        const messages = {401: '登录已过期，请重新登录。', 404: '找不到该版本，请重新打开版本列表。',
          409: '当前版本不能删除。', 412: '资料库已更新，请重新打开文件列表后再操作。', 429: '服务繁忙，请稍后重试。'};
        error.textContent = messages[response.status] || '暂时无法删除版本，请稍后重试。';
        return;
      }
    } catch {
      error.textContent = '未能确认删除结果，请重新打开版本列表核对。';
      return;
    } finally {
      cancel.disabled = confirm.disabled = false;
      confirm.textContent = '确认删除';
    }
    finishVersionDeletion(dialog, menu.closest('.version-row'));
    notice.textContent = '版本已删除';
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => { notice.textContent = ''; }, 4000);
  }
}

function finishVersionDeletion(dialog, row) {
  const list = row.parentElement;
  const page = list.parentElement;
  const neighbor = row.nextElementSibling || row.previousElementSibling;
  const focus = neighbor?.querySelector('.version-menu-toggle') || page.querySelector('.version-more') || page;
  const sidebar = row.closest('#version-sidebar');
  const top = sidebar?.scrollTop;
  const {scrollX, scrollY} = window;
  dialog.close();
  row.remove();
  if (!list.childElementCount) {
    list.hidden = true;
    const empty = page.querySelector('.no-changes');
    if (empty) empty.hidden = false;
  }
  focus.focus({preventScroll: true});
  if (sidebar) sidebar.scrollTop = top;
  window.scrollTo(scrollX, scrollY);
}

for (const dialog of document.querySelectorAll('.reset-account')) {
  const form = dialog.querySelector('form');
  const submit = form.querySelector('[type="submit"]');
  const cancel = form.querySelector('[command="close"]');
  const error = form.querySelector('[role="alert"]');
  dialog.addEventListener('cancel', event => { if (submit.disabled) event.preventDefault(); });
  dialog.addEventListener('close', () => { form.reset(); error.hidden = true; });
  form.addEventListener('submit', async event => {
    event.preventDefault();
    submit.disabled = cancel.disabled = true;
    submit.textContent = '正在重置…';
    error.hidden = true;
    try {
      const response = await fetch(form.action, {method: 'POST', headers: {Accept: 'text/plain'},
        body: new URLSearchParams(new FormData(form)), redirect: 'error'});
      if (response.status !== 204) {
        error.textContent = response.status === 403 ? '需要管理员权限，请重新登录。' :
          response.headers.get('Content-Type')?.startsWith('text/plain') ? await response.text() : '暂时无法重置密码，请稍后重试。';
        error.hidden = false;
        return;
      }
      const notice = document.querySelector('#account-notice');
      notice.hidden = false;
      notice.textContent = `已重置「${form.elements.namedItem('username').value}」的密码，原有会话和应用密码已失效。`;
      dialog.close();
    } catch {
      error.textContent = '未能确认重置结果，请检查网络连接。';
      error.hidden = false;
    } finally {
      submit.disabled = cancel.disabled = false;
      submit.textContent = '确认';
    }
  });
}

const probe = document.querySelector('#probe');
probe?.form.addEventListener('submit', async event => {
  event.preventDefault();
  probe.disabled = true;
  probe.textContent = '检查中…';
  await Promise.all([['live','/healthz','存活'], ['ready','/readyz','已就绪']].map(async ([id, url, ok]) => {
    let state = '未知';
    try {
      const res = await fetch(url, {cache:'no-store', credentials:'omit', redirect:'error', signal:AbortSignal.timeout(5000)});
      const data = await res.json();
      if (res.ok && data.RetCode === 0) state = ok;
      else if (id === 'ready' && res.status === 503 && data.RetCode === 5001) state = '未就绪';
    } catch { /* No probe response: the storage state is unknown. */ }
    const label = document.getElementById(id);
    label.textContent = state;
    label.dataset.state = state === ok ? 'ok' : state === '未就绪' ? 'warning' : 'unknown';
  }));
  setLocalTime(document.getElementById('checked'), new Date().toISOString());
  probe.textContent = '重新检查';
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
  let message;
  try {
    await navigator.clipboard.writeText(text.textContent);
    message = '已复制';
  } catch {
    const range = document.createRange();
    range.selectNodeContents(text);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    message = '已选中，请手动复制';
  }
  const status = button.closest('dialog')?.querySelector('[data-copy-status]');
  if (status) {
    status.textContent = `${text.closest('dd').previousElementSibling.textContent}：${message}`;
  } else {
    button.textContent = message;
    document.querySelector('#feedback').textContent = message;
  }
});
