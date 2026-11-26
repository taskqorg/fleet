// Step 1 of the board's component masters: reads every section of the built design book (pnpm run book) in the dark
// theme, with each element's computed style and its light-theme twin's colours, into one JSON file.
// Step 2, board-components.py, turns that file into Brilliant html fences, one per batch of ten masters.
// Run: node packages/ui/scripts/board-extract.mjs /tmp/book.json
import { chromium } from 'playwright';
import { writeFileSync } from 'node:fs';
const b = await chromium.launch(); const p = await b.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
await p.goto(new URL('../../../docs/design-system/book/index.html', import.meta.url).href);
await p.evaluate(() => { document.documentElement.dataset.theme = 'dark'; });
await p.waitForTimeout(800);
const data = await p.evaluate(() => {
  const keep = ['display','flex-direction','flex-wrap','align-items','justify-content','gap','row-gap','column-gap','padding-top','padding-right','padding-bottom','padding-left','background-color','color','border-top-width','border-top-color','border-top-style','border-bottom-width','border-bottom-color','border-left-width','border-left-color','border-right-width','border-right-color','border-radius','font-size','font-weight','font-family','font-style','border-top-style','opacity','text-decoration-line','white-space','grid-template-columns','overflow'];
  function walk(el, depth, twin) {
    const cs = getComputedStyle(el); const r = el.getBoundingClientRect();
    if (el.classList.contains('th-light') || el.classList.contains('frame-label')) return null;
    if (r.width <= 1 && r.height <= 1 && cs.position === 'absolute') return null;
    if (cs.display === 'none' || cs.visibility === 'hidden' || (r.width === 0 && r.height === 0)) return null;
    const n = { tag: el.tagName.toLowerCase(), w: Math.round(r.width*100)/100, h: Math.round(r.height*100)/100, s: {} };
    for (const k of keep) n.s[k] = cs.getPropertyValue(k);
    if (twin) { const ts = getComputedStyle(twin); n.l = { color: ts.color, bg: ts.backgroundColor, border: ts.borderTopColor, borderL: ts.borderLeftColor }; }
    if (n.tag === 'svg') { n.svg = el.outerHTML.replace(/currentColor/g, cs.color); return n; }
    if (el.tagName === 'IMG') { n.img = true; return n; }
    n.c = [];
    const tk = twin ? [...twin.childNodes] : [];
    let ti = 0;
    for (const ch of el.childNodes) {
      const tw = tk[ti++];
      if (ch.nodeType === 3) { const t = ch.textContent.replace(/\s+/g,' '); if (t.trim()) n.c.push({ text: t }); }
      else if (ch.nodeType === 1 && depth < 40) { let t2 = tw && tw.nodeType === 1 && tw.tagName === ch.tagName ? tw : null; if (ch.classList && ch.classList.contains('th-dark')) t2 = ch.parentElement.querySelector(':scope > .th-light'); const k = walk(ch, depth+1, t2); if (k) n.c.push(k); }
    }
    if (['input','textarea'].includes(n.tag)) n.c.push({ text: el.value || el.placeholder || '' });
    return n;
  }
  return [...document.querySelectorAll('section[id]')].map(sec => {
    const head = sec.querySelector('h2,h3');
    const kids = [...sec.children].filter(c => c !== head && !c.contains(head));
    return { id: sec.id, title: head ? head.childNodes[0]?.textContent?.trim() : sec.id, spec: head?.querySelector('a')?.textContent, w: Math.round(sec.getBoundingClientRect().width), kids: kids.map(k => walk(k, 0, null)).filter(Boolean) };
  });
});
writeFileSync(process.argv[2] ?? 'book.json', JSON.stringify(data));
console.log(data.length, data.map(d => d.id).join(' '));
await b.close();
