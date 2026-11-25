// UI context (docs/design-system/README.md, "Context for a UI task"): the minimal reading an agent needs before it
// designs or builds one page. Reads the page spec, its blocks (the links in its `## Blocks` section), the controls and patterns it and they link, its board canvas,
// and the text of every rule they cite by ID (OR-<n> in owner-rules.md, UR-<n> in principles.md). Prints Markdown;
// never fails a build. Node standard library only.
// Usage: node scripts/ui-context.ts <page>   (a page spec name such as home; without one it lists the pages)
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { dirname, join, normalize, relative } from 'node:path';

const root = join(import.meta.dirname, '..');
const ds = 'docs/design-system';
const read = (p: string) => readFileSync(join(root, p), 'utf8');

// Rule texts by ID. Owner rules are table rows `| **OR-<n> Name** | text |` in owner-rules.md; usability rules
// are `UR-<n>` rows in principles.md.
export function rules(owner: string, usability: string): Map<string, string> {
  const m = new Map<string, string>();
  for (const [, id, name, text] of owner.matchAll(/^\| \*\*(OR-\d+) ([^*]+)\*\* \| (.+?) \|\s*$/gm)) m.set(id, `${name}: ${text}`);
  // principles.md keeps them as table rows `| **UR-<n>.** | rule | check |`; a bold lead in a bullet also counts.
  for (const [, id, text] of usability.matchAll(/^\| \*\*(UR-\d+)\.?\*\* \| (.+?) \|/gm)) m.set(id, text.trim());
  for (const [, id, rest] of usability.matchAll(/^[ \t]*(?:[*-] )?\*\*(UR-\d+)[ .:]*([^\n|]*)$/gm)) if (!m.has(id)) m.set(id, rest.replace(/\*\*/g, '').trim());
  return m;
}

// The three principles, the first sentence of each, from the Principles section of owner-rules.md.
export function principles(owner: string): string[] {
  const sec = owner.split(/^## /m).find(s => s.startsWith('Principles')) ?? '';
  return [...sec.matchAll(/^\d+\. \*\*([^*]+)\*\* ([^\n]+)$/gm)].map(([, n, t]) => `${n} ${t.split('. ')[0].replace(/\.$/, '')}.`);
}

// Rule IDs in a text; a range such as `OR-24 to OR-27` counts every ID in it.
export function cited(text: string): string[] {
  const ids = new Set(text.match(/\b(?:OR|UR)-\d+\b/g) ?? []);
  for (const [, k, a, b] of text.matchAll(/\b(OR|UR)-(\d+) to \1-(\d+)\b/g)) for (let n = +a; n <= +b; n++) ids.add(`${k}-${n}`);
  return [...ids];
}

// Relative Markdown links in the file at `from`, as [repository path, anchor] pairs (anchor '' when none).
export function refs(text: string, from: string): [string, string][] {
  const out = new Map<string, [string, string]>();
  for (const [, t] of text.matchAll(/\]\(([^)\s]+)\)/g)) {
    if (/^[a-z]+:|^#/.test(t)) continue;
    const [f, a = ''] = t.split('#');
    const p = normalize(join(dirname(from), f));
    out.set(`${p}#${a}`, [p, a]);
  }
  return [...out.values()];
}
export const links = (text: string, from: string) => [...new Set(refs(text, from).map(([p]) => p))];

const slug = (h: string) => h.toLowerCase().replace(/[^\p{L}\p{N} -]/gu, '').trim().replace(/ /g, '-');

