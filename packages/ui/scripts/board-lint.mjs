// Reports layout defects on the design board's canvases: top-level frames that overlap or stand closer than the
// minimum gap (Brilliant draws each frame's name above it), children drawn outside their frame, and texts drawn on
// top of a sibling in a frame without auto layout. It reads the saved .bl files as text and never opens
// Brilliant. A warning report, not a gate: it always exits 0 unless --strict is passed.
// Run: pnpm run board:lint [-- <board dir> ...] [--drafts] [--json] [--strict]
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const BOARD = fileURLToPath(new URL('../../../docs/design-system/board/', import.meta.url));
/** Minimum gap between top-level frames, px (the board layout rule in docs/design-system/README.md). */
export const GAP = 40;
const EPS = 0.5;

function canvases(dir, drafts) {
  return readdirSync(dir).flatMap(f => {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) {
      if (['Assets', 'Styles', 'Exports', 'options'].includes(f) || f.startsWith('.')) return [];
      if (f === 'drafts' && !drafts) return [];
      return canvases(p, drafts);
    }
    return f.endsWith('.bl') ? [p] : [];
  });
}

const STR = /"(?:[^"\\]|\\.)*"/g;
const num = v => (v === undefined || v === '' ? null : Number(v));

/** One size axis of s(...): a number, hug:N, fill or fill:N; fill takes the resolved ext(...) value. */
function axis(v, ext) {
  if (v === undefined) return null;
  const m = /^(?:hug:|fill:)?(-?\d+(?:\.\d+)?)$/.exec(v.trim());
  if (m) return Number(m[1]);
  return ext;
}

