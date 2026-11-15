// Isolated, two-machine qualification. No arbitrary origin/database target is accepted.
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { appendFileSync, createWriteStream, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import http from 'node:http';
import { cpus, freemem, hostname, platform, totalmem } from 'node:os';
import { resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';
import { fixture, literal, manifest, marker, pricesSQL, projectIDs, seedSQL, type Fixture } from './seed-interface.ts';
import { startCoordinator, type Coordinator } from '../harness.ts';

const sleep = (ms: number) => new Promise<void>(r => setTimeout(r, ms));
const stamp = () => performance.now();
const command = (name: string, args: string[]) => { try { return execFileSync(name, args, { encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 }).trim(); } catch { return null; } };
export function stats(values: number[]) {
  const v = values.toSorted((a, b) => a - b);
  const q = (p: number) => v.length ? v[Math.max(0, Math.ceil(p * v.length) - 1)] : null;
  return { count: v.length, min: q(0), p50: q(.5), p95: q(.95), p99: q(.99), max: q(1), violations: v.filter(x => x > 5000).length };
}
export type SourceDistribution = 'hot' | 'distinct';
export type Workload = { sourceDistribution: SourceDistribution; readers: number; rate: number; warmup: number; duration: number; burst: number; recovery: number; mode: 'connected' | 'exceed-capacity'; network: 'unshaped' | '80ms-10Mbps' };
export function workload(x: Partial<Workload>, development = false): Workload {
  const w: Workload = { sourceDistribution: 'hot', readers: 1, rate: 1, warmup: 60, duration: 600, burst: 1000, recovery: 60, mode: 'connected', network: 'unshaped', ...x };
  for (const k of ['readers', 'rate', 'warmup', 'duration', 'burst', 'recovery'] as const) if (!Number.isInteger(w[k]) || w[k] < 0) throw Error('invalid workload ' + k);
  if (!['hot', 'distinct'].includes(w.sourceDistribution) || !['connected', 'exceed-capacity'].includes(w.mode) || !['unshaped', '80ms-10Mbps'].includes(w.network)) throw Error('invalid classification');
  if (development) { if (w.readers !== 1 || w.rate > 1 || w.warmup > 2 || w.duration > 3 || w.burst > 1 || w.recovery > 5) throw Error('development is a small smoke only'); }
  else if (![1, 10, 50].includes(w.readers) || (w.mode === 'connected' && ![1, 20, 100].includes(w.rate)) || w.rate < 1 || w.warmup !== 60 || w.duration !== 600 || w.burst !== 1000 || w.recovery < 60) throw Error('outside declared envelope; classify exceed-capacity separately');
  return w;
}

function hardware() {
  const read = (p: string) => { try { return readFileSync(p, 'utf8').trim(); } catch { return null; } };
  const cg = read('/proc/self/cgroup'), path = cg?.split('\n').find(x => x.startsWith('0::'))?.slice(3);
  return { hostname: hostname(), platform: platform(), cpu: cpus(), totalmem: totalmem(), freemem: freemem(), uname: command('uname', ['-a']), disk: command('lsblk', ['-o', 'NAME,SIZE,ROTA,TYPE,MODEL']), mounts: command('findmnt', ['-J']), cgroup: { membership: cg, cpuMax: path ? read('/sys/fs/cgroup' + path + '/cpu.max') : null, memoryMax: path ? read('/sys/fs/cgroup' + path + '/memory.max') : null }, pressure: { cpu: read('/proc/pressure/cpu'), memory: read('/proc/pressure/memory'), io: read('/proc/pressure/io') }, postgres: command('psql', ['--version']), tc: command('tc', ['-j', 'qdisc', 'show']) };
}
function candidate() {
  const sha = command('git', ['rev-parse', 'HEAD']);
  const status = command('git', ['status', '--porcelain']);
  const binary = process.env.FLEET_TRIAL_SERVER ?? 'go/bin/fleet-coordinator';
  if (!existsSync(binary)) throw Error('build candidate coordinator first: ' + binary);
  const files = ['tests/performance/interface.ts', 'tests/performance/seed-interface.ts', 'tests/harness.ts', 'app/src/api.ts'];
  return { sha, dirty: status, diffSHA256: createHash('sha256').update(command('git', ['diff', 'HEAD']) ?? '').digest('hex'), sourceFiles: Object.fromEntries(files.map(p => [p, createHash('sha256').update(readFileSync(p)).digest('hex')])), binarySHA256: createHash('sha256').update(readFileSync(binary)).digest('hex'), builtAssets: command('sh', ['-c', 'find dist/app -type f -name "*.js" -exec shasum -a 256 {} \\;']) };
}

// One persistent psql session per project. No process spawn per fact and no synchronous psql
// on the measured source-write path. The lower bound precedes submission; the receipt follows COMMIT.
class Writer {
  child: ReturnType<typeof spawn>;
  pending = new Map<string, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();
  constructor(db: string, log: NodeJS.WritableStream) {
    this.child = spawn('psql', ['-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-h', process.env.FLEET_TRIAL_PGDATA!, '-p', process.env.FLEET_TRIAL_PGPORT ?? '54329', '-U', 'fleet', '-d', db], { stdio: ['pipe', 'pipe', 'pipe'] });
    this.child.stderr!.pipe(log, { end: false });
    createInterface({ input: this.child.stdout! }).on('line', line => {
      if (!line.startsWith('{')) return;
      try { const v = JSON.parse(line), p = this.pending.get(v.tag); if (p) { this.pending.delete(v.tag); p.resolve(v); } } catch { this.fail(new Error('invalid SQL receipt')); }
    });
    this.child.on('error', e => this.fail(e)); this.child.on('exit', code => this.fail(new Error('writer exited ' + code)));
    this.child.stdin!.write("SET fleet.project_ids='*';\n");
  }
  fail(e: Error) { for (const p of this.pending.values()) p.reject(e); this.pending.clear(); }
  run(sql: string, payload = 'NULL'): Promise<any> {
    const tag = randomUUID();
    return new Promise((resolve, reject) => { this.pending.set(tag, { resolve, reject }); this.child.stdin!.write(sql + `\nSELECT json_build_object('tag',${literal(tag)},'afterCommit',clock_timestamp(),'backend',pg_backend_pid(),'payload',${payload});\n`); });
  }
  close() { this.child.stdin!.end(); }
}
export type Publication = { epoch: string; revision: string; calls: number; errors: number };
export type Fact = { id: string; execution: string; project: string; n: number; calls: number; errors: number; lower: number; upper: number | null; phase: string; due: number; transaction?: string; error?: string; proof?: Publication };
// Both endpoints use performance.now() in this process. Round outward by one millisecond
// per endpoint; neither SQL wall clocks nor browser clock offsets are subtracted.
export const sourceClockUncertaintyMs = 1;
export function sourceRate(facts: Fact[], spec: Workload, uncertaintyMs = sourceClockUncertaintyMs) {
  const phases = (['sustained', 'burst'] as const).map(phase => {
    const rows = facts.filter(f => f.phase === phase), expected = phase === 'burst' ? spec.burst : spec.rate * spec.duration;
    const confirmed = rows.filter(f => !f.error && f.upper !== null && f.transaction);
    const budgetMs = phase === 'burst' ? 1000 : spec.duration * 1000;
    const lower = rows.length ? rows.reduce((v, f) => Math.min(v, f.lower), Infinity) : null;
    const upper = confirmed.length ? confirmed.reduce((v, f) => Math.max(v, f.upper!), -Infinity) : null;
    const spanMs = lower === null || upper === null ? null : upper - lower + 2 * uncertaintyMs;
    // Count mutations/transactions independently: an aggregate counter delta is not a fact.
    const identitiesValid = new Set(rows.map(f => f.id)).size === expected && new Set(confirmed.map(f => f.transaction)).size === expected;
    const ordered = rows.toSorted((a, b) => a.due - b.due);
    const windows = phase === 'sustained' ? Array.from({ length: spec.duration }, (_, second) => {
      const bucket = ordered.slice(second * spec.rate, (second + 1) * spec.rate);
      const spanMs = bucket.length ? Math.max(...bucket.map(f => f.upper ?? Infinity)) - Math.min(...bucket.map(f => f.lower)) + 2 * uncertaintyMs : null;
      return { second, count: bucket.length, spanMs, valid: bucket.length === spec.rate && spanMs !== null && spanMs <= 1000 };
    }) : [];
    const valid = windows.every(w => w.valid) && Number.isFinite(uncertaintyMs) && uncertaintyMs >= 0 && expected > 0 && rows.length === expected && confirmed.length === expected && identitiesValid && spanMs !== null && spanMs <= budgetMs && confirmed.every(f => Number.isFinite(f.lower) && Number.isFinite(f.upper) && f.upper! >= f.lower);
    return { phase, expected, offered: rows.length, admitted: confirmed.length, confirmed: confirmed.length, transactions: new Set(confirmed.map(f => f.transaction)).size, lower, upper, spanMs, budgetMs, windows, minimumConfirmedPerSecond: spanMs && spanMs > 0 ? confirmed.length * 1000 / spanMs : null, valid };
  });
  const unique = new Set(facts.map(f => f.id)).size === facts.length && new Set(facts.flatMap(f => f.transaction ? [f.transaction] : [])).size === facts.filter(f => f.transaction).length;
  return { status: unique && phases.every(p => p.valid) ? 'qualified' : 'invalid-source-rate', uncertaintyMs, clock: 'same-process monotonic outward brackets, includes submission/queue/commit receipt', phases };
}
export function publicationVersion(value: string): { epoch: string; revision: string } | null {
  const m = /^([^:]+):([0-9]+)$/.exec(value);
  return m ? { epoch: m[1], revision: m[2] } : null;
}
export function canAcknowledge(fact: Fact, rendered: Publication, text: string) {
  const proof = fact.proof;
  return !fact.error && fact.upper !== null && !!proof && proof.epoch === rendered.epoch && BigInt(rendered.revision) >= BigInt(proof.revision) && rendered.calls >= proof.calls && rendered.errors >= proof.errors && text === `${rendered.calls} · ${rendered.errors} failed`;
}
export function bindProof(facts: Fact[], publication: Publication, leaves: Record<string, { calls: number; errors: number }>, distribution: SourceDistribution) {
  for (const fact of facts) {
    const leaf = leaves[fact.execution];
    if (fact.proof?.epoch === publication.epoch || fact.error || fact.upper === null || !leaf) continue;
    const included = distribution === 'hot' ? leaf.calls >= fact.calls && leaf.errors === fact.errors : leaf.calls === fact.calls && leaf.errors === fact.errors;
    if (included) fact.proof = { ...publication };
  }
}
export function sourceMutation(fact: Fact, distribution: SourceDistribution) {
  const totals = `jsonb_build_object('byTool',jsonb_build_object(${literal(marker)},jsonb_build_object('calls',${fact.calls},'errors',${fact.errors})))`;
  const mutation = distribution === 'hot'
    ? `UPDATE execution_totals SET tool_calls=${totals} WHERE execution_id=${literal(fact.execution)};`
    : `INSERT INTO execution(id,kind,state,project_id,started_at,harness,model) VALUES(${literal(fact.execution)},'agent_run','running',${literal(fact.project)},'2024-01-01','codex','interface-seed1-model-0'); INSERT INTO execution_totals(execution_id,project_id,tool_calls,tool_calls_basis) VALUES(${literal(fact.execution)},${literal(fact.project)},${totals},'measured');`;
  return mutation;
}
export function sourceTransaction(fact: Fact, distribution: SourceDistribution) {
  return `BEGIN; ${sourceMutation(fact, distribution)}
SELECT pg_current_xact_id()::text AS source_tx, clock_timestamp()::text AS before_commit \\gset
COMMIT;`;
}
export function proofSQL(project: string, executions: string[]) {
  return `(SELECT json_build_object('epoch',s.epoch::text,'revision',s.revision::text,'calls',coalesce(t.calls,0),'errors',coalesce(t.errors,0),'leaves',(SELECT coalesce(json_object_agg(c.execution_id,json_build_object('calls',(v->>'calls')::bigint,'errors',(v->>'errors')::bigint)),'{}') FROM resource_contribution c CROSS JOIN LATERAL jsonb_array_elements(c.facts->'tools') v WHERE c.project_id=s.project_id AND c.included AND c.execution_id=ANY(ARRAY[${executions.map(literal).join(',')}]::text[]) AND v->>'name'=${literal(marker)} AND v->>'connection'='')) FROM resource_project_summary s LEFT JOIN resource_tool t ON t.project_id=s.project_id AND t.name=${literal(marker)} AND t.connection='' WHERE s.project_id=${literal(project)} AND s.ready)`;
}
type Run = { id: string; spec: Workload; start: number; done: boolean; facts: Map<string, Fact[]>; seen: Map<string, Set<string>>; publications: Map<string, Publication>; metrics: { lags: number[]; writes: number[]; schedule: number[]; controls: number[] }; errors: string[]; readers: Map<string, { project: string; visible: boolean }>; count: number; finish?: unknown };
const pgSnapshot = (c: Coordinator) => JSON.parse(c.sql(`SELECT json_build_object('at',clock_timestamp(),'database',current_database(),'pg',version(),'settings',(SELECT json_object_agg(name,setting) FROM pg_settings WHERE name IN ('shared_buffers','work_mem','max_connections','fsync','synchronous_commit','track_io_timing','track_commit_timestamp','max_wal_size','effective_cache_size')),'databaseStats',(SELECT row_to_json(s) FROM pg_stat_database s WHERE datname=current_database()),'activity',(SELECT coalesce(json_agg(a),'[]') FROM (SELECT pid,state,wait_event_type,wait_event,query_start,left(query,250) query FROM pg_stat_activity WHERE datname=current_database()) a),'backlog',(SELECT count(*) FROM resource_work),'oldestPending',(SELECT min(first_pending_at) FROM resource_work),'builds',(SELECT coalesce(json_agg(b),'[]') FROM resource_build b))`).trim());

export async function serve(o: { out: string; history: number; projects: number; cardinality: number; port: number; development: boolean; placement: string; longEvents?: number }) {
  const f = fixture(o.history, o.projects, o.cardinality), ids = projectIDs(f);
  if (!o.development && platform() !== 'linux') throw Error('heavy qualification requires isolated Linux placement');
  if (o.development && (f.history > 618 || f.projects !== 1 || f.cardinality > 3)) throw Error('development fixture exceeds small smoke');
  if (o.longEvents && (o.development || o.longEvents !== 100_000)) throw Error('long fixture is exactly100000 accepted events on isolated placement only');
  if (!process.env.FLEET_TRIAL_PGDATA || !process.env.FLEET_TRIAL_PGDATA.startsWith(resolve(o.out) + '/')) throw Error('FLEET_TRIAL_PGDATA must be inside this run output directory; no shared/production PostgreSQL');
  const placement = JSON.parse(readFileSync(o.placement, 'utf8'));
  if (!o.development && (placement.isolatedProfile !== true || placement.vcpu !== 4 || placement.memoryGiB !== 8 || placement.ssd !== true || !placement.reservation || !placement.effectiveControls)) throw Error('declared isolated 4CPU/8GiB/SSD reservation and effective controls required; physical sharing remains explicit');
  if (!o.development) {
    const effective = hardware().cgroup, cpu = effective.cpuMax?.split(' ').map(Number);
    if (!cpu || cpu[0] / cpu[1] !== 4 || effective.memoryMax !== String(8 * 1024 ** 3)) throw Error('actual server cgroup must constrain the whole process tree to4CPU/8GiB: ' + JSON.stringify(effective));
  }
  mkdirSync(o.out, { recursive: true });
  const baselineManifest = manifest(f);
  writeFileSync(resolve(o.out, 'manifest.json'), JSON.stringify(baselineManifest, null, 2));
  writeFileSync(resolve(o.out, 'server.json'), JSON.stringify({ at: new Date().toISOString(), development: o.development, placement, hardware: hardware(), candidate: candidate(), clock: 'server monotonic milliseconds; lower before source submission, upper after commit; render receipt is a conservative actual-render upper bound including return network' }, null, 2));
  const log = createWriteStream(resolve(o.out, 'events.jsonl'), { flags: 'a' });
  const sqlLog = createWriteStream(resolve(o.out, 'psql.log'), { flags: 'a' });
  const emit = (type: string, body: object) => log.write(JSON.stringify({ type, at: stamp(), ...body }) + '\n');
  const spans = createWriteStream(resolve(o.out, 'spans.jsonl'), { flags: 'a' });
  // Capture REAL coordinator spans through its normal wire exporter. SELECT/schema calls get
  // empty success; this sink is not a ClickHouse correctness/performance qualification.
  const sink = http.createServer(async (req, res) => {
    const query = new URL(req.url!, 'http://localhost').searchParams.get('query') ?? '';
    for await (const chunk of req) if (query.startsWith('INSERT INTO span ')) spans.write(chunk);
    res.end();
  });
  await new Promise<void>(r => sink.listen(0, '127.0.0.1', r));
  const pgdata = process.env.FLEET_TRIAL_PGDATA!;
  if (!existsSync(resolve(pgdata, 'PG_VERSION'))) {
    mkdirSync(pgdata, { recursive: true });
    execFileSync('initdb', ['-D', pgdata, '-U', 'postgres', '-A', 'trust', '-E', 'UTF8', '--no-instructions'], { stdio: 'ignore' });
    appendFileSync(resolve(pgdata, 'postgresql.auto.conf'), "\nshared_preload_libraries='pg_stat_statements'\ntrack_io_timing=on\nlog_lock_waits=on\ndeadlock_timeout='100ms'\n");
  }
  const env = { FLEET_RESOURCE_PROCESSOR: 'off', FLEET_RESOURCE_READS: 'on', FLEET_LOST_AFTER_MS: '864000000', FLEET_CLICKHOUSE_URL: 'http://127.0.0.1:' + (sink.address() as any).port, FLEET_CLICKHOUSE_ENV: '/nonexistent' };
  let c: Coordinator;
  try { c = await startCoordinator({ name: 'interface-' + randomUUID().slice(0, 8), env }); }
  catch (e) { sink.close(); spans.end(); log.end(); sqlLog.end(); throw e; }
  let run: Run | null = null;
  const errors: string[] = [];
  const op = async (name: string, body: object) => {
    const r = await fetch(c.base + '/api/ops/' + name, { method: 'POST', headers: { authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json' }, body: JSON.stringify(body) });
    const text = await r.text(); if (!r.ok) throw Error(`${name} ${r.status}: ${text}`); return JSON.parse(text);
  };
  const writers: Writer[] = [];
  const sourceWriters: Writer[] = [];
  const server = http.createServer();
  let monitor: ReturnType<typeof setInterval> | undefined, closing = false;
  const close = async () => {
    if (closing) return; closing = true;
    if (monitor) clearInterval(monitor);
    server.close(); for (const w of writers) w.close(); await c.stop(); sink.close();
    execFileSync('pg_ctl', ['-D', pgdata, '-m', 'fast', '-w', 'stop'], { stdio: 'ignore' });
    log.end(); sqlLog.end(); spans.end();
  };
  process.once('SIGTERM', () => void close()); process.once('SIGINT', () => void close());
  try {
    const db = c.sql('SELECT current_database()').trim();
    execFileSync('psql', ['-X', '-q', '-v', 'ON_ERROR_STOP=1', '-h', pgdata, '-p', process.env.FLEET_TRIAL_PGPORT ?? '54329', '-U', 'postgres', '-d', db], { input: 'CREATE EXTENSION IF NOT EXISTS pg_stat_statements; GRANT pg_read_all_stats TO fleet;', stdio: ['pipe', 'ignore', 'pipe'] });
    for (const id of ids) await op('project_create', { id, requestId: randomUUID(), content: { settings: { id, name: id, contextFiles: ['rules.md'], roles: { pm: 'pm' } }, contextFiles: [{ path: 'rules.md', text: 'Synthetic interface qualification. No agent launch.' }] } });
    c.sql(pricesSQL(f.cardinality));
    for (let first = 0; first < f.history; first += 1000) { c.sql(seedSQL(f, first, Math.min(first + 999, f.history - 1))); emit('seed', { first, total: f.history }); }
    env.FLEET_RESOURCE_PROCESSOR = 'on'; await c.restart();
    for (;;) {
      const state = await Promise.all(ids.map(projectId => op('project_resources', { projectId })));
      if (state.every(x => x.kind === 'ready' && x.progress.state === 'current')) break;
      emit('build', { state: state.map(x => ({ project: x.projectId, progress: x.progress })) }); await sleep(1000);
    }
    await parity(c, f, o.out);
    for (const id of ids) c.sql(`BEGIN; INSERT INTO execution(id,kind,state,project_id,started_at,harness,model) VALUES(${literal(id + '-marker')},'agent_run','running',${literal(id)},'2024-01-01','codex','interface-seed1-model-0'); INSERT INTO execution_totals(execution_id,project_id,tool_calls,tool_calls_basis) VALUES(${literal(id + '-marker')},${literal(id)},${literal(JSON.stringify({ byTool: { [marker]: { calls: 0, errors: 0 } } }))},'measured'); COMMIT;`);
    let native: { secret: string; seq: number } | null = null;
    const hostID = 'interface-seed1-host';
    const ingest = async (events: object[]) => {
      const t = stamp();
      const r = await fetch(c.base + '/api/hosts/events', { method: 'POST', headers: { authorization: 'Bearer ' + native!.secret, 'content-type': 'application/json' }, body: JSON.stringify({ hostId: hostID, executionId: ids[0] + '-marker', events }) });
      if (!r.ok) throw Error('native event acceptance ' + r.status + ': ' + await r.text());
      const receipt = await r.json(); emit('native-acceptance', { count: events.length, ms: stamp() - t, receipt });
    };
    const usage = (seq: number) => ({ id: 'interface-seed1-usage-' + seq, seq, type: 'usage.observation', timestamp: 1704067200000 + seq, data: { fleetNormalized: { lifecycle: 'usage.observation', nativeUsage: { last: { inputTokens: 1, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, reasoningOutputTokens: 0 } } } } });
    if (o.longEvents) {
      const registered = await op('hosts_register', { id: hostID }); native = { secret: registered.secret, seq: 0 };
      c.sql(`UPDATE execution SET host_id=${literal(hostID)},data=jsonb_build_object('hostId',${literal(hostID)}) WHERE id=${literal(ids[0] + '-marker')}`);
      for (let i = 0; i < o.longEvents; i += 500) { const events = Array.from({ length: Math.min(500, o.longEvents - i) }, () => usage(++native!.seq)); await ingest(events); }
      await ingest([usage(2), usage(1)]); // identical retained IDs/bodies, reordered; no new usage.
      const proof = JSON.parse(c.sql(`SELECT json_build_object('input',t.tokens_input,'state',e.state,'cpu',t.cpu_ms,'api',t.api_ms,'wall',t.wall_ms,'events',(SELECT count(*) FROM document WHERE kind='event' AND data->>'executionId'=e.id)) FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.id=${literal(ids[0] + '-marker')}`));
      assert.equal(proof.input, o.longEvents); assert.equal(proof.events, o.longEvents); assert.equal(proof.state, 'running'); assert.equal(proof.cpu, null); assert.equal(proof.api, null); assert.equal(proof.wall, null);
      writeFileSync(resolve(o.out, 'long-native.json'), JSON.stringify(proof, null, 2));
    }
    for (const _ of ids) { const writer = new Writer(db, sqlLog); writers.push(writer); sourceWriters.push(writer); }
    const observer = new Writer(db, sqlLog); writers.push(observer);
    let observing = false;
    monitor = setInterval(() => {
      if (observing || closing) return; observing = true;
      void observer.run('', `(SELECT json_build_object('pending',count(*),'oldest',min(first_pending_at),'failed',count(*) FILTER(WHERE last_error_class IS NOT NULL)) FROM resource_work)`).then(result => emit('backlog', { run: run?.id, ...result })).catch(e => emit('observer-error', { error: String(e) })).finally(() => { observing = false; });
    }, 1000);
    writeFileSync(resolve(o.out, 'pg-before.json'), JSON.stringify(pgSnapshot(c), null, 2));
    const writeFact = async (r: Run, index: number, phase: string, due: number) => {
      const project = ids[index % ids.length], list = r.facts.get(project)!;
      const n = list.length + 1, execution = r.spec.sourceDistribution === 'hot' ? project + '-marker' : `${project}-${r.id}-leaf-${index}`;
      const fact: Fact = { id: `${r.id}-${index}`, execution, project, n, calls: r.spec.sourceDistribution === 'hot' ? n : 1, errors: r.spec.sourceDistribution === 'distinct' && index % 7 === 0 ? 1 : 0, lower: stamp(), upper: null, phase, due }; list.push(fact); r.count++;
      emit('source-submitted', { run: r.id, sourceDistribution: r.spec.sourceDistribution, ...fact });
      try {
        if (native && project === ids[0]) await ingest([usage(++native.seq)]);
        const receipt = await sourceWriters[index % sourceWriters.length].run(sourceTransaction(fact, r.spec.sourceDistribution), `json_build_object('transaction',:'source_tx','beforeCommit',:'before_commit')`);
        fact.upper = stamp(); fact.transaction = receipt.payload.transaction;
        r.metrics.writes.push(fact.upper - fact.lower); r.metrics.schedule.push(fact.lower - due); emit('confirmed', { run: r.id, sourceDistribution: r.spec.sourceDistribution, canonicalMutations: r.spec.sourceDistribution === 'hot' ? 1 : 2, ...fact, receipt });
      } catch (e) { fact.error = String(e); r.errors.push(String(e)); emit('source-error', { run: r.id, ...fact }); }
    };
    // One SELECT gives contribution membership, aggregate numerics and publication identity
    // in the SAME PostgreSQL read snapshot. This is fixture-only, never a production API.
    const proofReads = new Map<string, Promise<void>>();
    const prove = (r: Run, project: string) => {
      const active = proofReads.get(project); if (active) return active;
      const facts = r.facts.get(project)!, pending = [...new Set(facts.filter(f => !f.proof && !f.error).map(f => f.execution))];
      const query = proofSQL(project, pending);
      const task = observer.run('', query).then(receipt => {
        const p = receipt.payload; if (!p) return;
        const publication: Publication = { epoch: p.epoch, revision: p.revision, calls: p.calls, errors: p.errors };
        for (const fact of facts) if (fact.proof && fact.proof.epoch !== publication.epoch) delete fact.proof;
        bindProof(facts, publication, p.leaves, r.spec.sourceDistribution);
        r.publications.set(project + '/' + p.epoch + ':' + p.revision, publication);
        emit('publication-proof', { run: r.id, project, publication, leaves: p.leaves, receipt });
      }).finally(() => proofReads.delete(project));
      proofReads.set(project, task); return task;
    };
    const finish = async (r: Run) => {
      const missing: object[] = [];
      for (const [reader, { project }] of r.readers) {
        const seen = r.seen.get(reader) ?? new Set<string>();
        for (const fact of r.facts.get(project)!) if (!seen.has(fact.id) && !fact.error && fact.phase !== 'warmup') missing.push({ reader, project, n: fact.n, age: stamp() - fact.lower });
      }
      const allFacts = [...r.facts.values()].flat();
      const expected = ids.map(project => {
        const facts = r.facts.get(project)!.filter(f => !f.error && f.upper !== null);
        const calls = r.spec.sourceDistribution === 'hot' ? facts.at(-1)?.calls ?? 0 : facts.reduce((s, f) => s + f.calls, 0), errors = facts.reduce((s, f) => s + f.errors, 0);
        return { project, tools: { ...baselineManifest.expected[project].tools, [marker]: { calls, errors } }, totalCalls: baselineManifest.expected[project].metrics.toolCalls.sum + calls, totalErrors: baselineManifest.expected[project].metrics.toolErrors.sum + errors, calls: r.spec.sourceDistribution === 'hot' ? facts.at(-1)?.calls ?? 0 : facts.reduce((s, f) => s + f.calls, 0), errors: facts.reduce((s, f) => s + f.errors, 0), executions: baselineManifest.expected[project].executions + 1 + (r.spec.sourceDistribution === 'distinct' ? facts.length : 0) };
      });
      const recovery = expected.map(e => ({ ...e, observed: JSON.parse(c.sql(`SELECT json_build_object('calls',coalesce((SELECT calls FROM resource_tool WHERE project_id=${literal(e.project)} AND name=${literal(marker)} AND connection=''),0),'errors',coalesce((SELECT errors FROM resource_tool WHERE project_id=${literal(e.project)} AND name=${literal(marker)} AND connection=''),0),'tools',(SELECT coalesce(json_object_agg(name,v),'{}') FROM (SELECT name,sum(calls)::bigint calls,sum(errors)::bigint errors FROM resource_tool WHERE project_id=${literal(e.project)} GROUP BY name) v),'totalCalls',(SELECT sum(sum_value) FROM resource_metric WHERE project_id=${literal(e.project)} AND metric='toolCalls'),'totalErrors',(SELECT sum(sum_value) FROM resource_metric WHERE project_id=${literal(e.project)} AND metric='toolErrors'),'executions',(SELECT executions FROM resource_project_summary WHERE project_id=${literal(e.project)}),'pending',(SELECT count(*) FROM resource_work_scope WHERE project_id=${literal(e.project)}))`)) }));
      for (const x of recovery) if (x.calls !== x.observed.calls || x.errors !== x.observed.errors || x.executions !== x.observed.executions || x.totalCalls !== x.observed.totalCalls || x.totalErrors !== x.observed.totalErrors || Object.keys(x.tools).length !== Object.keys(x.observed.tools).length || Object.entries(x.tools).some(([name, t]) => t.calls !== x.observed.tools[name]?.calls || t.errors !== x.observed.tools[name]?.errors) || x.observed.pending) r.errors.push('recovery/double-count mismatch: ' + JSON.stringify(x));
      if (!r.metrics.lags.length) r.errors.push('no measured connected foreground samples');
      const rate = sourceRate(allFacts, r.spec);
      const failed = r.errors.length || missing.length || (r.spec.mode === 'connected' && r.metrics.lags.some(x => x > 5000));
      const source = { distribution: r.spec.sourceDistribution, logicalFactCount: allFacts.length, logicalFacts: allFacts.map(f => f.id), projects: [...r.facts.keys()], leafIDs: [...new Set(allFacts.map(f => f.execution))], distinctLeaves: new Set(allFacts.map(f => f.execution)).size, canonicalMutations: allFacts.filter(f => f.upper !== null).length * (r.spec.sourceDistribution === 'hot' ? 1 : 2), commitTransactionCount: new Set(allFacts.flatMap(f => f.transaction ? [f.transaction] : [])).size, commitTransactions: [...new Set(allFacts.flatMap(f => f.transaction ? [f.transaction] : []))] };
      const result = { id: r.id, spec: r.spec, development: o.development, status: rate.status !== 'qualified' ? 'incomplete' : failed ? 'failed' : r.spec.mode === 'exceed-capacity' ? 'recovered' : 'passed', reason: rate.status !== 'qualified' ? 'invalid-source-rate' : null, sourceRate: rate, source, classifications: [r.spec.mode, r.spec.sourceDistribution], facts: r.count, readers: [...r.readers], samples: stats(r.metrics.lags), sourceWriteMs: stats(r.metrics.writes), scheduleDelayMs: stats(r.metrics.schedule), actionAckMs: stats(r.metrics.controls), recovery, missing, errors: r.errors, note: 'Hot mode is hot-execution replacement evidence, never distinct-launch throughput. Legacy hot passed results without source-rate proof are unqualified. Render bounds include proof read and return network. Development is not capacity acceptance. This run alone does not prove the full envelope.' };
      writeFileSync(resolve(o.out, 'source-manifest.json'), JSON.stringify({ run: r.id, spec: r.spec, source, expected, facts: allFacts }, null, 2));
      r.finish = result; r.done = true; emit('finish', result);
      writeFileSync(resolve(o.out, r.id + '-result.json'), JSON.stringify(result, null, 2));
      writeFileSync(resolve(o.out, r.id + '-pg-after.json'), JSON.stringify(pgSnapshot(c), null, 2));
      writeFileSync(resolve(o.out, r.id + '-query-statistics.json'), c.sql("SELECT coalesce(json_agg(s),'[]') FROM (SELECT * FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) ORDER BY total_exec_time DESC) s"));
    };
    const produce = async (r: Run) => {
      const tasks = new Set<Promise<void>>(); let ordinal = 0;
      // Distinct facts have no shared mutable leaf: eight lanes per project, each with
      // its own persistent transaction session. Hot keeps one ordered lane/project.
      if (r.spec.sourceDistribution === 'distinct') for (let i = sourceWriters.length; i < ids.length * 8; i++) { const writer = new Writer(db, sqlLog); writers.push(writer); sourceWriters.push(writer); }
      emit('source-lanes', { run: r.id, distribution: r.spec.sourceDistribution, lanes: sourceWriters.length });
      const chains = sourceWriters.map(() => Promise.resolve());
      const schedule = (phase: string, due: number) => { const n = ordinal++, slot = n % sourceWriters.length; const p = chains[slot].then(() => writeFact(r, n, phase, due)); chains[slot] = p; tasks.add(p); void p.finally(() => tasks.delete(p)); };
      for (const [phase, seconds] of [['warmup', r.spec.warmup], ['sustained', r.spec.duration]] as const) {
        const start = stamp();
        for (let i = 0; i < seconds * r.spec.rate; i++) { const due = start + i * 1000 / r.spec.rate; if (due > stamp()) await sleep(due - stamp()); schedule(phase, due); }
      }
      const burst = stamp();
      for (let i = 0; i < r.spec.burst; i++) { const due = burst + i * 1000 / Math.max(1, r.spec.burst); if (due > stamp()) await sleep(due - stamp()); schedule('burst', due); }
      emit('burst-scheduled', { run: r.id, count: r.spec.burst, durationMs: stamp() - burst });
      await Promise.all(tasks);
      await sleep(r.spec.recovery * 1000); await finish(r);
    };
    server.on('request', async (req, res) => {
      try {
        if (req.url?.startsWith('/__interface/')) {
          const path = req.url.split('?')[0]; let b: any = {};
          if (req.method === 'POST') { const chunks: Buffer[] = []; let size = 0; for await (const chunk of req) { size += chunk.length; if (size > 1024 * 1024) throw Error('measurement body too large'); chunks.push(chunk); } b = JSON.parse(Buffer.concat(chunks).toString()); }
          let result: unknown;
          if (path === '/__interface/config') result = { fixture: f, projects: ids, marker, longEvents: o.longEvents ?? 0, development: o.development, hostname: hostname(), candidate: candidate().sha, run: run?.id };
          else if (path === '/__interface/clock') result = { monotonic: stamp(), wall: Date.now() };
          else if (path === '/__interface/start') {
            if (run && !run.done) throw Error('run already active');
            const spec = workload(b.spec, o.development);
            if (native && spec.sourceDistribution === 'distinct') throw Error('long native hot-execution fixture requires hot source distribution; use a separate fresh distinct case');
            if (!Array.isArray(b.readers) || b.readers.length !== spec.readers || new Set(b.readers.map((x: any) => x.id)).size !== spec.readers || b.readers.some((x: any) => !ids.includes(x.project))) throw Error('reader barrier mismatch');
            // Fresh server per case: marker counters and prepared baseline must not carry a prior run.
            if (run) throw Error('start a fresh isolated server per case');
            run = { id: randomUUID(), spec, start: stamp(), done: false, facts: new Map(ids.map(p => [p, []])), seen: new Map(), publications: new Map(), metrics: { lags: [], writes: [], schedule: [], controls: [] }, errors: [], readers: new Map(b.readers.map((x: any) => [x.id, { project: x.project, visible: true }])), count: 0 };
            emit('start', { id: run.id, spec, readers: b.readers, browser: b.browser }); result = { id: run.id }; void produce(run).catch(e => { run!.errors.push(String(e)); void finish(run!); });
          } else if (path === '/__interface/render') {
            const r = run; if (!r || r.id !== b.run || !r.readers.has(b.reader)) throw Error('unregistered render');
            const reader = r.readers.get(b.reader)!;
            if (reader.project !== b.project || !Number.isSafeInteger(b.value) || !Number.isSafeInteger(b.errors) || b.value < 0 || b.errors < 0 || !publicationVersion(b.version ?? '') || !b.visible || b.text !== `${b.value} · ${b.errors} failed`) throw Error('unverified rendered value/envelope');
            await prove(r, b.project);
            const received = stamp(), seen = r.seen.get(b.reader) ?? new Set<string>(), facts = r.facts.get(b.project)!;
            const publication = r.publications.get(b.project + '/' + b.version);
            // A version not observed by the proof read is retried, never guessed from a count.
            if (!publication || publication.calls !== b.value || publication.errors !== b.errors) { result = { accepted: false }; }
            else {
              for (const fact of facts) if (!seen.has(fact.id) && canAcknowledge(fact, publication, b.text)) {
                const upperMs = received - fact.lower;
                if (fact.phase !== 'warmup') r.metrics.lags.push(upperMs);
                seen.add(fact.id);
                emit('render-sample', { run: r.id, reader: b.reader, fact: fact.id, execution: fact.execution, phase: fact.phase, project: b.project, proof: fact.proof, upperMs, sourceAfterCommit: fact.upper, receipt: received, browser: b });
              }
              r.seen.set(b.reader, seen); result = { accepted: !facts.some(f => !f.error && !seen.has(f.id)) };
            }
          } else if (path === '/__interface/error') { if (run) run.errors.push(JSON.stringify(b)); emit('browser-error', b); result = { accepted: true }; }
          else if (path === '/__interface/status') result = run?.finish ?? { done: false, id: run?.id, submitted: run?.count, errors: run?.errors };
          else if (path === '/__interface/control') {
            const begin = stamp(), detail = await op('project_detail', { projectId: ids[0] });
            const readMs = stamp() - begin, t = stamp();
            const content = structuredClone(detail.content); content.settings.name = ids[0] + ' control ' + randomUUID().slice(0, 6);
            await op('project_update', { projectId: ids[0], requestId: randomUUID(), baseRevision: detail.revision, content });
            const ackMs = stamp() - t; run?.metrics.controls.push(ackMs); emit('control', { readMs, ackMs }); result = { readMs, ackMs };
          } else if (path === '/__interface/stop') { result = { stopped: true }; res.end(JSON.stringify(result)); void close(); return; }
          else throw Error('unknown measurement route');
          res.setHeader('content-type', 'application/json'); res.end(JSON.stringify(result)); return;
        }
        const target = new URL(c.base);
        const upstream = http.request({ hostname: target.hostname, port: target.port, method: req.method, path: req.url, headers: { ...req.headers, host: target.host, origin: c.base, authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test' } }, response => { res.writeHead(response.statusCode!, response.headers); response.pipe(res); });
        upstream.on('error', e => { errors.push(String(e)); if (!res.headersSent) res.writeHead(502); res.end(); }); req.pipe(upstream);
      } catch (e) { emit('request-error', { error: String(e), path: req.url }); res.writeHead(400, { 'content-type': 'application/json' }); res.end(JSON.stringify({ error: String(e) })); }
    });
    await new Promise<void>(r => server.listen(o.port, '127.0.0.1', r));
    console.log(JSON.stringify({ ready: true, port: (server.address() as any).port, projects: ids, out: o.out, development: o.development }));
  } catch (e) { await close(); throw e; }
}

async function parity(c: Coordinator, f: Fixture, out: string) {
  const expected = manifest(f).expected;
  const observed: Record<string, unknown> = {};
  for (const [id, p] of Object.entries(expected)) {
    const actual = JSON.parse(c.sql(`SELECT json_build_object('executions',(SELECT (header->>'executions')::bigint FROM resource_project_summary WHERE project_id=${literal(id)}),'metrics',(SELECT json_object_agg(metric,m) FROM (SELECT metric,sum(sum_value)::bigint AS sum,sum(present_count)::bigint present,sum(expected_count)::bigint expected FROM resource_metric WHERE project_id=${literal(id)} GROUP BY metric) m),'tools',(SELECT coalesce(json_object_agg(name,t),'{}') FROM (SELECT name,calls,errors FROM resource_tool WHERE project_id=${literal(id)}) t),'costs',(SELECT coalesce(json_agg(t),'[]') FROM (SELECT currency,basis,value::text,usages FROM resource_cost WHERE project_id=${literal(id)}) t))`).trim());
    observed[id] = actual;
    assert.equal(actual.executions, p.executions, id + ' inclusion');
    for (const [name, metric] of Object.entries(p.metrics)) { const m = actual.metrics[name]; assert.equal(Number(m?.sum ?? 0), metric.sum, id + ' sum ' + name); assert.equal(Number(m?.present ?? 0), metric.present, id + ' present ' + name); assert.equal(Number(m?.expected ?? 0), metric.expected, id + ' expected ' + name); }
    const actualTools = Object.fromEntries(Object.entries(actual.tools as Record<string, any>).map(([name, t]) => [name, { calls: Number(t.calls), errors: Number(t.errors) }]));
    assert.deepEqual(actualTools, p.tools, id + ' tools');
    for (const [key, cents] of Object.entries(p.directCostsCents)) { const [currency, basis] = key.split('/'); const group = actual.costs.find((x: any) => x.currency === currency && x.basis === basis); assert.equal(Math.round(Number(group?.value ?? 0) * 100), cents, id + ' cost ' + key); }
    const euros = actual.costs.find((x: any) => x.currency === 'EUR' && x.basis === 'estimated');
    assert.equal(Math.round(Number(euros?.value ?? 0) * 1e6), p.estimatedEURMicro, id + ' estimated EUR');
  }
  writeFileSync(resolve(out, 'parity.json'), JSON.stringify({ passed: true, expectedManifest: manifest(f).sha256, observed }, null, 2));
}

export async function browse(o: { origin: string; out: string; spec: Workload; placement: string }) {
  const url = new URL(o.origin);
  if (!['127.0.0.1', 'localhost'].includes(url.hostname) || url.protocol !== 'http:') throw Error('use a loopback SSH tunnel to isolated server, never production');
  mkdirSync(o.out, { recursive: true });
  const request = async (path: string, body?: object) => { const r = await fetch(o.origin + '/__interface/' + path, body ? { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) } : {}); const b = await r.json() as any; if (!r.ok) throw Error(JSON.stringify(b)); return b; };
  const config = await request('config'), spec = workload(o.spec, config.development);
  const placement = JSON.parse(readFileSync(o.placement, 'utf8'));
  if (!config.development && (hostname() === config.hostname || placement.separateMachine !== true || !placement.reservation)) throw Error('separate reserved Chromium machine required');
  const { chromium } = await import('playwright');
  const browser = await chromium.launch({ headless: true, args: ['--disable-background-timer-throttling', '--disable-renderer-backgrounding', '--disable-backgrounding-occluded-windows'] });
  const evidence: any = { spec, placement, hardware: hardware(), chromium: browser.version(), server: config, navigation: [], networkProbes: [] };
  const readers: { id: string; project: string; page: import('playwright').Page }[] = [];
  let controls: ReturnType<typeof setInterval> | undefined;
  try {
    for (let i = 0; i < spec.readers; i++) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
      const page = await context.newPage(), project = config.projects[i % config.projects.length], id = 'reader-' + i;
      const session = await context.newCDPSession(page);
      if (spec.network === '80ms-10Mbps') { await session.send('Network.enable'); await session.send('Network.emulateNetworkConditions', { offline: false, latency: 80, downloadThroughput: 10_000_000 / 8, uploadThroughput: 10_000_000 / 8 }); }
      page.on('pageerror', error => void request('error', { reader: id, type: 'pageerror', error: error.message }));
      page.on('response', response => { if (response.status() >= 400) void request('error', { reader: id, type: 'http', status: response.status(), path: new URL(response.url()).pathname }); });
      await page.addInitScript(() => { (window as any).__interfaceLongTasks = []; new PerformanceObserver(list => { for (const entry of list.getEntries()) (window as any).__interfaceLongTasks.push({ start: entry.startTime, duration: entry.duration }); }).observe({ type: 'longtask', buffered: true }); });
      const start = stamp(); await page.goto(o.origin + '/projects/' + project);
      await page.getByRole('button', { name: 'Save project', exact: true }).waitFor();
      const editableMs = stamp() - start;
      await page.waitForFunction(() => !!document.querySelector('[data-resource-project]')?.getAttribute('data-resource-version'));
      const summaryMs = stamp() - start;
      const warmStart = stamp(); await page.goto(o.origin + '/projects/' + project);
      await page.getByRole('button', { name: 'Save project', exact: true }).waitFor();
      const warmEditableMs = stamp() - warmStart;
      await page.waitForFunction(() => !!document.querySelector('[data-resource-project]')?.getAttribute('data-resource-version'));
      await page.getByRole('button', { name: /^Tools ·/ }).click();
      await page.getByText(config.marker, { exact: true }).waitFor();
      const visible = await page.evaluate(() => document.visibilityState);
      assert.equal(visible, 'visible', 'each reader must be foreground');
      evidence.navigation.push({ reader: id, coldEditableMs: editableMs, warmEditableMs, firstSummaryMs: summaryMs, initialVisible: visible });
      // Clock brackets include actual round-trip. No clock-offset estimate is subtracted from lag.
      const t0 = stamp(), clock = await request('clock'), t1 = stamp();
      const shaped = await page.evaluate(async () => { const before = performance.now(), c = await (await fetch('/__interface/clock')).json(); return { before, after: performance.now(), server: c }; });
      evidence.networkProbes.push({ reader: id, t0, t1, clock, uncertaintyMs: t1 - t0, shaped, impairment: spec.network === 'unshaped' ? 'none' : 'CDP additional latency80ms,10Mbps each reader; actual network RTT is retained, not relabeled80ms total' });
      readers.push({ id, project, page });
    }
    const { id: run } = await request('start', { spec, readers: readers.map(({ id, project }) => ({ id, project })), browser: evidence });
    for (const reader of readers) {
      await reader.page.evaluate(({ run, reader, project, marker }) => {
        let previous = '', sending = false, inEnvelope = true;
        const observe = () => {
          const win = document.querySelector('[data-resource-window="tools"]');
          const label = [...(win?.querySelectorAll('span') ?? [])].find(x => x.textContent === marker);
          const row = label?.closest('[data-key]'), text = row?.querySelector('.num')?.textContent ?? '';
          const value = /^(\d+) · (\d+) failed$/.exec(text), version = win?.getAttribute('data-resource-version');
          const visible = !!row && document.visibilityState === 'visible' && row.getBoundingClientRect().height > 0 && row.getBoundingClientRect().top >= 0 && row.getBoundingClientRect().bottom <= innerHeight;
          if (!visible && inEnvelope) void fetch('/__interface/error', { method: 'POST', body: JSON.stringify({ reader, type: 'left-connected-foreground-envelope', visibility: document.visibilityState, rowPresent: !!row }) });
          inEnvelope = visible;
          if (!value || !version || !visible || version + '/' + text === previous || sending) return;
          const n = Number(value[1]); sending = true;
          requestAnimationFrame(() => requestAnimationFrame(() => {
            // Re-read the painted DOM; don't acknowledge an obsolete candidate or an attribute alone.
            const actual = row?.querySelector('.num')?.textContent ?? '';
            if (actual !== text || win?.getAttribute('data-resource-version') !== version || !row?.isConnected || label?.textContent !== marker || document.visibilityState !== 'visible' || row.getBoundingClientRect().top < 0 || row.getBoundingClientRect().bottom > innerHeight) { sending = false; observe(); return; }
            fetch('/__interface/render', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ run, reader, project, value: n, errors: Number(value[2]), text: actual, version, visible, browserMonotonic: performance.now(), browserWall: Date.now(), domNodes: document.querySelectorAll('*').length, longTasks: (window as any).__interfaceLongTasks.splice(0) }) }).then(r => { if (!r.ok) throw Error('render receipt rejected'); return r.json(); }).then(result => { if (result.accepted) previous = version + '/' + text; }).catch(error => { void fetch('/__interface/error', { method: 'POST', body: JSON.stringify({ reader, error: String(error) }) }); }).finally(() => { sending = false; });
          }));
        };
        new MutationObserver(observe).observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true });
        document.addEventListener('visibilitychange', observe);
        setInterval(observe, 50); observe();
      }, { run, reader: reader.id, project: reader.project, marker: config.marker });
    }
    let controlling = false;
    controls = setInterval(() => { if (controlling) return; controlling = true; void request('control').catch(e => request('error', { error: String(e), type: 'control' })).finally(() => { controlling = false; }); }, 1000);
    let result;
    for (;;) { await sleep(1000); result = await request('status'); if (result.status) break; }
    clearInterval(controls); controls = undefined;
    await readers[0].page.screenshot({ path: resolve(o.out, 'rendered.png') });
    evidence.result = result; writeFileSync(resolve(o.out, 'browser.json'), JSON.stringify(evidence, null, 2));
    console.log(JSON.stringify(result));
    if (!['passed', 'recovered'].includes(result.status)) process.exitCode = 1;
  } finally { if (controls) clearInterval(controls); await browser.close(); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = Object.fromEntries(process.argv.slice(3).map(a => { const [k, v] = a.replace(/^--/, '').split('='); return [k, v ?? 'true']; }));
  const out = resolve(args.out ?? '.egm/interface-performance-run');
  if (process.argv[2] === 'serve') await serve({ out, history: Number(args.history ?? 618), projects: Number(args.projects ?? 1), cardinality: Number(args.cardinality ?? 3), longEvents: Number(args['long-events'] ?? 0), port: Number(args.port ?? 9479), development: args.development === 'true', placement: args.placement });
  else if (process.argv[2] === 'browse') await browse({ origin: args.origin ?? 'http://127.0.0.1:9479', out, placement: args.placement, spec: { readers: Number(args.readers ?? 1), rate: Number(args.rate ?? 1), warmup: Number(args.warmup ?? 60), duration: Number(args.duration ?? 600), burst: Number(args.burst ?? 1000), recovery: Number(args.recovery ?? 60), sourceDistribution: (args['source-distribution'] ?? 'hot') as SourceDistribution, mode: args.mode as Workload['mode'] ?? 'connected', network: args.network as Workload['network'] ?? 'unshaped' } });
  else throw Error('usage: interface.ts serve|browse --placement=<json> --out=<ignored directory> [--key=value]');
}
