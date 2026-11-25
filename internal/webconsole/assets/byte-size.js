export function setByteSize(cell, bytes) {
  cell.title = `${bytes} 字节`;
  let n = Number(bytes);
  for (const unit of ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB', 'ZiB']) {
    if (n < 1024) {
      cell.textContent = `${Number(n.toFixed(1))} ${unit}`;
      return;
    }
    n /= 1024;
  }
  cell.textContent = `${bytes} B`;
}
