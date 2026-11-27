// Builds the standalone design book, catalog and demos. The generated HTML stays out of git.
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { buildCatalog, buildPage, previewDocument, repository } from './scene-build.mjs';
import { readScenes } from './scenes.ts';
import { uiCss } from './tokens.mjs';

const ui = new URL('../', import.meta.url);
function write(outPath, html) {
  const out = new URL(outPath, ui);
  mkdirSync(new URL('.', out), { recursive: true });
  writeFileSync(out, html);
  const size = Buffer.byteLength(html);
  console.log(`${outPath.replace('../../', '')}: ${(size / 1024).toFixed(0)} KB (limit 512 KB)${size > 512 * 1024 ? '  OVER LIMIT' : ''}`);
}
async function page(entry, outPath, title, extraCss) {
  const bundle = await buildPage({ entryPoints: ['packages/ui/' + entry] });
  write(outPath, previewDocument(bundle.outputFiles[0].text, title, uiCss(extraCss)));
}
const bookCss = readFileSync(new URL('book/book.css', ui), 'utf8');
await page('book/book.tsx', '../../docs/design-system/book/index.html', 'Fleet design book', bookCss);
await page('book/masters.tsx', '../../docs/design-system/book/masters.html', 'Fleet board masters',
  bookCss + '.bm-v{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-bottom:8px}');
write('../../docs/design-system/pages/mockups.html', await buildCatalog(readScenes(repository)));
await page('book/open-questions.tsx', '../../docs/design-system/explorations/open-questions-01.html', 'Fleet open questions 01',
  readFileSync(new URL('book/oq.css', ui), 'utf8'));
