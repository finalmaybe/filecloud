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
