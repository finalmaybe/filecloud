import { api, type Policy, type View } from "./ui.js";

export function retentionDaysLabel(days: number) {
  return days < 0
    ? "永久保留"
    : days > 0
      ? `${days} 天`
      : "尚未获取服务器默认值";
}

export function retentionDefaultLabel(days: number) {
  const label = retentionDaysLabel(days);
  return days ? `服务器默认：${label}` : label;
}

export function showRetentionDefaults(view: View, show: (p: Policy) => void) {
  const known = {
    TrashDays: view.DefaultTrashDays,
    HistoryDays: view.DefaultHistoryDays,
  };
  show(known);
  if (view.CatalogSession && (!known.TrashDays || !known.HistoryDays))
    void api.RetentionDefaults().then(
      (p) =>
        show({
          TrashDays: p.TrashDays || known.TrashDays,
          HistoryDays: p.HistoryDays || known.HistoryDays,
        }),
      () => {},
    );
}
