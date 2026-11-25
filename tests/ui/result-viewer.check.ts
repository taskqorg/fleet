// One result in the Projects Results facet (FLT-39; docs/design-system/controls/result-viewer.md, canvas
// board/screens/projects.bl "RESULT VIEWER"): an earlier link by UUID lands on the result's code, the head names it
// without the UUID or hash its writer put in the file name, and the body picks the viewer by kind: key/value for JSON
// lines with a Source switch, markdown, text, table, diff and download-only. Checked at 1440 and 390 px. Not part of CI;
// run after `pnpm run build`:
//   node tests/ui/result-viewer.check.ts           (FLEET_BROWSER_SHOTS=<dir> keeps screenshots)
import { mkdirSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { chromium, type Page } from 'playwright';
import { checkServer } from './seed.ts';

const shots = process.env.FLEET_BROWSER_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });
const { origin, seed, close } = await checkServer({ store: true });
const call = async (name: string, body: object) => {
  const r = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
  if (r.status !== 200) throw Error(`${name}: ${r.status} ${await r.text()}`);
  return r.json() as Promise<any>;
};
await call('project_create', { id: 'fleet', requestId: 'rv-fleet', content: { settings: { id: 'fleet', name: 'fleet', contextFiles: ['rules.md'] }, contextFiles: [{ path: 'rules.md', text: 'Rules.' }] } });
await call('project_code_set', { projectId: 'fleet', code: 'FLT' });
const task = seed.task({ title: 'Resume after restart', projectId: 'fleet', state: 'completed' });
seed.execution({ id: 'x-rv', projectId: 'fleet', taskId: task, hostId: 'pds18', state: 'completed', startedAt: Date.now() - 600e3, finishedAt: Date.now() - 500e3, harness: 'claude' });

const UUID = randomUUID();
const RECORD = '{"phase":"socket","provenance":"runtime-sidecar-observation","returnedError":null,"status":{"outcome":"unknown","provenance":"status-unavailable","registration":"unknown","stage":"unknown"}}\n';
const kinds = {
  jsonl: { name: `mcp-lifecycle-${'5e'.repeat(32)}-${UUID}.jsonl`, mediaType: 'application/x-ndjson', bytes: RECORD },
  md: { name: 'T-FLT-62-resumption-def7bd70.md', mediaType: 'text/markdown', bytes: '# Resumption\n\nThe session resumed after the host restart.\n' },
  text: { name: 'worker.log', mediaType: 'text/plain', bytes: '03:43:31 worker started\n03:44:06 worker done, exit 0\n' },
  csv: { name: 'lab-run-summary.csv', mediaType: 'text/csv', bytes: 'model,pass\nopus,42/45\nsonnet,39/45\n' },
  diff: { name: 'flt-39.patch', mediaType: 'text/x-diff', bytes: 'diff --git a/a.ts b/a.ts\n--- a/a.ts\n+++ b/a.ts\n@@ -1,2 +1,2 @@\n keep\n-old\n+new\ndiff --git a/b.ts b/b.ts\n--- a/b.ts\n+++ b/b.ts\n@@ -5 +5 @@\n-x\n+y\n' },
  zip: { name: 'bundle.zip', mediaType: 'application/zip', bytes: 'PK\u0003\u0004' },
};
const ids: Record<string, string> = {};
for (const [k, v] of Object.entries(kinds)) {
  ids[k] = k === 'jsonl' ? UUID : randomUUID();
  seed.artifact({ id: ids[k], executionId: 'x-rv', name: v.name, mediaType: v.mediaType, projectId: 'fleet', taskId: task, bytes: Buffer.from(v.bytes) });
}

