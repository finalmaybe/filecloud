import assert from 'node:assert/strict';
import test from 'node:test';
import {fileIcon} from './assets/file-icon.js';

test('fileIcon classifies filenames by their final, case-insensitive extension', () => {
  for (const [name, icon] of [
    ['photos/Family.JpEg', 'image'],
    ['Report.PDF', 'pdf'],
    ['notes.md', 'text'],
    ['backup.tar.GZ', 'archive'],
    ['letter.docx', 'document'],
    ['export.CSV', 'spreadsheet'],
    ['slides.pptx', 'presentation'],
    ['src/ui.test.TSX', 'code'],
    ['music.flac', 'audio'],
    ['movie.webm', 'video'],
    ['pdf', 'file'],
    ['archive.zip/README', 'file'],
    ['notes.md.unknown', 'file'],
    ['ending-dot.', 'file'],
    ['.gitignore', 'file'],
    ['hidden/.pdf', 'file'],
    ['.config.json', 'text'],
  ]) assert.equal(fileIcon(name), icon, name);
});
