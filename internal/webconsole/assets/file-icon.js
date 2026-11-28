export function fileExtension(name) {
  const dot = name.lastIndexOf('.');
  return dot > name.lastIndexOf('/') + 1 ? name.slice(dot + 1).toLowerCase() : '';
}

export function fileIcon(name) {
  const ext = fileExtension(name);
  if (ext === 'pdf') return 'pdf';
  if (/^(png|jpe?g|gif|webp|bmp|tiff?|heic|avif)$/.test(ext)) return 'image';
  if (/^(md|txt|json|ya?ml|log|xml|ini|toml)$/.test(ext)) return 'text';
  if (/^(zip|tar|gz|7z|rar|bz2|xz)$/.test(ext)) return 'archive';
  if (/^(docx?|odt|rtf)$/.test(ext)) return 'document';
  if (/^(xlsx?|ods|csv|tsv)$/.test(ext)) return 'spreadsheet';
  if (/^(pptx?|odp)$/.test(ext)) return 'presentation';
  if (/^(js|jsx|mjs|cjs|ts|tsx|go|py|rb|rs|java|kt|c|h|cc|cpp|hpp|cs|sh|bash|html|css|sql|php|swift|lua)$/.test(ext)) return 'code';
  if (/^(mp3|wav|flac|aac|m4a|ogg|opus)$/.test(ext)) return 'audio';
  if (/^(mp4|mov|webm|mkv|avi|m4v)$/.test(ext)) return 'video';
  return 'file';
}
