// Outside tests of telemetry (docs/designs/fleet-telemetry.md) against a fake ClickHouse HTTP interface: span inserts,
// the clickhouse problem question, host span batches and the project scope of telemetry reads.
//   FLEET_TRIAL_TESTS=tests/go/telemetry.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {randomUUID} from 'node:crypto';
import {startCoordinator, type Coordinator} from '../harness.ts';

const OWNER = 'owner@trial.test', MEMBER = 'member@trial.test';
type Req = {query: string, params: URLSearchParams, body: string};

async function fakeClickHouse() {
  const seen: Req[] = [];
  let up = true;
  const srv = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      const params = new URL(req.url!, 'http://x').searchParams;
      const query = params.get('query') ?? body;
      seen.push({query, params, body});
      if (!up) { res.writeHead(503); res.end('down'); return; }
      res.writeHead(200); res.end('');
    });
  });
  await new Promise<void>((r) => srv.listen(0, '127.0.0.1', r));
  const port = (srv.address() as any).port;
  return {url: `http://127.0.0.1:${port}`, seen, set: (v: boolean) => { up = v; }, close: () => srv.close()};
}

async function call(c: Coordinator, path: string, body: unknown, o: {person?: string | null, bearer?: string} = {}) {
  const headers: Record<string, string> = {authorization: 'Bearer ' + (o.bearer ?? c.secret), 'content-type': 'application/json'};
  if (o.person !== null) headers['x-fleet-person'] = o.person ?? OWNER;
  const r = await fetch(c.base + path, {method: 'POST', headers, body: JSON.stringify(body)});
  const text = await r.text(); let out: any = text; try { out = JSON.parse(text); } catch {}
  return {status: r.status, body: out};
}

const until = async (f: () => Promise<boolean> | boolean, what: string) => {
  for (let i = 0; i < 100; i++) { if (await f()) return; await new Promise((r) => setTimeout(r, 100)); }
  assert.fail('timed out: ' + what);
};

test('T1 requests succeed with ClickHouse down; 3 failed flushes open one problem:telemetry:clickhouse question, the next good flush withdraws it (requirement: fleet-telemetry.md Goals 4, When the watcher is down; risk: telemetry blocks requests, or an outage goes unseen)', async () => {
  const ch = await fakeClickHouse();
  ch.set(false);
  const c = await startCoordinator({name: 'tele1', env: {FLEET_CLICKHOUSE_URL: ch.url}});
  const open = async () => (await call(c, '/api/ops/questions_list', {state: 'open'})).body.questions.filter((x: any) => x.cause === 'problem:telemetry:clickhouse' || /ClickHouse refuses/.test(x.text));
  try {
    for (let i = 0; i < 40; i++) {
      assert.equal((await call(c, '/api/ops/questions_list', {})).status, 200);
      if (i % 10 === 0) await new Promise((r) => setTimeout(r, 1100));
    }
    await until(async () => (await open()).length === 1, 'clickhouse question');
    const s = await call(c, '/api/ops/telemetry_summary', {});
    assert.equal(s.status, 200);
    assert.ok(s.body.droppedSpans > 0, JSON.stringify(s.body));
    ch.set(true);
    await call(c, '/api/ops/questions_list', {});
    await until(async () => (await open()).length === 0, 'question withdrawn');
    await until(() => ch.seen.some((r) => r.query.startsWith('INSERT INTO span') && r.body.includes('"telemetry.recovered"')), 'recovered span');
    const insert = ch.seen.find((r) => r.query.startsWith('INSERT INTO span') && r.body.includes('"questions_list"'));
    assert.ok(insert, "a server span named after the operation: " + ch.seen.filter((r) => r.query.startsWith("INSERT")).map((r) => r.body.slice(0, 400)).join("\n").slice(0, 1500));
    const row = JSON.parse(insert!.body.split('\n').find((l) => l.includes('"questions_list"'))!);
    assert.equal(row.service, 'coordinator');
    assert.equal(row.kind, 'server');
    assert.match(row.trace_id, /^[0-9a-f]{32}$/);
  } finally { await c.stop(); ch.close(); }
});

test('T2 a host span batch keeps only rows of its own host and executions, with the batch id as dedup token (requirement: fleet-telemetry.md §5 SpanBatchRequest; risk: one host writes spans as another)', async () => {
  const ch = await fakeClickHouse();
  const c = await startCoordinator({name: 'tele2', env: {FLEET_CLICKHOUSE_URL: ch.url}});
  try {
    const id = 'h-' + randomUUID().slice(0, 8);
    const secret = (await call(c, '/api/ops/hosts_register', {id})).body.secret;
    const span = (host: string, extra = {}) => ({trace_id: 'a'.repeat(32), span_id: 'b'.repeat(16), parent_span_id: '', service: 'executor', host_id: host, name: 'host.heartbeat', kind: 'check',
      start: '2026-11-11 10:00:00.000000', duration_ns: 1, outcome: 'ok', error_class: '', actor: 'fleet', attrs: {}, ...extra});
    const r = await call(c, '/api/hosts/spans', {batchId: 'b1', spans: [span(id), span('other-host'), span(id, {execution_id: 'not-mine'})]}, {bearer: secret, person: null});
    assert.equal(r.status, 200, JSON.stringify(r.body));
    assert.deepEqual(r.body, {accepted: 1, skipped: 2});
    const ins = ch.seen.filter((x) => x.query.startsWith('INSERT INTO span') && x.params.get('insert_deduplication_token') === id + ':b1');
    assert.equal(ins.length, 1);
    assert.equal(ins[0].body.trim().split('\n').length, 1);
    assert.equal((await call(c, '/api/hosts/spans', {batchId: 'b2', spans: []})).status, 401, 'a person bearer is not a host');
  } finally { await c.stop(); ch.close(); }
});