const browser = await chromium.launch({ headless: true });
const failures: string[] = [];
const check = (ok: boolean, what: string) => { console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) failures.push(what); };
const shot = async (p: Page, name: string) => { if (shots) await p.screenshot({ path: `${shots}/${name}.png` }); };
const viewer = (p: Page) => p.locator('[data-ui=result-viewer]');
// Opens a result by its earlier UUID link and waits for the facet route with its code.
const open = async (p: Page, kind: string) => {
  await p.goto(`${origin}/results/${ids[kind]}`);
  const end = Date.now() + 10_000;
  while (Date.now() < end && !/^\/projects\/FLT\/results\/R-FLT-\d+$/.test(new URL(p.url()).pathname)) await p.waitForTimeout(100);
  await viewer(p).waitFor();
  return new URL(p.url()).pathname;
};
const errors: string[] = [];
try {
  for (const [width, height, tag] of [[1440, 900, '1440'], [390, 844, '390']] as const) {
    const p = await browser.newPage({ viewport: { width, height }, colorScheme: 'dark', ...(width < 768 ? { isMobile: true, hasTouch: true } : {}) });
    p.on('pageerror', e => errors.push(e.message));

    const at = await open(p, 'jsonl');
    check(/^\/projects\/FLT\/results\/R-FLT-\d+$/.test(at), `${tag}: a UUID link lands on the code route: ${at}`);
    await p.locator('[data-ui=kv-view]').waitFor();
    const head = await viewer(p).locator('.rv-h').innerText();
    check(head.includes('mcp-lifecycle.jsonl') && head.includes('JSON lines') && /R-FLT-\d+/.test(head), `${tag}: the head names the result, its code and kind: ${head.replace(/\s+/g, ' ')}`);
    const page = await p.locator('main, body').first().innerText();
    check(!page.includes(UUID) && !page.includes('5e5e5e5e5e5e'), `${tag}: no UUID or hash anywhere on the page`);
    check(await p.locator('.kv-row', { hasText: 'returnedError' }).locator('.kv-v.null').innerText() === 'none', `${tag}: null reads as none`);
    check(await p.locator('.kv-branch > summary', { hasText: 'status' }).isVisible(), `${tag}: the nested object folds under its key`);
    check(await p.evaluate(() => document.scrollingElement!.scrollWidth <= innerWidth), `${tag}: nothing scrolls sideways`);
    await shot(p, `kv-${tag}`);
    await p.getByRole('button', { name: 'Source', exact: true }).click();
    await p.locator('.av-text pre').waitFor();
    check((await p.locator('.av-text pre').innerText()).includes('"returnedError":null'), `${tag}: Source shows the stored text`);
    await shot(p, `raw-${tag}`);
    await p.getByRole('button', { name: 'Preview', exact: true }).click();

    await open(p, 'md');
    await p.locator('.av .md .md-h', { hasText: 'Resumption' }).waitFor();
    check((await viewer(p).locator('.rv-h .nm').innerText()) === 'T-FLT-62-resumption-def7bd70.md', `${tag}: markdown renders; a short hash stays in its name`);
    await open(p, 'text');
    await p.locator('.av-text pre', { hasText: 'worker done' }).waitFor();
    check(true, `${tag}: text shows as is`);
    await open(p, 'csv');
    await p.locator('.av-table th', { hasText: 'model' }).waitFor();
    check(await p.locator('.av-table tbody tr').count() === 2, `${tag}: CSV is a table with its header`);
    await shot(p, `table-${tag}`);
    await open(p, 'diff');
    await p.locator('[data-ui=diff-view]').waitFor();
    check(await p.locator('.diff-lines tr.add .sr-only', { hasText: 'added' }).count() === 1, `${tag}: an added line is named in words`);
    if (width < 768) {
      check(!(await p.locator('.diff-files').isVisible()) && (await p.locator('.diff-step').innerText()).includes('1 of 2'), `${tag}: the file list becomes a step through files`);
      await p.getByRole('button', { name: 'Next file' }).click();
      await p.locator('.diff-path', { hasText: 'b.ts' }).waitFor();
    } else {
      await p.locator('.diff-file', { hasText: 'b.ts' }).click();
      await p.locator('.diff-path', { hasText: 'b.ts' }).waitFor();
      check(await p.locator('.diff-lines td.ln').first().isVisible(), `${tag}: line numbers show`);
    }
    await shot(p, `diff-${tag}`);
    await open(p, 'zip');
    check(await viewer(p).getByText('has no view here').isVisible(), `${tag}: an archive is download only`);
    await shot(p, `file-${tag}`);
    await p.close();
  }
  check(errors.length === 0, 'no page errors' + (errors.length ? ': ' + errors.join(' | ') : ''));
} finally {
  await browser.close(); await close();
}
if (failures.length) { console.log(`${failures.length} failed`); process.exit(1); }
