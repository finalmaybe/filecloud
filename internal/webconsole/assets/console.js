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
  } catch {
    const range = document.createRange();
    range.selectNodeContents(text);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    button.textContent = '已选中，请手动复制';
  }
});
