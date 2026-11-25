/// <reference lib="dom" />
// OR-66 no horizontal scroll (docs/design-system/owner-rules.md): every sideways overflow on a rendered page. Runs in the
// page (page.evaluate(hscrollScan)); used by scripts/hscroll.ts on the deployed site and by tests/ui/app.check.ts on the
// built app, so both see the same cases.
export type Finding = { kind: 'page' | 'scroller' | 'sticks-out'; el: string; px: number; cause?: string };

/** Runs in the page: every horizontal overflow, outermost first; a descendant of a reported box is not repeated. */
export function hscrollScan(): Finding[] {
  const out: Finding[] = [], seen: Element[] = [];
  const name = (e: Element) => {
    const parts: string[] = [];
    for (let x: Element | null = e; x && x !== document.body && parts.length < 4; x = x.parentElement) {
      const cls = [...x.classList].slice(0, 2).map(c => '.' + c).join('');
      parts.unshift(x.tagName.toLowerCase() + (x.id ? '#' + x.id : '') + cls);
    }
    return parts.join(' > ');
  };
  // The innermost element past the right edge of a box: what makes it overflow.
  const cause = (box: Element) => {
    const edge = box.getBoundingClientRect().left + box.clientWidth + 1;
    let hit: Element | undefined;
    for (const d of box.querySelectorAll('*')) if (d.getBoundingClientRect().right > edge && d.getBoundingClientRect().width) hit = d;
    return hit ? name(hit) : undefined;
  };
  const root = document.scrollingElement!;
  const vw = root.clientWidth;
  if (root.scrollWidth > vw + 1) out.push({ kind: 'page', el: 'document', px: root.scrollWidth - vw });
  const inside = (e: Element) => seen.some(s => s.contains(e));
  for (const e of document.body.querySelectorAll('*')) {
    if (inside(e)) continue;
    const r = e.getBoundingClientRect();
    if (!r.width || !r.height) continue;
    const s = getComputedStyle(e);
    if (s.visibility === 'hidden' || s.display === 'none') continue;
    if ((s.overflowX === 'auto' || s.overflowX === 'scroll') && e.scrollWidth > e.clientWidth + 1) {
      out.push({ kind: 'scroller', el: name(e), px: e.scrollWidth - e.clientWidth, cause: cause(e) }); seen.push(e); continue;
    }
    // Content past the viewport's right edge that is not clipped by an ancestor.
    if (r.right > vw + 1 && s.position !== 'fixed') {
      let clipped = false;
      for (let a = e.parentElement; a; a = a.parentElement) {
        const o = getComputedStyle(a).overflowX;
        if (o !== 'visible' && a.getBoundingClientRect().right <= vw + 1) { clipped = true; break; }
      }
      if (!clipped) { out.push({ kind: 'sticks-out', el: name(e), px: Math.round(r.right - vw) }); seen.push(e); }
    }
  }
  return out;
}

