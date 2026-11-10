// Layout rules checked on a rendered page (docs/design-system/owner-rules.md, "How each rule is checked").
// Requirement: OR-28 one format per value (prose never in mono, headers never wrap); OR-24 three columns and no top bar, OR-25 one scroll per column, OR-26 the whole row opens, OR-27 open in the work area.
// Risk: these cannot be seen in source text, so a page breaks them while every source check stays quiet (D19).
// Used by ux.check.mjs (mockups) and app.check.ts (built app). Call it last for a page: the row click navigates.
import type { Page } from 'playwright';

export async function layoutFindings(p: Page): Promise<string[]> {
  const out = await p.evaluate(() => {
    const out: string[] = [];
    const name = (e: Element) => e.tagName.toLowerCase() + (e.classList[0] ? '.' + e.classList[0] : '');
    const scroller = (e: Element) => /auto|scroll/.test(getComputedStyle(e).overflowY);
    const scrolls = (e: Element) => scroller(e) && e.scrollHeight > e.clientHeight + 1;
    const shown = (e: Element | null) => !!e && e.getBoundingClientRect().width > 0;
    if (document.querySelector('[data-ui=app-shell] .topbar, [data-ui=app-shell] > header')) out.push('OR-24 top bar in the shell');
    // OR-24: navigation, control column and work area are all on screen (wide window only; narrow shows one of the last two).
    const ld = document.querySelector('[data-ui=list-detail]');
    if (!shown(document.querySelector('[data-ui=nav-rail]'))) out.push('OR-24 navigation column missing');
    if (innerWidth >= 1100 && ld && [...ld.children].filter(shown).length < 2) out.push('OR-24 control column or work area missing');
    if (document.querySelector('[data-ui=app-shell]') && !document.querySelector('[data-ui=nav-rail] .brand')) out.push('OR-24 the product name is not in the navigation');
    if (document.documentElement.scrollHeight > innerHeight + 1) out.push(`OR-25 the page itself scrolls (${document.documentElement.scrollHeight} px in a ${innerHeight} px window)`);
    for (const e of document.querySelectorAll('[data-ui=app-shell] *')) {
      // The outer one counts as a scroller by its style alone: it scrolls as soon as its column grows.
      if (scrolls(e)) for (let a = e.parentElement; a; a = a.parentElement) if (scroller(a)) { out.push(`OR-25 nested scroll: ${name(e)} inside ${name(a)}`); break; }
      const s = getComputedStyle(e), r = e.getBoundingClientRect();
      if (/fixed|sticky|absolute/.test(s.position) && r.width * r.height > innerWidth * innerHeight * 0.3) out.push(`OR-27 ${s.position} panel over the page: ${name(e)}`);
    }
      // OR-28 (formats.md): prose set in mono (mono is for codes, ids, paths, hashes, models and aligned numbers),
      // and a column header that wraps or is cut.
      for (const el of document.querySelectorAll('[data-ui=app-shell] span, [data-ui=app-shell] button, [data-ui=app-shell] div')) {
        if (!(el.getBoundingClientRect().width > 0 && getComputedStyle(el).visibility !== 'hidden') || el.children.length || el.closest('pre,code,kbd,.code,textarea,.md')) continue;
        const words = (el.textContent ?? '').trim().split(/\s+/).filter(w => /^[A-Za-z]{3,}$/.test(w));
        if (words.length >= 4 && /mono/i.test(getComputedStyle(el).fontFamily.split(',')[0])) out.push(`OR-28 prose in mono: "${(el.textContent ?? '').trim().slice(0, 50)}"`);
      }
      for (const el of document.querySelectorAll('.vl-head > span')) if (el.getBoundingClientRect().width > 0 && (el.scrollWidth > el.clientWidth + 1 || el.getBoundingClientRect().height > 24)) out.push(`OR-28 column header wraps or is cut: "${(el.textContent ?? '').trim()}"`);
    return [...new Set(out)];
  });
  // OR-26: a click on the row's left padding, away from the title, opens it.
  // A row with a title link but no row-link class would never open from its edge, so it is picked too.
  const row = await p.$('.vl-row:not(.sel):not(.grp):has(.row-link, a.title), .kv li:has(.row-link)');
  const box = await row?.boundingBox();
  if (row && box) {
    const before = p.url();
    await p.mouse.click(box.x + 3, box.y + box.height / 2); await p.waitForTimeout(200);
    // Opened: the address changed, or the row became the selected one (a mockup may keep its selection in state).
    const selected = await row.evaluate(e => e.classList.contains('sel')).catch(() => false);
    if (p.url() === before && !selected) out.push('OR-26 a click on the row edge did not open the item');
  }
  return out;
}