test('T3 a non-owner telemetry read is limited to granted projects and gets no host readings (requirement: fleet-telemetry.md §5 Permissions; risk: system spans or another project leak)', async () => {
  const ch = await fakeClickHouse();
  const c = await startCoordinator({name: 'tele3', env: {FLEET_CLICKHOUSE_URL: ch.url}});
  try {
    const before = ch.seen.length;
    const r = await call(c, '/api/ops/telemetry_summary', {window: '5m'}, {person: MEMBER});
    assert.equal(r.status, 200, JSON.stringify(r.body));
    assert.deepEqual([r.body.enabled, r.body.hosts, r.body.processes], [true, [], []]);
    const reads = ch.seen.slice(before).filter((x) => x.query.startsWith('SELECT'));
    assert.equal(reads.length, 2, 'operations and runs only, no host or process reading');
    for (const x of reads) {
      assert.match(x.query, /project_id IN \{scope:Array\(String\)\}/);
      assert.equal(x.params.get('param_scope'), "['fleet-hello-world']");
    }
    assert.equal((await call(c, '/api/ops/telemetry_trace', {traceId: 'nope'})).status, 400);
  } finally { await c.stop(); ch.close(); }
});

test('T4 with a real ClickHouse (FLEET_TEST_CLICKHOUSE_URL, user t, password t) the schema applies, host spans land and every read query runs (requirement: fleet-telemetry.md §4, §5; risk: SQL that only a fake accepts)', {skip: !process.env.FLEET_TEST_CLICKHOUSE_URL}, async () => {
  const {mkdtempSync, writeFileSync} = await import('node:fs');
  const {join} = await import('node:path');
  const {tmpdir} = await import('node:os');
  const env = join(mkdtempSync(join(tmpdir(), 'fleet-ch-')), 'clickhouse.env');
  writeFileSync(env, 'CLICKHOUSE_DB=fleet\nCLICKHOUSE_USER=t\nCLICKHOUSE_PASSWORD=t\n');
  const c = await startCoordinator({name: 'tele4', env: {FLEET_CLICKHOUSE_URL: process.env.FLEET_TEST_CLICKHOUSE_URL!, FLEET_CLICKHOUSE_ENV: env}});
  try {
    const id = 'h-' + randomUUID().slice(0, 8);
    const secret = (await call(c, '/api/ops/hosts_register', {id})).body.secret;
    const trace = randomUUID().replace(/-/g, '');
    const now = new Date().toISOString().replace('T', ' ').replace('Z', '000');
    const span = (name: string, extra = {}) => ({trace_id: trace, span_id: randomUUID().replace(/-/g, '').slice(0, 16), parent_span_id: '', service: 'executor', host_id: id, name, kind: 'check',
      start: now, duration_ns: 1000, outcome: 'ok', error_class: '', actor: 'fleet', ...extra});
    let r: any;
    for (let i = 0; i < 50; i++) { // the schema applies in the background at start
      r = await call(c, '/api/hosts/spans', {batchId: 'b1', spans: [span('host.heartbeat', {load1: 0.5, mem_free: 1e9, disk_free: 2e9, attrs: {'fleet.cpus': '8', 'fleet.mem_total': '16000000000'}}),
        span('process.reading', {cpu_seconds: 1.5, rss_bytes: 1000, open_files: 10, attrs: {'fleet.process': 'executor'}})]}, {bearer: secret, person: null});
      if (r.status === 200) break;
      await new Promise((res) => setTimeout(res, 200));
    }
    assert.deepEqual(r.body, {accepted: 2, skipped: 0});
    await until(async () => {
      const s = (await call(c, '/api/ops/telemetry_summary', {window: '5m'})).body;
      assert.equal(s.error, undefined, s.error);
      return s.hosts.some((h: any) => h.hostId === id && h.cpus === 8 && h.load1 === 0.5) && s.processes.some((p: any) => p.hostId === id && p.process === 'executor' && p.rssBytes === 1000)
        && s.operations.some((o: any) => o.service === 'coordinator' && o.name === 'hosts_register');
    }, 'summary reads the spans');
    const t = await call(c, '/api/ops/telemetry_trace', {traceId: trace});
    assert.equal(t.status, 200, JSON.stringify(t.body));
    assert.equal(t.body.spans.length, 2);
    const m = await call(c, '/api/ops/telemetry_trace', {traceId: trace}, {person: MEMBER});
    assert.deepEqual([m.body.spans.length, m.body.hidden], [0, 2], 'system spans hidden from a project member');
  } finally { await c.stop(); }
});
