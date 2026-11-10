// UX checks on the built mockups (D14). Warnings only; never fails. Run after `pnpm run book`.
// Catches: repeated words in one region (OR-17), touch targets under 44 px with a coarse pointer,
// status shown by colour only (WCAG 1.4.1, OR-18), truncated important text (OR-19), detail/outcome
// outside the viewport at 600 and 1100 px (OR-19), JS size and interaction latency against targets.
// Proof mode: UX_INJECT=repeat|touch|colour|truncate|offscreen|latency|size injects one defect into the page.
// Screenshots of each route go to design-report/shots/ (for CI artifacts).
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';
import { gzipSync } from 'node:zlib';

const page = new URL('../../docs/design-system/pages/mockups.html', import.meta.url).href;
const shots = new URL('../../design-report/shots/', import.meta.url);
mkdirSync(shots, { recursive: true });
const ROUTES = ['/overview', '/tasks', '/tasks/9989', '/tasks/9978', '/results', '/hosts', '/laboratory'];
const DETAIL_ROUTES = ['/tasks/9989', '/tasks/9978', '/results'];
const INJECT = process.env.UX_INJECT || '';
const TARGET_JS = 200 * 1024, TARGET_MS = 100;
const warn = [];

async function inject(p, what) {
  if (INJECT !== what) return;
  await p.evaluate(kind => {
    const main = document.getElementById('main') || document.body;
    const add = html => main.insertAdjacentHTML('afterbegin', html);
    if (kind === 'repeat') { const r = document.querySelector('.vl-row:not(.grp) .title'); if (r) r.textContent += ' windows windows'; }
    if (kind === 'touch') add('<button type="button" style="width:20px;height:20px">x</button>');
    if (kind === 'colour') add('<span class="chip-age t-danger alert">13h</span>');
    if (kind === 'truncate') add('<span data-important style="display:inline-block;width:40px;overflow:hidden;white-space:nowrap">18 GB free, warning at 25 GB</span>');
    if (kind === 'size') { const s = document.createElement('script'); s.type = 'text/plain'; s.textContent = 'x'.repeat(250 * 1024); document.body.append(s); }
    if (kind === 'latency') document.addEventListener('click', () => { const t = performance.now(); while (performance.now() - t < 150) { /* busy */ } }, true);
  }, what);
  if (what === 'offscreen') await p.addStyleTag({ content: '.split-view.has-detail>.detail{order:5!important}' });
}