// The part of a Markdown text under the heading named (or slugged) `h`, up to the next heading of the same or a
// higher level; the whole text when no heading matches.
export function section(text: string, h: string): string {
  const lines = text.split('\n');
  const i = lines.findIndex(l => /^#+ /.test(l) && (l.replace(/^#+ /, '') === h || slug(l.replace(/^#+ /, '')) === h));
  if (i < 0) return text;
  const level = lines[i].match(/^#+/)![0].length;
  const j = lines.findIndex((l, k) => k > i && /^#+ /.test(l) && l.match(/^#+/)![0].length <= level);
  return lines.slice(i, j < 0 ? undefined : j).join('\n');
}

const GROUPS = ['Canvas', 'Blocks', 'Controls', 'Patterns'] as const;
const group = (p: string) =>
  p.endsWith('.bl') ? 'Canvas' : p.startsWith(`${ds}/controls/`) ? 'Controls' : p.startsWith(`${ds}/patterns/`) ? 'Patterns' : null;

export function context(page: string): string {
  const spec = `${ds}/pages/${page}.md`;
  const owner = read(`${ds}/owner-rules.md`);
  const byId = rules(owner, existsSync(join(root, ds, 'principles.md')) ? read(`${ds}/principles.md`) : '');
  const text = read(spec);
  const parts = new Map<string, string[]>();
  const why = new Map<string, Set<string>>(); // rule ID -> the files that cite it
  const cite = (t: string, f: string) => { for (const id of cited(t)) (why.get(id) ?? why.set(id, new Set()).get(id)!).add(f); };
  cite(text, 'page');
  const add = (g: string, item: string) => { const l = parts.get(g) ?? parts.set(g, []).get(g)!; if (!l.includes(item)) l.push(item); };
  // Blocks are the links in the spec's `## Blocks` section; a link with an anchor names one section of a control
  // spec, and only that section is read. Each block's own controls and patterns follow.
  const blocks = (/^## Blocks$/m.test(text) ? refs(section(text, 'Blocks'), spec) : []).filter(([p]) => group(p) === 'Controls' || group(p) === 'Patterns');
  const queue: string[] = [];
  for (const [p, a] of blocks) {
    if (!existsSync(join(root, p))) continue;
    const t = a ? section(read(p), a) : read(p), name = relative(ds, p) + (a ? `#${a}` : '');
    add('Blocks', `${p}${a ? `#${a}` : ''}`);
    cite(t, name);
    queue.push(...links(t, p));
  }
  queue.unshift(...links(text, spec));
  const blockFiles = new Set(blocks.map(([p]) => p)); // a block's file is read as the block, never again whole
  for (const p of queue) {
    const g = group(p);
    if (!g || blockFiles.has(p) || !existsSync(join(root, p))) continue;
    add(g, p);
    if (g !== 'Canvas') cite(read(p), relative(ds, p));
  }
  const L = [`# UI context: ${page}`, '', `Read first: ${spec}`, ''];
  for (const g of GROUPS) {
    const ps = parts.get(g);
    if (!ps) continue;
    L.push(`${g}:`, ...ps.map(p => `- ${p}`));
    if (g === 'Canvas') L.push('  Read the canvas as plain text with your file tools; never call the Brilliant MCP tools from a task worktree; a visible change is drawn first in ~/Projects/fleet-board and approved by the owner (OR-62, README.md#design-board).');
    L.push('');
  }
  // Data (docs/designs/fleet-data-layer.md): how this page gets its data, so a change keeps it instant.
  L.push('Data (docs/designs/fleet-data-layer.md):',
    '- Read the screen through one view operation with useView (contract `view: true`, composed on the server from summaries); add fields to the view, not new reads.',
    '- No `every` timers and no reads on focus or visibility: the change stream rereads what it touches; a hidden tab reads nothing.',
    '- Prove it: `node scripts/page-budget.ts <path>` on the deployed site (open, reload, Alt-Tab, Back) and quote the numbers in the pull request.', '');
  L.push('Principles (owner-rules.md):', ...principles(owner).map((p, i) => `${i + 1}. ${p}`), '');
  const ids = [...why.keys()].sort((a, b) => a.localeCompare(b, 'en', { numeric: true }));
  L.push(`Rules cited (${ids.length}):`);
  for (const id of ids) L.push(`- **${id}** ${byId.get(id) ?? '(no rule with this ID)'} _(${[...why.get(id)!].join(', ')})_`);
  L.push('', 'Build from `packages/ui` and the book (OR-13); compare the app at 1440 and 390 px with the canvas; `pnpm run check:design` shows the design report.');
  return L.join('\n');
}

if (process.argv[1] === import.meta.filename) {
  const page = process.argv[2];
  const pages = readdirSync(join(root, ds, 'pages')).filter(f => f.endsWith('.md') && f !== 'README.md').map(f => f.slice(0, -3));
  console.log(page && pages.includes(page) ? context(page) : `Usage: node scripts/ui-context.ts <page>\nPages: ${pages.join(', ')}`);
}
