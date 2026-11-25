// J1–J5 walk on the deployed app (docs/designs/fleet-ia-and-journeys.md §3, §4 item 4). Information only: it prints a short
// Markdown report on stdout and exits 0 whatever it finds. It never writes: it stops before Launch, Promote, task creation
// and every other action that changes state, and says so.
// Opens https://fleet.ufobe.com as site-agents (AGENTS.md "Opening the deployed web app"): the Access service token comes
// from CF_ACCESS_CLIENT_ID/CF_ACCESS_CLIENT_SECRET or ~/.config/ufobe-agents/site-access.env, is set only on a browser
// context that opens this origin, and is never printed.
// Run: node tests/journeys/walk.ts [origin] [WIDTHxHEIGHT], 1440x900 by default (390x844 for the phone).
import { existsSync, readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { chromium, type Page } from 'playwright';
import { guardOutput, prodContext, redact } from '../../scripts/prod-browser.ts';

guardOutput();

const origin = new URL(process.argv[2] ?? 'https://fleet.ufobe.com').origin;
const size = /^(\d+)x(\d+)$/.exec(process.argv[3] ?? '1440x900') ?? (console.error('viewport must be WIDTHxHEIGHT; using 1440x900'), ['', '1440', '900']);
const [width, height] = [Number(size[1]), Number(size[2])];

function access(): { id: string; secret: string } | null {
  let id = process.env.CF_ACCESS_CLIENT_ID, secret = process.env.CF_ACCESS_CLIENT_SECRET;
  const file = join(homedir(), '.config/ufobe-agents/site-access.env');
  if ((!id || !secret) && existsSync(file)) {
    const env = Object.fromEntries(readFileSync(file, 'utf8').split('\n').map(l => l.replace(/^\s*export\s+/, '').match(/^\s*([A-Z_]+)\s*=\s*(.*?)\s*$/)).filter(m => m !== null).map(m => [m[1], m[2].replace(/^(['"])(.*)\1$/, '$2')]));
    id ??= env.CF_ACCESS_CLIENT_ID; secret ??= env.CF_ACCESS_CLIENT_SECRET;
  }
  return id && secret ? { id, secret } : null;
}

// A step either passes (optionally with a note), records a gap and goes on, or stops the journey.
class Stop extends Error { kind: 'missing' | 'broken' | 'write'; constructor(msg: string, kind: 'missing' | 'broken' | 'write') { super(msg); this.kind = kind; } }
const missing = (screen: string) => { throw new Stop(`${screen} is not built yet`, 'missing'); };
const readOnly = (action: string) => { throw new Stop(`${action} not pressed: the walk never writes`, 'write'); };
type Ctx = { page: Page; gap: (msg: string) => void; clicks: () => number; click: (l: ReturnType<Page['locator']>) => Promise<void> };
type Step = [name: string, run: (c: Ctx) => Promise<string | void>];
type Journey = { id: string; title: string; steps: Step[] };

const T = 15000;
// The phone keeps desktop-only parts in the DOM, hidden; only a shown element counts.
const visible = async (l: ReturnType<Page['locator']>, ms = T) => { try { await l.filter({ visible: true }).first().waitFor({ state: 'visible', timeout: ms }); return true; } catch { return false; } };
const open = async (page: Page, path: string) => { await page.goto(origin + path, { waitUntil: 'domcontentloaded' }); };
const RAW_ID = /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b(?=[0-9a-f]*\d)(?=[0-9a-f]*[a-f])[0-9a-f]{12,40}\b/;

// A CatalogueTree item by its label; expanding a folder clicks its disclosure button.
const treeItem = (page: Page, label: string) => page.locator('[role=treeitem]').filter({ has: page.locator('.cat-label').getByText(label, { exact: true }) });
async function expand(c: Ctx, label: string) {
  const item = treeItem(c.page, label);
  if (await item.first().getAttribute('aria-expanded') === 'false') await c.click(item.locator('button.cat-tw'));
}
async function labCollection(c: Ctx, name: string) {
  await open(c.page, '/lab');
  if (!await visible(c.page.locator('[data-ui=catalogue-tree]'))) missing('Lab catalogue tree');
  if (!await visible(treeItem(c.page, 'Use cases'))) missing('Lab › Use cases');
  await expand(c, 'Use cases');
  if (!await visible(treeItem(c.page, name), 5000)) missing(`collection "${name}" in Lab › Use cases`);
  await c.click(treeItem(c.page, name).locator('.cat-label'));
}
const taskRows = (page: Page) => page.locator('[data-ui=project-work] .vl-row');
const leaderboard = (page: Page) => page.locator('section[aria-label^="Suite leaderboard"]');

const JOURNEYS: Journey[] = [
  { id: 'J1', title: 'Best model/method at a glance (< 5 s)', steps: [
    ['Open Lab and pick the LLM Code Benchmarks collection', async c => { await labCollection(c, 'LLM Code Benchmarks'); }],
    ['Comparison matrix shows', async c => {
      if (!await visible(leaderboard(c.page))) missing('collection leaderboard');
      if (!await visible(leaderboard(c.page).locator('tbody tr'))) throw new Stop('leaderboard has no rows', 'broken');
      const rows = await leaderboard(c.page).locator('tbody tr').filter({ visible: true }).count();
      return `${rows} rows`;
    }],
    ['Cells: score, median wall time, failure rate; tokens/cost in Details', async c => {
      const head = (await leaderboard(c.page).innerText()).split('\n').slice(0, 40).join(' ');
      const want: [string, RegExp][] = [['score', /Score/i], ['median wall time', /Median/i], ['failure rate', /Fail/i]];
      const absent = want.filter(([, r]) => !r.test(head)).map(([n]) => n);
      // The target matrix folds tokens and cost into each cell's Details (pages/laboratory.md#target, OR-54, OR-59).
      if (!await leaderboard(c.page).locator('[data-tip*="tokens"]').count()) absent.push('tokens/cost');
      if (absent.length) c.gap(`no ${absent.join(', ')} in the comparison`);
    }],
    ['No raw commit hashes or UUIDs (OR-38)', async c => {
      const m = (await leaderboard(c.page).innerText()).match(RAW_ID);
      if (m) c.gap(`raw identifier shown: ${m[0].slice(0, 12)}…`);
    }],
  ] },
  { id: 'J2', title: 'Fast A/B or multi-step Lab run (≤ 3 clicks)', steps: [
    ['Open a use case from LLM Code Benchmarks', async c => {
      // Expanded, not selected: on the phone a selected collection replaces the tree with its leaderboard.
      await open(c.page, '/lab');
      if (!await visible(treeItem(c.page, 'Use cases'))) missing('Lab › Use cases');
      await expand(c, 'Use cases');
      if (!await visible(treeItem(c.page, 'LLM Code Benchmarks'), 5000)) missing('collection "LLM Code Benchmarks" in Lab › Use cases');
      await expand(c, 'LLM Code Benchmarks');
      const uc = c.page.locator('[role=treeitem]').filter({ hasText: /bench:/ });
      if (!await visible(uc)) missing('use case under LLM Code Benchmarks');
      const before = c.clicks(); await c.click(uc); return `use case after ${c.clicks() - before} more click`;
    }],
    ['Run in Lab', async c => {
      const b = c.page.getByRole('button', { name: 'Run in Lab', exact: true });
      if (!await visible(b)) missing('Run in Lab on the use-case card');
      const start = c.clicks(); await c.click(b);
      await c.page.waitForURL(/\/lab\/new/, { timeout: T }).catch(() => { throw new Stop('Run in Lab did not open the launcher', 'broken'); });
      return `launcher open; ${c.clicks() - start} click from the use case`;
    }],
    ['Variants picked with EntityPicker, no JSON', async c => {
      if (!await visible(c.page.locator('[data-ui=entity-picker]'))) missing('EntityPicker in the launcher');
      return `${await c.page.locator('[data-ui=entity-picker]').count()} pickers`;
    }],
    ['Launch', async c => {
      const launch = c.page.getByRole('button', { name: /^Launch/ });
      if (await visible(launch, 3000)) readOnly('Launch');
      const draft = c.page.getByRole('button', { name: /draft/i });
      if (await visible(draft, 3000)) { c.gap('the launcher creates a draft; Launch is a separate action on the draft (more than 3 clicks from a use case)'); readOnly(`"${(await draft.first().innerText()).trim()}"`); }
      missing('Launch in the launcher');
    }],
  ] },
  { id: 'J3', title: 'Promote winner to Recommended (1 action + scope)', steps: [
    ['Open a collection leaderboard', async c => { await labCollection(c, 'LLM Code Benchmarks'); if (!await visible(leaderboard(c.page))) missing('collection leaderboard'); }],
    ['Promote on a leaderboard row', async c => {
      const p = leaderboard(c.page).getByRole('button', { name: /Promote/ });
      if (!await visible(p, 5000)) missing('Promote on a leaderboard row');
      // The scope (project or system) is chosen with Promote, inside the leaderboard; the header's project focus does not count.
      if (!await visible(leaderboard(c.page).getByText(/\bscope\b|\bsystem\b/i), 1000)) c.gap('no scope choice (project or system) next to Promote');
      return 'Promote present (not pressed: the walk never writes)';
    }],
    ['Library › Methods shows lifecycle status', async c => {
      await open(c.page, '/library/methods');
      if (!await visible(c.page.locator('[data-ui=catalogue-tree]'))) missing('Library catalogue tree');
      if (!await visible(treeItem(c.page, 'Methods'), 5000)) missing('Library › Methods');
      await expand(c, 'Methods');
      // The first item under Methods is the row after its folder row in the virtual list.
      const at = Number(await treeItem(c.page, 'Methods').first().locator('xpath=..').getAttribute('data-index'));
      const first = c.page.locator(`.vl-row[data-index="${at + 1}"] [role=treeitem][aria-level="2"]`);
      if (!await visible(first, 3000)) missing('a method under Library › Methods');
      await c.click(first);
      await c.page.waitForLoadState('networkidle', { timeout: 3000 }).catch(() => {});
      const status = c.page.locator('[data-ui=detail]').getByText(/^(Recommended|Experimental|Deprecated)$/i);
      if (!await visible(status, 5000)) c.gap('no method shows a lifecycle status (experimental / recommended)');
    }],
  ] },
  { id: 'J4', title: 'Real project execution with full attribution', steps: [
    ['Tasks facet of the Fleet project', async c => {
      // Tasks are a facet of their project (pages/projects.md, Routes); `/projects/tasks` lists projects first.
      await open(c.page, '/projects/FLT/tasks');
      if (!await visible(taskRows(c.page).locator('.row-link'))) missing('Tasks facet of a project');
      if (!await visible(c.page.getByRole('button', { name: /New task/ }), 2000)) c.gap('no "New task" action on Tasks (tasks are created from a conversation)');
    }],
    ['Open a task that ran', async c => {
      // A row with recorded spend has run; otherwise the first task.
      const rows = taskRows(c.page).filter({ has: c.page.locator('.row-link') }), spent = rows.filter({ hasNotText: /Spend —/ });
      await c.click((await spent.count() ? spent : rows).locator('.row-link').filter({ visible: true }));
      if (!await visible(c.page.locator('[data-ui=detail] .detail-title'))) throw new Stop('task detail did not open', 'broken');
      await c.page.waitForLoadState('networkidle', { timeout: 3000 }).catch(() => {});
    }],
    ['Task shows executions, host, delivery stage, tokens/cost and time', async c => {
      const d = await c.page.locator('[data-ui=detail]').first().innerText();
      const want: [string, RegExp][] = [['executions', /Execution/i], ['host', /\bhost\b|\bpds18\b|owner-mac|\bwindows\b/i], ['delivery stage', /PR open|merged|released|installed|publishing|delivery/i], ['tokens or cost', /token|\$|cost/i], ['time', /\d+\s?(ms|s|min|m|h)\b/]];
      const absent = want.filter(([, r]) => !r.test(d)).map(([n]) => n);
      if (absent.length) c.gap(`task detail lacks ${absent.join(', ')}`);
    }],
    ['Spend attributes the work', async c => {
      await open(c.page, '/system/spend');
      if (!await visible(c.page.locator('[data-ui=data-list], [data-ui=data-table], table'))) missing('Spend breakdown');
    }],
  ] },
  { id: 'J5', title: 'Proactive environment & account compatibility', steps: [
    ['System host list', async c => {
      await open(c.page, '/system/hosts');
      if (!await visible(treeItem(c.page, 'Hosts'))) missing('System › Hosts');
      await expand(c, 'Hosts');
      if (!await visible(c.page.locator('[role=treeitem][aria-level="2"]'), 5000)) throw new Stop('no host under Hosts', 'broken');
    }],
    ['Open a host', async c => {
      await c.click(c.page.locator('[role=treeitem][aria-level="2"]'));
      if (!await visible(c.page.locator('[data-ui=detail]'))) throw new Stop('host detail did not open', 'broken');
      await c.page.waitForLoadState('networkidle', { timeout: 3000 }).catch(() => {});
    }],
    ['Probed models per bound account; incompatible profiles marked', async c => {
      const d = await c.page.locator('[data-ui=detail]').first().innerText();
      if (!/CAPABILITIES/i.test(d)) missing('host capabilities');
      if (!/account/i.test(d)) c.gap('the host lists harness capabilities, not models probed per bound account');
      if (!/incompatib|not supported|modelNotFound|rejects/i.test(d)) c.gap('no profile marked incompatible on this host (none may be)');
    }],
    ['Router skips (route decisions) visible', async () => missing('route-decision view (accounts skipped for modelNotFound)')],
  ] },
];

type Outcome = { id: string; title: string; status: string; lines: string[]; errors: string[]; failed: string[]; ms: number };

async function walk(page: Page, j: Journey): Promise<Outcome> {
  const errors: string[] = [], failed: string[] = [], lines: string[] = [], gaps: string[] = [];
  const onConsole = (m: { type(): string; text(): string }) => { if (m.type() === 'error') errors.push(redact(m.text()).slice(0, 160)); };
  const onPageError = (e: Error) => errors.push(redact(e).slice(0, 160));
  const onFailed = (r: { url(): string; failure(): { errorText: string } | null }) => { if (r.url().startsWith(origin) && !r.url().includes('/api/stream') && !r.failure()?.errorText.includes('ERR_ABORTED')) failed.push(`${r.failure()?.errorText} ${new URL(r.url()).pathname}`); };
  const onResponse = (r: { status(): number; url(): string }) => { if (r.status() >= 400) failed.push(`${r.status()} ${new URL(r.url()).pathname}`); };
  page.on('console', onConsole); page.on('pageerror', onPageError); page.on('requestfailed', onFailed); page.on('response', onResponse);
  let n = 0;
  const ctx: Ctx = { page, gap: m => gaps.push(m), clicks: () => n, click: async l => { n++; await l.first().click({ timeout: T }); } };
  const t0 = Date.now();
  let status = 'reached';
  for (const [i, [name, run]] of j.steps.entries()) {
    const before = gaps.length;
    try {
      const note = await run(ctx);
      lines.push(`- ok ${name}${note ? ` (${note})` : ''}${gaps.slice(before).map(g => `; gap: ${g}`).join('')}`);
    } catch (e) {
      const s = e instanceof Stop ? e : new Stop(redact(e), 'broken');
      lines.push(`- ${s.kind === 'write' ? 'stop' : 'no'} ${name}: ${s.message}${gaps.slice(before).map(g => `; gap: ${g}`).join('')}`);
      status = s.kind === 'write' ? (gaps.length ? `partly (read-only stop at ${name})` : `reached up to ${name} (read-only)`)
        : s.kind === 'missing' && i === 0 ? `not possible yet: ${s.message}` : `partly (stops at: ${name})`;
      break;
    }
  }
  if (status === 'reached' && gaps.length) status = `partly (${gaps.length} gap${gaps.length > 1 ? 's' : ''})`;
  page.off('console', onConsole); page.off('pageerror', onPageError); page.off('requestfailed', onFailed); page.off('response', onResponse);
  return { id: j.id, title: j.title, status, lines, errors, failed, ms: Date.now() - t0 };
}

const out: string[] = [`## J1–J5 walk on ${origin}`];
const cred = access();
if (!cred) { out.push('', 'Not run: no Access service token (CF_ACCESS_CLIENT_ID/CF_ACCESS_CLIENT_SECRET or ~/.config/ufobe-agents/site-access.env).'); console.log(out.join('\n')); process.exit(0); }
let browser: Awaited<ReturnType<typeof chromium.launch>> | undefined;
try {
  browser = await chromium.launch();
  // The token goes only to this origin: other hosts are not opened.
  const context = await prodContext(browser, cred, { viewport: { width, height } }, origin);
  const results: Outcome[] = [];
  let version = '';
  for (const j of JOURNEYS) {
    const page = await context.newPage();
    results.push(await walk(page, j));
    version ||= (await page.locator('body').innerText().catch(() => '')).match(/\b\d+\.\d{6}\.\d+\b/)?.[0] ?? '';
    await page.close();
  }
  out[0] += `${version ? ` (v${version})` : ''} at ${width} × ${height}, ${new Date().toISOString().slice(0, 16).replace('T', ' ')} UTC`;
  out.push('', 'Read-only walk of docs/designs/fleet-ia-and-journeys.md §3; information, never a gate.', '',
    '| Journey | Result | Console errors | Failed requests | Time |', '|---|---|---|---|---|',
    ...results.map(r => `| ${r.id} | ${r.status} | ${r.errors.length} | ${r.failed.length} | ${(r.ms / 1000).toFixed(1)} s |`));
  for (const r of results) {
    out.push('', `**${r.id} — ${r.title}:** ${r.status}`, ...r.lines);
    const uniq = (a: string[]) => [...new Set(a)].slice(0, 5);
    if (r.errors.length) out.push(`- console errors: ${uniq(r.errors).map(e => `\`${e}\``).join('; ')}`);
    if (r.failed.length) out.push(`- failed requests: ${uniq(r.failed).map(e => `\`${e}\``).join('; ')}`);
  }
} catch (e) {
  out.push('', `Walk stopped: ${redact(e)}`);
} finally {
  await browser?.close().catch(() => {});
}
console.log(out.join('\n'));
