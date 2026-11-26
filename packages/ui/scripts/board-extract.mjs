// Step 1 of the board's component masters: reads every section of the built design book (pnpm run book) in the dark
// theme, with each element's computed style and its light-theme twin's colours, into one JSON file.
// Step 2, board-components.py, turns that file into Brilliant html fences, one per batch of ten masters.
// Run: node packages/ui/scripts/board-extract.mjs /tmp/book.json
// With --masters it reads the board masters page (book/masters.tsx, every state of each master) instead: one entry per
// master with its variants, each variant's tree named by its slots and holding nested atoms as {inst, at}; step 2 is
// board-masters.py. Run: node packages/ui/scripts/board-extract.mjs --masters /tmp/masters.json
import { chromium } from 'playwright';
import { writeFileSync } from 'node:fs';
const MASTERS = process.argv.includes('--masters');
const OUT = process.argv.slice(2).filter(a => !a.startsWith('--'))[0];
const b = await chromium.launch(); const p = await b.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark', reducedMotion: 'reduce' });
await p.goto(new URL(`../../../docs/design-system/book/${MASTERS ? 'masters' : 'index'}.html`, import.meta.url).href);
await p.evaluate(() => { document.documentElement.dataset.theme = 'dark'; });
await p.waitForTimeout(800);
const data = MASTERS ? await p.evaluate(extractMasters) : await p.evaluate(() => {
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
writeFileSync(OUT ?? 'book.json', JSON.stringify(data));
console.log(data.length, data.map(d => d.id ?? `${d.name}(${d.variants.length})`).join(' '));
await b.close();

// In the page: each [data-master] section's variants. Slot marks (nametext) become the text and
// its name; a NEST atom inside a master becomes {inst, at, texts} and is not walked.
function extractMasters() {
  const nest = window.__boardNest;
  const keep = ['display','flex-direction','flex-wrap','align-items','justify-content','gap','row-gap','column-gap','padding-top','padding-right','padding-bottom','padding-left','background-color','color','border-top-width','border-top-color','border-top-style','border-bottom-width','border-bottom-color','border-left-width','border-left-color','border-right-width','border-right-color','border-radius','font-size','font-weight','font-family','font-style','opacity','text-decoration-line','white-space','grid-template-columns','overflow','stroke','fill','flex-grow'];
  const MARK = /([^]*)([^]*)/g;
  // Unmark every text first, so layout is measured on the real text.
  const slotOf = new WeakMap();
  const tw = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  const marked = [];
  for (let t = tw.nextNode(); t; t = tw.nextNode()) if (t.textContent.includes('')) marked.push(t);
  for (const t of marked) {
    const parts = t.textContent.split(MARK);
    if (parts.length === 4 && !parts[0] && !parts[3]) { slotOf.set(t, parts[1]); t.textContent = parts[2]; continue; }
    const frag = document.createDocumentFragment();
    for (let i = 0; i < parts.length; i++) {
      if (i % 3 === 0) { if (parts[i]) frag.append(parts[i]); continue; }
      const n = document.createTextNode(parts[i + 1]); slotOf.set(n, parts[i]); frag.append(n); i++;
    }
    t.replaceWith(frag);
  }
  const ICON = el => /lucide-([a-z0-9-]+)/.exec(el.getAttribute('class') ?? '')?.[1];
  function walk(el, twin, ctx, root) {
    const cs = getComputedStyle(el); const r = el.getBoundingClientRect();
    if (cs.display === 'none' || cs.visibility === 'hidden' || (r.width === 0 && r.height === 0)) return null;
    if (r.width <= 1 && r.height <= 1 && cs.position === 'absolute') return null;
    const n = { tag: el.tagName.toLowerCase(), w: Math.round(r.width * 100) / 100, h: Math.round(r.height * 100) / 100, s: {} };
    for (const k of keep) n.s[k] = cs.getPropertyValue(k);
    if (twin) { const ts = getComputedStyle(twin); n.l = { color: ts.color, bg: ts.backgroundColor, border: ts.borderTopColor, borderL: ts.borderLeftColor, stroke: ts.stroke }; }
    const ui = el.getAttribute('data-ui');
    if (!root && ui && nest[ui]) {
      const at = nest[ui].at(el);
      if (at) { n.inst = nest[ui].master; n.at = at; n.texts = [...el.querySelectorAll('*')].flatMap(e => [...e.childNodes]).filter(c => c.nodeType === 3 && c.textContent.trim()).map(c => c.textContent.replace(/\s+/g, ' ').trim()); return n; }
    }
    if (n.tag === 'svg') {
      n.icon = ICON(el) ?? null; n.name = ctx.icon();
      n.svg = el.outerHTML.replace(/currentColor/g, cs.color);
      return n;
    }
    if (el.tagName === 'IMG') { n.img = true; return n; }
    n.c = [];
    const tk = twin ? [...twin.childNodes] : [];
    let ti = 0;
    for (const ch of el.childNodes) {
      const t2 = tk[ti++];
      if (ch.nodeType === 3) {
        const t = ch.textContent.replace(/\s+/g, ' ');
        if (t.trim()) n.c.push({ text: t, name: slotOf.get(ch) ?? ctx.text() });
      } else if (ch.nodeType === 1) {
        const k = walk(ch, t2 && t2.nodeType === 1 && t2.tagName === ch.tagName ? t2 : null, ctx, false);
        if (k) n.c.push(k);
      }
    }
    if (['input', 'textarea'].includes(n.tag)) n.c.push({ text: el.value || el.placeholder || '', name: ctx.text() });
    return n;
  }
  return [...document.querySelectorAll('section[data-master]')].map(sec => {
    const texts = (sec.dataset.texts ?? '').split('\n').filter(Boolean);
    return {
      name: sec.dataset.master, w: Number(sec.dataset.w) || 0,
      variants: [...sec.querySelectorAll(':scope > .bm-v')].map(v => {
        const pick = v.dataset.pick;
        const box = t => v.querySelector(`.th-${t} .bm-box`);
        const rootOf = b => (pick ? b.querySelector(pick) : b.firstElementChild);
        const dark = rootOf(box('dark')), light = rootOf(box('light'));
        let ti = 0, ii = 0;
        const ctx = { text: () => texts[ti++] ?? `text ${ti}`, icon: () => (ii++ ? `icon ${ii}` : 'icon') };
        const at = Object.fromEntries((v.dataset.variant ?? '').split(';').filter(Boolean).map(x => x.split('=')));
        return { at, tree: dark ? walk(dark, light, ctx, true) : null };
      }),
    };
  });
}
