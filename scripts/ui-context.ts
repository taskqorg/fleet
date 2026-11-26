// UI context (docs/design-system/README.md, "Context for a UI task"): the minimal reading an agent needs before it
// designs or builds one page. Reads the page spec, its blocks (the links in its `## Blocks` section), the controls and patterns it and they link, its board canvas,
// and the text of every judgment rule they cite by ID, after the owner's principles card (OR-<n> in owner-rules.md, UR-<n> in principles.md);
// a rule a component spec or a check keeps gets a one-line pointer to its home instead. Prints Markdown;
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

// Rules that left the rule lists, by ID: the rows of the `Rules kept by a component or a check` table in
// owner-rules.md and the `Rules kept elsewhere` table in principles.md, `| <ids> | <fate> | <where> |`. Each answers
// its name (owner rules), fate, where it lives (a repository path for a component, the check, or the rule it merged
// into) and, for a merge, that rule's ID.
export type Home = { name: string; fate: string; where: string; into?: string };
export function homes(owner: string, usability: string): Map<string, Home> {
  const m = new Map<string, Home>();
  const rows = (text: string, h: string) => {
    if (!text.split('\n').includes(`## ${h}`)) return [];
    return [...section(text, h).matchAll(/^\| ((?:OR|UR)-\d+[^|]*) \| (component|check|merged) \| (.+?) \|\s*$/gm)];
  };
  for (const [, ids, fate, where] of [...rows(owner, 'Rules kept by a component or a check'), ...rows(usability, 'Rules kept elsewhere')]) {
    const link = where.match(/^\[[^\]]*\]\(([^)#]+)(#[^)]*)?\)/);
    const into = where.match(/^into \[?((?:OR|UR)-\d+)/)?.[1];
    const at = fate === 'component' && link ? `${ds}/${link[1]}${link[2] ?? ''}` : fate === 'merged' ? `merged into ${into}` : where.split('. The owner')[0];
    for (const part of ids.split(/, (?=(?:OR|UR)-\d)/)) {
      const [, id, name = ''] = part.match(/^((?:OR|UR)-\d+) ?(.*)$/)!;
      m.set(id, { name: name.trim(), fate, where: at, ...(into ? { into } : {}) });
    }
  }
  return m;
}

// The principles card: the `## Principles card` section of owner-rules.md (the three principles and the standing
// principles), without the board and Target notes that follow it. Printed first for every page.
export function card(owner: string): string {
  const sec = section(owner, 'Principles card');
  if (sec === owner) return '';
  return sec.split(/\n\nThe principles, the rail/)[0].replace(/^## Principles card/, '## Principles card (owner-rules.md, mandatory)').trim();
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

// Committed frames of the given canvases from board/index.json (pnpm run board:index), one line each:
// `- <id> <name> (<width> px[, <status>])`; the status is left out when the name already states it.
export type Frame = { id: string; canvas: string; name: string; width: number | null; frameWidth: number | null; status: string | null };
export function frames(index: { frames: Frame[] } | null, canvases: string[]): string[] {
  const want = new Set(canvases.map(p => relative(`${ds}/board`, p).replace(/\.bl$/, '')));
  return (index?.frames ?? []).filter(f => want.has(f.canvas)).map(f => {
    const w = f.width ?? f.frameWidth, st = f.status && !f.name.includes(f.status) ? f.status : '';
    const tail = [w ? `${w} px` : '', st].filter(Boolean).join(', ');
    return `- ${f.id} ${f.name}${tail ? ` (${tail})` : ''}`;
  });
}

const GROUPS = ['Canvas', 'Blocks', 'Controls', 'Patterns'] as const;
const group = (p: string) =>
  p.endsWith('.bl') ? 'Canvas' : p.startsWith(`${ds}/controls/`) ? 'Controls' : p.startsWith(`${ds}/patterns/`) ? 'Patterns' : null;

export function context(page: string): string {
  const spec = `${ds}/pages/${page}.md`;
  const owner = read(`${ds}/owner-rules.md`);
  const usability = existsSync(join(root, ds, 'principles.md')) ? read(`${ds}/principles.md`) : '';
  const byId = rules(owner, usability), home = homes(owner, usability);
  const text = read(spec);
  const index = existsSync(join(root, ds, 'board/index.json')) ? JSON.parse(read(`${ds}/board/index.json`)) : null;
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
  const L = [`# UI context: ${page}`, '', card(owner), '', `Read next: ${spec}`, ''];
  for (const g of GROUPS) {
    const ps = parts.get(g);
    if (!ps) continue;
    L.push(`${g}:`, ...ps.map(p => `- ${p}`));
    if (g === 'Canvas') {
      L.push('  Read the canvas as plain text with your file tools; never call the Brilliant MCP tools from a task worktree; a visible change is drawn first on a drafts/<task>/ copy in ~/Projects/fleet-board (never on its tracked canvases) and approved by the owner, then copied into your worktree (OR-62, README.md#design-board).');
      const fs = frames(index, ps);
      if (fs.length) L.push('', 'Frames (board/index.json):', ...fs);
      L.push('', 'Before you build: frame vs prod data (board frames carry illustrative data):',
        '- Walk the page on prod as site-agents (AGENTS.md, "Opening the deployed web app").',
        '- Mark each difference between the frame and prod: code (the UI lacks it), data (the UI is right, prod data differs) or missing field (the model has no such field).',
        '- Put that list in the pull request description.');
    }
    L.push('');
  }
  // Data (docs/designs/fleet-data-layer.md): how this page gets its data, so a change keeps it instant.
  L.push('Data (docs/designs/fleet-data-layer.md):',
    '- Read the screen through one view operation with useView (contract `view: true`, composed on the server from summaries); add fields to the view, not new reads.',
    '- No `every` timers and no reads on focus or visibility: the change stream rereads what it touches; a hidden tab reads nothing.',
    '- Prove it: `node scripts/page-budget.ts <path>` on the deployed site (open, reload, Alt-Tab, Back) and quote the numbers in the pull request.', '');
  // A merged rule brings the rule it merged into.
  for (const [id, fs] of [...why]) { const into = home.get(id)?.into; if (into) for (const f of fs) cite(into, f); }
  const ids = [...why.keys()].sort((a, b) => a.localeCompare(b, 'en', { numeric: true }));
  const judged = ids.filter(id => byId.has(id) || !home.has(id)), kept = ids.filter(id => !byId.has(id) && home.has(id));
  L.push(`Rules cited, judgment (${judged.length}):`);
  for (const id of judged) L.push(`- **${id}** ${byId.get(id) ?? '(no rule with this ID)'} _(${[...why.get(id)!].join(', ')})_`);
  // One line per home: a component spec, a check, or the rules merged into another; a spec already listed above says so.
  const listed = new Set([...parts.values()].flat().filter(p => !p.includes('#'))); // a block read as one section is not the whole spec
  const byHome = new Map<string, string[]>();
  for (const id of kept) {
    const h = home.get(id)!, key = h.fate === 'merged' ? 'merged' : `${h.fate}: ${h.where}`;
    (byHome.get(key) ?? byHome.set(key, []).get(key)!).push(h.fate === 'merged' ? `${id} into ${h.into}` : `${id}${h.name ? ` ${h.name}` : ''}`);
  }
  L.push('', `Rules cited, kept by a component or a check (${kept.length}); read them there:`);
  for (const [key, list] of byHome) L.push(`- ${list.join(', ')} → ${key}${listed.has(key.replace(/^component: /, '').split('#')[0]) ? ' (listed above)' : ''}`);
  L.push('', 'Build from `packages/ui` and the book (OR-13); compare the app at 1440 and 390 px with the canvas; `pnpm run check:design` shows the design report.');
  return L.join('\n');
}

if (process.argv[1] === import.meta.filename) {
  const page = process.argv[2];
  const pages = readdirSync(join(root, ds, 'pages')).filter(f => f.endsWith('.md') && f !== 'README.md').map(f => f.slice(0, -3));
  console.log(page && pages.includes(page) ? context(page) : `${card(read(`${ds}/owner-rules.md`))}\n\nUsage: node scripts/ui-context.ts <page>\nPages: ${pages.join(', ')}`);
}
