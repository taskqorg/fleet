// Behaviour check for OR-6 / principles §3: inserting rows above the viewport must not move the row being read.
// Run: node tests/ui/scroll.check.mjs   (needs a built book: npm run book). Prints "ok" or the shift in px.
import { chromium } from 'playwright';
const book = new URL('../../docs/design-system/book/index.html', import.meta.url).href;
const b = await chromium.launch();
try {
  const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
  await p.goto(book);
  const vl = p.locator('#data-list .vl').first();
  await vl.scrollIntoViewIfNeeded();
  await vl.evaluate(e => { e.scrollTop = 3200; });
  await p.waitForTimeout(200);
  const [text, top] = await vl.evaluate(e => { const r = [...e.querySelectorAll('.vl-row')].find(x => x.getBoundingClientRect().bottom > e.getBoundingClientRect().top + 1); return [r.textContent, r.getBoundingClientRect().top]; });
  await p.locator('#data-list button:has-text("Insert 5 above")').first().click();
  await p.waitForTimeout(200);
  const after = await vl.evaluate((e, t) => [...e.querySelectorAll('.vl-row')].find(x => x.textContent === t)?.getBoundingClientRect().top, text);
  const pill = await p.locator('#data-list .pill').first().textContent();
  console.log(after === top && pill === '5 new' ? 'ok' : `shift ${after - top}px, pill "${pill}"`);
} finally { await b.close(); }
