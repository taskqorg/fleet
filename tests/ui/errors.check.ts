// Requirement: fleet-error-tracking.md: an error thrown in the built app and a render crash reach Bugsink as Sentry
// envelopes through Fleet's own tunnel POST /api/errors, for the configured web DSN, with the person as the event's user
// (set by the coordinator), the script's debug ID for source maps, and the render boundary still showing a recoverable
// error. Risk: browser errors never reach the error tracker, go to another project, or cannot be source-mapped.
// A fake Bugsink stands behind the coordinator's FLEET_BUGSINK_INGEST. Run: pnpm run build && node tests/ui/errors.check.ts
import assert from 'node:assert/strict';
import http from 'node:http';
import { readFileSync } from 'node:fs';
import { chromium } from 'playwright';
import { checkServer, OWNER } from './seed.ts';

const dsn: string = JSON.parse(readFileSync(new URL('../../config/bugsink.json', import.meta.url), 'utf8')).web;
assert.ok(dsn, 'config/bugsink.json names the web DSN the app is built with');
const project = new URL(dsn).pathname.split('/').pop();

type Envelope = { path: string; header: any; items: { header: any; payload: any }[] };
const got: Envelope[] = [];
const fake = http.createServer((req, res) => {
  const chunks: Buffer[] = [];
  req.on('data', c => chunks.push(c));
  req.on('end', () => {
    const lines = Buffer.concat(chunks).toString('utf8').split('\n');
    const items: Envelope['items'] = [];
    for (let i = 1; i + 1 < lines.length; i += 2) { if (lines[i]) items.push({ header: JSON.parse(lines[i]), payload: (() => { try { return JSON.parse(lines[i + 1]); } catch { return lines[i + 1]; } })() }); }
    got.push({ path: req.url ?? '', header: JSON.parse(lines[0]), items });
    res.writeHead(200, { 'content-type': 'application/json' }); res.end('{"id":"x"}');
  });
});
await new Promise<void>(r => fake.listen(0, '127.0.0.1', r));
const ingest = `http://127.0.0.1:${(fake.address() as any).port}`;

const events = () => got.flatMap(e => e.items.filter(i => i.header.type === 'event').map(i => ({ envelope: e, event: i.payload })));
async function event(match: (ev: any) => boolean, what: string) {
  for (let i = 0; i < 100; i++) {
    const hit = events().find(e => match(e.event));
    if (hit) return hit;
    await new Promise(r => setTimeout(r, 100));
  }
  assert.fail(`${what}: no envelope reached the fake Bugsink (${got.length} received)`);
}

const server = await checkServer({ env: { FLEET_BUGSINK_WEB_DSN: dsn, FLEET_BUGSINK_INGEST: ingest } });
const browser = await chromium.launch(process.env.FLEET_CHROMIUM ? { executablePath: process.env.FLEET_CHROMIUM } : {});
try {
  const context = await browser.newContext(), page = await context.newPage();
  const tunnelled: string[] = [];
  page.on('request', r => { if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/errors') tunnelled.push(new URL(r.url()).origin); });
  await page.goto(server.origin + '/tasks');
  await page.locator('header.page-h, .tr-body, [data-ui]').first().waitFor();
  await page.evaluate(() => { setTimeout(() => { throw new Error('bugsink browser check'); }); });
  const thrown = await event(ev => ev.exception?.values?.some((v: any) => v.value === 'bugsink browser check'), 'thrown error');
  assert.equal(thrown.envelope.path, `/api/${project}/envelope/`, 'forwarded to the configured project');
  assert.equal(thrown.envelope.header.dsn, dsn, 'the envelope names the configured DSN');
  assert.deepEqual(thrown.event.user, { id: OWNER }, 'the coordinator sets the person, id only');
  assert.equal(thrown.event.tags?.['fleet.actor'], OWNER);
  assert.equal(thrown.event.environment, 'production');
  const frames = thrown.event.exception.values.at(-1).stacktrace?.frames ?? [];
  assert.ok(frames.some((f: any) => /\/assets\/main-[^/]+\.js$/.test(f.filename ?? '')), 'the error has a frame in the app script');
  assert.ok(thrown.event.debug_meta?.images?.some((i: any) => i.type === 'sourcemap' && /^[0-9a-f-]{36}$/.test(i.debug_id)), 'the script carries its debug ID for source maps');
  assert.ok(tunnelled.length > 0 && tunnelled.every(o => o === server.origin), 'the SDK posts only to the app origin');

  // A render crash: Overview receives a reply it cannot render, shows a recoverable error and reports it.
  await page.route('**/api/ops/questions_list', route => route.fulfill({ json: { questions: null } }));
  await page.goto(server.origin + '/overview');
  await page.getByText('This page failed to render.', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Reload' }).waitFor();
  await event(ev => ev.tags?.source === 'render', 'render crash');
  await context.close();
  console.log('ok browser errors reach Bugsink through /api/errors (thrown, render crash, person, debug ID)');
} finally { await browser.close(); await server.close(); fake.close(); }