/** Parses a canvas into a tree of elements with their box relative to the parent. */
export function parse(text) {
  const root = { id: null, name: '(canvas)', children: [], depth: -1 };
  const stack = [root];
  let body = false;
  for (const line of text.split('\n')) {
    if (!body) { if (line.startsWith('---')) body = true; continue; }
    const m = /^( *)([0-9a-f]{16}) (.*)$/.exec(line);
    if (!m) continue;
    const depth = m[1].length / 2;
    const rest = m[3];
    const strings = rest.match(STR) ?? [];
    const name = strings.length ? strings[strings.length - 1].slice(1, -1).replace(/\\"/g, '"') : '';
    const bare = rest.replace(STR, '""');
    const p = /(?:^| )p\((-?[\d.]+),(-?[\d.]+)\)/.exec(bare);
    const s = /(?:^| )s\(([^,)]*)(?:,([^)]*))?\)/.exec(bare);
    const e = /(?:^| )ext\(([^,)]*),?([^)]*)\)/.exec(bare);
    const ew = e ? num(e[1]) : null;
    const eh = e ? num(e[2]) : null;
    const el = {
      id: m[2], name, depth, children: [],
      type: /^t\(/.test(bare) ? 'text' : /^inst\(/.test(bare) ? 'instance' : /^(r|c|l|v\(|vr\()/.test(bare) ? 'shape' : 'frame',
      autoLayout: /^al\(/.test(bare) || /^inst\([^)]*al\(/.test(bare),
      abs: / abs(?: |$)/.test(bare),
      clip: / clip(?: |$)/.test(bare),
      hidden: / hidden(?: |$)/.test(bare),
      derived: /(?:^| )mref\(/.test(bare),
      rotated: /(?:^| )rot\(/.test(bare),
      x: p ? Number(p[1]) : 0, y: p ? Number(p[2]) : 0,
      w: s ? axis(s[1], ew) : null, h: s ? axis(s[2] ?? s[1], eh) : null,
    };
    while (stack.length && stack[stack.length - 1].depth >= depth) stack.pop();
    const parent = stack[stack.length - 1] ?? root;
    el.parent = parent;
    parent.children.push(el);
    stack.push(el);
  }
  return root;
}

const sized = el => !el.hidden && el.w != null && el.h != null && el.w > 0 && el.h > 0;
const area = (a, b) => Math.max(0, Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x)) * Math.max(0, Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y));
const label = el => `${el.name || el.type} [${el.id}]`;

/** The findings of one canvas. */
export function lint(text) {
  const root = parse(text);
  const out = [];
  const top = root.children.filter(sized);
  // 1. Top-level frames overlap, or stand closer than the gap.
  for (let i = 0; i < top.length; i++) {
    for (let j = i + 1; j < top.length; j++) {
      const a = top[i], b = top[j];
      const ov = area(a, b);
      if (ov > 1) { out.push({ kind: 'overlap', a: label(a), b: label(b), detail: `${Math.round(ov)} px² shared` }); continue; }
      const dx = Math.max(b.x - (a.x + a.w), a.x - (b.x + b.w));
      const dy = Math.max(b.y - (a.y + a.h), a.y - (b.y + b.h));
      const gap = Math.max(dx, dy);
      // Only neighbours that face each other (their spans overlap on the other axis) count.
      const facing = dx > dy ? Math.min(a.y + a.h, b.y + b.h) > Math.max(a.y, b.y) : Math.min(a.x + a.w, b.x + b.w) > Math.max(a.x, b.x);
      if (facing && gap < GAP - EPS) out.push({ kind: 'tight', a: label(a), b: label(b), detail: `gap ${Math.round(gap)} px < ${GAP}` });
    }
  }
  // 2. Children outside their frame; 3. a title or frame on top of a sibling where no auto layout places them.
  const walk = el => {
    for (const c of el.children) {
      if (c.derived || c.rotated || c.hidden) continue; // copies follow their master; a rotated box is not its p/s rectangle
      if (el.id && sized(el) && sized(c) && el.type !== 'instance') {
        // A clipping frame hides what passes its edge, and an abs child is a floating overlay (a menu, a popover)
        // drawn on top on purpose; neither puts content under content. 2 px covers stroke and rounding.
        const by = Math.max(-c.x, -c.y, c.x + c.w - el.w, c.y + c.h - el.h);
        if (!el.clip && !c.abs && by > 2) out.push({ kind: 'outside', a: label(c), b: label(el), detail: `${Math.round(by)} px past the edge` });
      }
      if (c.type !== 'instance') walk(c);
    }
    const free = el.children.filter(c => sized(c) && !c.derived && !c.rotated && (!el.autoLayout || c.abs) && (c.type === 'text' || c.type === 'frame' || c.type === 'instance'));
    if (el.depth === -1) return; // top level is check 1
    for (let i = 0; i < free.length; i++) {
      for (let j = i + 1; j < free.length; j++) {
        const a = free[i], b = free[j];
        if (a.type !== 'text' && b.type !== 'text') continue; // overlapping panels are often a deliberate popover
        const ov = area(a, b);
        if (ov > Math.min(a.w * a.h, b.w * b.h) * 0.1) out.push({ kind: 'collision', a: label(a), b: label(b), detail: `in ${label(el)}` });
      }
    }
  };
  walk(root);
  return out;
}

function main() {
  const args = process.argv.slice(2);
  const flags = new Set(args.filter(a => a.startsWith('--')));
  const dirs = args.filter(a => !a.startsWith('--')).map(d => resolve(d));
  const roots = dirs.length ? dirs : [BOARD];
  const report = [];
  for (const dir of roots) {
    if (!existsSync(dir)) { console.error(`board-lint: no such board ${dir}`); continue; }
    for (const p of canvases(dir, flags.has('--drafts')).sort()) {
      const findings = lint(readFileSync(p, 'utf8'));
      if (findings.length) report.push({ canvas: relative(dir, p).replace(/\.bl$/, ''), findings });
    }
  }
  if (flags.has('--json')) console.log(JSON.stringify(report, null, 1));
  else {
    for (const { canvas, findings } of report) {
      console.log(`${canvas}: ${findings.length}`);
      for (const f of findings) console.log(`  ${f.kind}: ${f.a} / ${f.b} (${f.detail})`);
    }
    const n = report.reduce((s, r) => s + r.findings.length, 0);
    console.log(n ? `board-lint: ${n} finding(s) on ${report.length} canvas(es); layout rule: docs/design-system/README.md#board-layout` : 'board-lint: clean');
  }
  if (flags.has('--strict') && report.length) process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
