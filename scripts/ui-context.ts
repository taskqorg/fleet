// UI context (docs/design-system/README.md, "Context for a UI task"): the minimal reading an agent needs before it
// designs or builds one page. Reads the page spec, the blocks, controls and patterns it links, its board canvas,
// and the text of every rule they cite by ID (OR-<n> in owner-rules.md, UR-<n> in principles.md). Prints Markdown;
// never fails a build. Node standard library only.
// Usage: node scripts/ui-context.ts <page>   (a page spec name such as home; without one it lists the pages)
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { dirname, join, normalize, relative } from 'node:path';

const root = join(import.meta.dirname, '..');
const ds = 'docs/design-system';
const read = (p: string) => readFileSync(join(root, p), 'utf8');

// Rule texts by ID. Owner rules are table rows `| **OR-<n> Name** | text |`; usability rules open with a bold
// `**UR-<n> ...**` lead and run to the end of the line.
export function rules(owner: string, usability: string): Map<string, string> {
  const m = new Map<string, string>();
  for (const [, id, name, text] of owner.matchAll(/^\| \*\*(OR-\d+) ([^*]+)\*\* \| (.+?) \|\s*$/gm)) m.set(id, `${name}: ${text}`);
  for (const [, id, rest] of usability.matchAll(/^\s*(?:[*-] )?\*\*(UR-\d+)[ .:]*([^\n]*)$/gm)) m.set(id, rest.replace(/\*\*/g, '').trim());
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

// Repository paths of the relative Markdown links in the file at `from`, without anchors.
export function links(text: string, from: string): string[] {
  const out = new Set<string>();
  for (const [, t] of text.matchAll(/\]\(([^)\s]+)\)/g)) {
    if (/^[a-z]+:|^#/.test(t)) continue;
    out.add(normalize(join(dirname(from), t.split('#')[0])));
  }
  return [...out];
}

const GROUPS = ['Canvas', 'Blocks', 'Controls', 'Patterns'] as const;
const group = (p: string) =>
  p.endsWith('.bl') ? 'Canvas' : p.startsWith(`${ds}/blocks/`) ? 'Blocks' : p.startsWith(`${ds}/controls/`) ? 'Controls'
    : p.startsWith(`${ds}/patterns/`) ? 'Patterns' : null;

export function context(page: string): string {
  const spec = `${ds}/pages/${page}.md`;
  const owner = read(`${ds}/owner-rules.md`);
  const byId = rules(owner, existsSync(join(root, ds, 'principles.md')) ? read(`${ds}/principles.md`) : '');
  const text = read(spec);
  const parts = new Map<string, string[]>();
  const why = new Map<string, Set<string>>(); // rule ID -> the files that cite it
  const cite = (t: string, f: string) => { for (const id of cited(t)) (why.get(id) ?? why.set(id, new Set()).get(id)!).add(f); };
  cite(text, 'page');
  // The spec's own links, then the controls and patterns its blocks link (a page is assembled from blocks).
  const queue = links(text, spec), seen = new Set<string>();
  while (queue.length) {
    const p = queue.shift()!, g = group(p);
    if (!g || seen.has(p) || !existsSync(join(root, p))) continue;
    seen.add(p);
    (parts.get(g) ?? parts.set(g, []).get(g)!).push(p);
    if (g === 'Canvas') continue;
    const t = read(p);
    cite(t, relative(ds, p));
    if (g === 'Blocks') queue.push(...links(t, p).filter(q => group(q) === 'Controls' || group(q) === 'Patterns'));
  }
  const L = [`# UI context: ${page}`, '', `Read first: ${spec}`, ''];
  for (const g of GROUPS) {
    const ps = parts.get(g);
    if (!ps) continue;
    L.push(`${g}:`, ...ps.map(p => `- ${p}`));
    if (g === 'Canvas') L.push(`  Brilliant MCP: \`init\` with ${join(root, ds, 'board')}, then \`read\` with paths ${ps.map(p => relative(`${ds}/board`, p)).join(', ')}.`);
    L.push('');
  }
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
