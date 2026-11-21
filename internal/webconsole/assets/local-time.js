const locale = document.documentElement.lang;
const dateFields = {year: 'numeric', month: '2-digit', day: '2-digit'};
const date = new Intl.DateTimeFormat(locale, dateFields);
const dateTime = new Intl.DateTimeFormat(locale, {
  ...dateFields, hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
});
const zone = dateTime.resolvedOptions().timeZone;

export function setLocalTime(time, value = time.dateTime) {
  const instant = Date.parse(value);
  if (Number.isNaN(instant)) {
    time.removeAttribute('datetime');
    time.textContent = value || '未知';
    return;
  }
  time.dateTime = value;
  time.textContent = (time.hasAttribute('data-date-only') ? date : dateTime).format(instant);
  time.title = zone;
}