const b = await chromium.launch();
try {
  // 1, 3, 4: per route at desktop width, plus screenshots.
  for (const r of ROUTES) {
    const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
    await p.goto(`${page}#${r}`); await p.waitForTimeout(200);
    for (const k of ['repeat', 'colour', 'truncate']) await inject(p, k);
    const found = await p.evaluate(() => {
      const out = [];
      const visible = e => { const s = getComputedStyle(e); const rc = e.getBoundingClientRect(); return s.visibility !== 'hidden' && s.display !== 'none' && rc.width > 0 && rc.height > 0; };
      const STOP = new Set(['the', 'and', 'for', 'with', 'from', 'not', 'are', 'has', 'all']);
      // OR-17: a word repeated inside one region (row, heading, card meta, stat).
      for (const el of document.querySelectorAll('.vl-row:not(.grp), .kv li, .panel-h, .page-h, .detail-h, .detail-title, .state-line, .stat, .topbar, .decision .meta, .problem')) {
        if (!visible(el)) continue;
        const words = (el.innerText.toLowerCase().match(/[a-z]{3,}/g) || []).filter(w => !STOP.has(w));
        const dup = words.find((w, i) => words.indexOf(w) !== i);
        if (dup) out.push(`OR-17 repeated word "${dup}" in ${el.className.split(' ')[0] || el.tagName.toLowerCase()}: "${el.innerText.replace(/\s+/g, ' ').slice(0, 60)}"`);
      }
      // WCAG 1.4.1 / OR-18: attention or danger tone with no icon and no meaning in text or name.
      const MEANING = /fail|error|warn|alert|long|full|low|exhaust|unavailable|offline|need|attention|look|unconfirmed/i;
      for (const el of document.querySelectorAll('[class*="t-danger"], [class*="t-attention"]')) {
        if (!visible(el) || el.querySelector('.icon') || el.closest('.icon')) continue;
        const said = `${el.getAttribute('aria-label') || ''} ${el.innerText} ${el.getAttribute('data-tip') || ''}`;
        if (!MEANING.test(said)) out.push(`WCAG 1.4.1 status by colour only: "${el.innerText.trim().slice(0, 30)}" (${el.className})`);
      }
      // OR-19: important text cut off by its own box or a clipping ancestor.
      for (const el of document.querySelectorAll('[data-important]')) {
        if (!visible(el)) continue;
        let cut = el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1;
        const rc = el.getBoundingClientRect();
        for (let a = el.parentElement; a && !cut; a = a.parentElement) {
          const s = getComputedStyle(a);
          if (s.overflow !== 'visible' || s.overflowX !== 'visible') { const ar = a.getBoundingClientRect(); if (rc.right > ar.right + 1 || rc.bottom > ar.bottom + 1) cut = true; }
        }
        if (cut) out.push(`OR-19 important text truncated: "${el.innerText.replace(/\s+/g, ' ').slice(0, 60)}"`);
      }
      return out;
    });
    found.forEach(f => warn.push(`#${r}  ${f}`));
    await p.screenshot({ path: new URL(`${r.slice(1).replace('/', '-')}-1440.png`, shots).pathname, fullPage: true });
    await p.close();
  }

  // 2: touch targets with a coarse pointer (touch emulation makes (pointer: coarse) match).
  const ctx = await b.newContext({ viewport: { width: 600, height: 900 }, hasTouch: true, isMobile: true });
  for (const r of ROUTES) {
    const p = await ctx.newPage();
    await p.goto(`${page}#${r}`); await p.waitForTimeout(200);
    await inject(p, 'touch');
    const coarse = await p.evaluate(() => matchMedia('(pointer: coarse)').matches);
    if (!coarse) { warn.push(`touch check not run: (pointer: coarse) did not match`); await p.close(); break; }
    const small = await p.evaluate(() => {
      const seen = new Map();
      for (const el of document.querySelectorAll('a[href], button, input, select, [role=tab]')) {
        // An input inside a label is hit through the whole label.
        const rc = (el.tagName === 'INPUT' && el.closest('label') || el).getBoundingClientRect(); const s = getComputedStyle(el);
        if (!rc.width || !rc.height || s.visibility === 'hidden' || rc.bottom < 0 || rc.top > innerHeight) continue;
        if (rc.width < 43 || rc.height < 43) { const k = `${el.tagName.toLowerCase()}.${(el.className || '').toString().split(' ').filter(Boolean).slice(0, 2).join('.')}`; seen.set(k, `${Math.round(rc.width)}×${Math.round(rc.height)}`); }
      }
      return [...seen].map(([k, v]) => `${k} ${v}`);
    });
    small.forEach(s => warn.push(`#${r}  touch target under 44 px: ${s}`));
    if (r === '/tasks/9989') await p.screenshot({ path: new URL('tasks-9989-600-touch.png', shots).pathname });
    await p.close();
  }
  await ctx.close();

  // 5: the detail of the opened item is visible without scrolling at 600 and 1100 px.
  for (const w of [600, 1100]) for (const r of DETAIL_ROUTES) {
    const p = await b.newPage({ viewport: { width: w, height: 900 } });
    await p.goto(`${page}#${r}`); await p.waitForTimeout(250);
    await inject(p, 'offscreen');
    if (INJECT === 'offscreen') { await p.evaluate(() => scrollTo(0, 0)); }
    const top = await p.evaluate(() => { const d = document.querySelector('[data-ui=detail]'); return d ? d.getBoundingClientRect().top : null; });
    if (top === null) warn.push(`#${r} @${w}px  no [data-ui=detail] rendered`);
    else if (top > 900 - 120 || top < -50) warn.push(`#${r} @${w}px  OR-19 detail outside the viewport (top ${Math.round(top)} px)`);
    if (w === 1100) await p.screenshot({ path: new URL(`${r.slice(1).replace('/', '-')}-1100.png`, shots).pathname });
    await p.close();
  }

  // 6: JS size of the page and interaction latency (click task row until detail heading shows).
  const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
  await p.goto(`${page}#/tasks`); await p.waitForTimeout(300);
  await inject(p, 'size'); await inject(p, 'latency');
  const js = await p.evaluate(() => [...document.scripts].map(s => s.textContent).join(''));
  const raw = Buffer.byteLength(js), gz = gzipSync(js).length;
  const times = [];
  for (let i = 0; i < 5; i++) {
    await p.goto(`${page}#/tasks`); await p.waitForTimeout(150);
    await inject(p, 'latency');
    times.push(await p.evaluate(async i => {
      const link = document.querySelectorAll('.vl-row:not(.grp) .row-link')[i];
      const t0 = performance.now(); link.click();
      while (!document.querySelector('[data-ui=detail] .detail-title')) await new Promise(r => requestAnimationFrame(r));
      return performance.now() - t0;
    }, i));
  }
  times.sort((a, z) => a - z);
  const med = times[2];
  await p.close();
  console.log(`- page JS: ${(raw / 1024).toFixed(0)} KB raw, ${(gz / 1024).toFixed(0)} KB gzip (target ≤ 200 KB raw)${raw > TARGET_JS ? '  OVER TARGET' : ''}`);
  console.log(`- interaction latency (task row to detail): median ${med.toFixed(0)} ms of 5 (target ≤ ${TARGET_MS} ms)${med > TARGET_MS ? '  OVER TARGET' : ''}`);
} finally { await b.close(); }
console.log(warn.length ? `- ${warn.length} UX warnings:\n    ${warn.join('\n    ')}` : '- UX checks: no warnings');
