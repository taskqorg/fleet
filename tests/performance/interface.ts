// Isolated, two-machine qualification. No arbitrary origin/database target is accepted.
import assert from 'node:assert/strict';
import {Writer} from './sql-writer-interface.ts';
import {installEventSourceCapture,EventSourceRecorder} from './eventsource-interface.ts';
import { execFileSync, spawn } from 'node:child_process';
import { createHash, randomUUID, X509Certificate } from 'node:crypto';
import { appendFileSync, createWriteStream, existsSync, mkdirSync, readFileSync, writeFileSync, renameSync } from 'node:fs';
import http from 'node:http';
import http2 from 'node:http2';
import https from 'node:https';
import { cpus, freemem, hostname, platform, totalmem } from 'node:os';
import { resolve } from 'node:path';
import { once } from 'node:events';
import { pipeline, finished } from 'node:stream/promises';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';
import { fixture, literal, manifest, marker, pricesSQL, projectIDs, seedSQL, type Fixture } from './seed-interface.ts';
import { startCoordinator, type Coordinator } from '../harness.ts';
import { PublicationHistory, ObservationBook, RenderBounds, journalVersion, installPublicationJournalSQL, publicationJournalSQL, publicationAnchorsSQL, publicationNotificationsSQL, type JournalPublication, type RenderObservation, type LeafState } from './publication-proof.ts';

const sleep = (ms: number) => new Promise<void>(r => setTimeout(r, ms));
const stamp = () => performance.now();
// An error can precede a later page query; never wait for drain on an already failed stream.
async function writeEvidenceLine(stream: ReturnType<typeof createWriteStream>, line: string) {
  const check = () => { if (stream.errored) throw stream.errored; if (stream.destroyed || stream.writableEnded) throw Error('export stream unavailable: ' + String(stream.path)); };
  check();
  if (!stream.write(line)) { check(); await once(stream, 'drain'); }
}

// Stream the actual private-database statistics; query text may exceed spawnSync's1MiB buffer.
async function exportQueryStatistics(database: string, file: string) {
  const partial = file + '.partial', output = createWriteStream(partial, { flags: 'wx' });
  const child = spawn('psql', ['-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-h', process.env.FLEET_TRIAL_PGDATA!, '-p', process.env.FLEET_TRIAL_PGPORT ?? '54329', '-U', 'fleet', '-d', database], { stdio: ['pipe', 'pipe', 'pipe'] });
  let stderr = '';
  child.stderr.on('data', b => { stderr = (stderr + String(b)).slice(-4096); });
  const exited = new Promise<void>((resolve, reject) => { child.once('error', reject); child.once('close', code => code === 0 ? resolve() : reject(Error('query-statistics psql exit ' + code + ' (stderr tail, at most4096 characters): ' + stderr))); });
  child.stdin.on('error', () => {});
  child.stdin.end("SELECT coalesce(json_agg(s),'[]') FROM (SELECT * FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) ORDER BY total_exec_time DESC) s");
  try { await Promise.all([pipeline(child.stdout, output), exited]); renameSync(partial, file); }
  catch (e) { child.kill('SIGTERM'); await exited.catch(() => {}); throw e; }
}

const command = (name: string, args: string[]) => { try { return execFileSync(name, args, { encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 }).trim(); } catch { return null; } };
export function stats(values: number[]) {
  const v = values.toSorted((a, b) => a - b);
  const q = (p: number) => v.length ? v[Math.max(0, Math.ceil(p * v.length) - 1)] : null;
  return { count: v.length, min: q(0), p50: q(.5), p95: q(.95), p99: q(.99), max: q(1), violations: v.filter(x => x > 5000).length };
}
export function hasRenderFailure(mode: 'connected' | 'exceed-capacity' | 'controlled-burst', lags: number[], errors: number, missing: number) { return !lags.length || errors > 0 || missing > 0 || mode !== 'exceed-capacity' && lags.some(x => x > 5000); }
export type SourceDistribution = 'hot' | 'distinct';
export type Workload = { sourceDistribution: SourceDistribution; readers: number; rate: number; warmup: number; duration: number; burst: number; recovery: number; mode: 'connected' | 'exceed-capacity' | 'controlled-burst'; network: 'unshaped' | '80ms-10Mbps' };
export type SourcePhase = 'warmup' | 'sustained' | 'burst';
export type PhaseWindow = { phase: SourcePhase; plannedStart: number; plannedEnd: number; actualStart: number; actualEnd: number | null; startOvershootMs: number; endOvershootMs: number | null; offered: number; expected: number; offering: 'paced' | 'early' };

// Fixed before qualification: pace each second's requested facts over its first800ms.
// The original1000ms windows and conservative confirmation validator stay unchanged.
export const sourcePacing = { version: 1, offeredSpanMs: 800, minimumTailHeadroomMs: 200 } as const;

// One origin for all phases. Clock injection tests the actual deadline control flow without
// sleeping for a600-second trial; runtime uses the normal monotonic clock and timer.
export async function scheduleSourcePhases(spec: Workload, offer: (phase: SourcePhase, due: number) => void, observe: (event: 'start' | 'end', window: PhaseWindow) => void, clock = { now: stamp, sleep }) {
  const origin = clock.now(), windows: PhaseWindow[] = [];
  const until = async (deadline: number) => { while (clock.now() < deadline) await clock.sleep(deadline - clock.now()); };
  for (const [phase, offset, seconds] of [['warmup', 0, spec.warmup], ['sustained', spec.warmup, spec.duration], ['burst', spec.warmup + spec.duration, 1]] as const) {
    const plannedStart = origin + offset * 1000, plannedEnd = plannedStart + seconds * 1000;
    await until(plannedStart);
    const actualStart = clock.now(), expected = phase === 'burst' ? spec.burst : seconds * spec.rate;
    const window: PhaseWindow = { phase, plannedStart, plannedEnd, actualStart, actualEnd: null, startOvershootMs: Math.max(0, actualStart - plannedStart), endOvershootMs: null, offered: 0, expected, offering: phase === 'burst' ? 'early' : 'paced' };
    windows.push(window); observe('start', { ...window });
    for (let i = 0; i < expected; i++) {
      const due = phase === 'burst' ? plannedStart : plannedStart + Math.floor(i / spec.rate) * 1000 + (i % spec.rate) * sourcePacing.offeredSpanMs / spec.rate;
      await until(due); offer(phase, due); window.offered++;
    }
    await until(plannedEnd);
    window.actualEnd = clock.now(); window.endOvershootMs = Math.max(0, window.actualEnd - plannedEnd);
    observe('end', { ...window });
  }
  return windows;
}
export function workload(x: Partial<Workload>, development = false): Workload {
  const w: Workload = { sourceDistribution: 'hot', readers: 1, rate: 1, warmup: 60, duration: 600, burst: 1000, recovery: 60, mode: 'connected', network: 'unshaped', ...x };
  for (const k of ['readers', 'rate', 'warmup', 'duration', 'burst', 'recovery'] as const) if (!Number.isInteger(w[k]) || w[k] < 0) throw Error('invalid workload ' + k);
  if (!['hot', 'distinct'].includes(w.sourceDistribution) || !['connected', 'exceed-capacity', 'controlled-burst'].includes(w.mode) || !['unshaped', '80ms-10Mbps'].includes(w.network)) throw Error('invalid classification');
  if (w.mode === 'controlled-burst') { if (development || w.sourceDistribution !== 'distinct' || w.readers !== 1 || w.rate !== 1 || w.warmup !== 3 || w.duration !== 0 || w.burst !== 1000 || w.recovery !== 10) throw Error('controlled-burst is a separate fixed diagnostic, not development or full qualification'); }
  else if (development) { if (w.readers !== 1 || w.rate > 1 || w.warmup > 2 || w.duration > 3 || w.burst > 1 || w.recovery > 5) throw Error('development is a small smoke only'); }
  else if (![1, 10, 50].includes(w.readers) || (w.mode === 'connected' && ![1, 20, 100].includes(w.rate)) || w.rate < 1 || w.warmup !== 60 || w.duration !== 600 || w.burst !== 1000 || w.recovery < 60) throw Error('outside declared envelope; classify exceed-capacity separately');
  return w;
}

// Diagnostic history sizes are explicit declared points, never a full-envelope waiver.
export function checkSourceFixture(mode: Workload['mode'], f: Fixture, longEvents = 0) {
  if (mode === 'controlled-burst' && (![618, 100_000].includes(f.history) || f.projects !== 1 || longEvents)) throw Error('controlled-burst requires history618 or100000, one project and no long-native events');
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
  const files = ['tests/performance/interface.ts', 'tests/performance/seed-interface.ts', 'tests/performance/publication-proof.ts','tests/performance/eventsource-interface.ts','tests/performance/sql-writer-interface.ts', 'tests/harness.ts', 'app/src/api.ts'];
  return { sha, dirty: status, diffSHA256: createHash('sha256').update(command('git', ['diff', 'HEAD']) ?? '').digest('hex'), sourceFiles: Object.fromEntries(files.map(p => [p, createHash('sha256').update(readFileSync(p)).digest('hex')])), binarySHA256: createHash('sha256').update(readFileSync(binary)).digest('hex'), builtAssets: command('sh', ['-c', 'find dist/app -type f -name "*.js" -exec shasum -a 256 {} \\;']) };
}

export type Publication = { epoch: string; revision: string; calls: number; errors: number };
export type Fact = { id: string; execution: string; project: string; n: number; calls: number; errors: number; lower: number; upper: number | null; phase: string; due: number; transaction?: string; error?: string; proof?: Publication };
// Both endpoints use performance.now() in this process. Round outward by one millisecond
// per endpoint; neither SQL wall clocks nor browser clock offsets are subtracted.
export const sourceClockUncertaintyMs = 1;
export function sourceRate(facts: Fact[], spec: Workload, uncertaintyMs = sourceClockUncertaintyMs, phaseWindows: PhaseWindow[] = []) {
  const origin = phaseWindows[0]?.plannedStart;
  const windowsValid = phaseWindows.length === 3 && phaseWindows.every((w, i) => {
    const phase = (['warmup', 'sustained', 'burst'] as const)[i], offset = i === 0 ? 0 : i === 1 ? spec.warmup : spec.warmup + spec.duration;
    const seconds = i === 0 ? spec.warmup : i === 1 ? spec.duration : 1;
    return w.phase === phase && Number.isFinite(origin) && [w.plannedStart, w.plannedEnd, w.actualStart, w.actualEnd, w.startOvershootMs, w.endOvershootMs].every(v => v !== null && Number.isFinite(v)) &&
      w.plannedStart === origin + offset * 1000 && w.plannedEnd === w.plannedStart + seconds * 1000 && w.actualStart >= w.plannedStart && w.actualEnd! >= w.plannedEnd && w.actualEnd! >= w.actualStart &&
      w.startOvershootMs === w.actualStart - w.plannedStart && w.endOvershootMs === w.actualEnd! - w.plannedEnd &&
      (i === 0 || w.actualStart >= phaseWindows[i - 1].actualEnd!) && w.expected === (phase === 'burst' ? spec.burst : seconds * spec.rate) && w.offered === w.expected && w.offering === (phase === 'burst' ? 'early' : 'paced');
  });
  const phases = (['sustained', 'burst'] as const).map(phase => {
    const window = phaseWindows.find(w => w.phase === phase);
    const rows = facts.filter(f => f.phase === phase), expected = phase === 'burst' ? spec.burst : spec.rate * spec.duration;
    const confirmed = rows.filter(f => !f.error && f.upper !== null && f.transaction);
    const budgetMs = phase === 'burst' ? 1000 : spec.duration * 1000;
    const lower = rows.length ? rows.reduce((v, f) => Math.min(v, f.lower), Infinity) : null;
    const upper = confirmed.length ? confirmed.reduce((v, f) => Math.max(v, f.upper!), -Infinity) : null;
    const spanMs = !window || upper === null ? null : upper - window.plannedStart + 2 * uncertaintyMs;
    // Count mutations/transactions independently: an aggregate counter delta is not a fact.
    const identitiesValid = new Set(rows.map(f => f.id)).size === expected && new Set(confirmed.map(f => f.transaction)).size === expected;
    const ordered = rows.toSorted((a, b) => a.due - b.due);
    const windows = phase === 'sustained' ? Array.from({ length: spec.duration }, (_, second) => {
      const bucket = ordered.slice(second * spec.rate, (second + 1) * spec.rate);
      const spanMs = bucket.length ? Math.max(...bucket.map(f => f.upper ?? Infinity)) - Math.min(...bucket.map(f => f.lower)) + 2 * uncertaintyMs : null;
      const plannedStart = window ? window.plannedStart + second * 1000 : null, plannedEnd = plannedStart === null ? null : plannedStart + 1000;
      const withinOriginalWindow = plannedStart !== null && bucket.every(f => f.lower >= plannedStart && f.upper !== null && f.upper + 2 * uncertaintyMs <= plannedEnd!);
      return { second, count: bucket.length, spanMs, plannedStart, plannedEnd, lastPlannedOffer: bucket.length ? Math.max(...bucket.map(f => f.due)) : null, lastSubmitted: bucket.length ? Math.max(...bucket.map(f => f.lower)) : null, lastConfirmed: bucket.length ? Math.max(...bucket.map(f => f.upper ?? Infinity)) : null, confirmationTailHeadroomMs: plannedEnd === null || !bucket.length ? null : plannedEnd - Math.max(...bucket.map(f => f.upper ?? Infinity)) - 2 * uncertaintyMs, valid: bucket.length === spec.rate && spanMs !== null && spanMs <= 1000 && withinOriginalWindow };
    }) : [];
    const notRun = spec.mode === 'controlled-burst' && phase === 'sustained' && expected === 0 && rows.length === 0;
    const valid = notRun ? windowsValid && Number.isFinite(uncertaintyMs) && uncertaintyMs >= 0 : windowsValid && windows.every(w => w.valid) && Number.isFinite(uncertaintyMs) && uncertaintyMs >= 0 && expected > 0 && rows.length === expected && confirmed.length === expected && identitiesValid && spanMs !== null && spanMs <= budgetMs && confirmed.every(f => Number.isFinite(f.lower) && Number.isFinite(f.upper) && f.upper! >= f.lower && f.lower >= window!.plannedStart);
    return { phase, notRun, expected, offered: rows.length, admitted: confirmed.length, confirmed: confirmed.length, transactions: new Set(confirmed.map(f => f.transaction)).size, lower, upper, windowStart: window?.plannedStart ?? null, windowEnd: window?.plannedEnd ?? null, spanMs, budgetMs, windows, minimumConfirmedPerSecond: spanMs && spanMs > 0 ? confirmed.length * 1000 / spanMs : null, valid };
  });
  const unique = new Set(facts.map(f => f.id)).size === facts.length && new Set(facts.flatMap(f => f.transaction ? [f.transaction] : [])).size === facts.filter(f => f.transaction).length;
  return { pacing: sourcePacing, status: unique && windowsValid && phases.every(p => p.valid) ? 'qualified' : 'invalid-source-rate', uncertaintyMs, clock: 'same-process monotonic outward brackets from original phase start, includes submission/queue/commit receipt', phaseWindows, phaseWindowsValid: windowsValid, phases };
}
export const publicationVersion = journalVersion;
export function sourceMutation(fact: Fact, distribution: SourceDistribution) {
  const totals = `jsonb_build_object('byTool',jsonb_build_object(${literal(marker)},jsonb_build_object('calls',${fact.calls},'errors',${fact.errors})))`;
  const mutation = distribution === 'hot'
    ? `UPDATE execution_totals SET tool_calls=${totals} WHERE execution_id=${literal(fact.execution)};`
    : `INSERT INTO execution(id,kind,state,project_id,started_at,ended_at,harness,model) VALUES(${literal(fact.execution)},'agent_run','succeeded',${literal(fact.project)},'2024-01-01 00:00:00+00','2024-01-01 00:00:01+00','codex','interface-seed1-model-0'); INSERT INTO execution_totals(execution_id,project_id,tool_calls,tool_calls_basis) VALUES(${literal(fact.execution)},${literal(fact.project)},${totals},'measured');`;
  return mutation;
}
export function sourceTransaction(fact: Fact, distribution: SourceDistribution) {
  return `BEGIN; ${sourceMutation(fact, distribution)}
SELECT pg_current_xact_id()::text AS source_tx, clock_timestamp()::text AS before_commit \\gset
COMMIT;`;
}
type SampleBound = {fact: Fact; reader: string; browser: RenderObservation; publication: {epoch:string;revision:string;calls:number;errors:number}; leaf: LeafState};
type Run = { id: string; spec: Workload; start: number; done: boolean; abort: AbortController; producer?: Promise<void>; finalization?: Promise<void>; phaseWindows: PhaseWindow[]; facts: Map<string, Fact[]>; seen: Map<string, Set<string>>; history: PublicationHistory; observations: ObservationBook; bounds: RenderBounds<SampleBound>; processedReceipt: Map<string,number>; unseen: Map<string, Map<string, Fact>>; metrics: { lags: number[]; writes: number[]; schedule: number[]; controls: number[] }; errors: string[]; readers: Map<string, { project: string; visible: boolean }>; count: number; finish?: unknown };
const pgSnapshot = (c: Coordinator) => JSON.parse(c.sql(`SELECT json_build_object('at',clock_timestamp(),'database',current_database(),'pg',version(),'settings',(SELECT json_object_agg(name,setting) FROM pg_settings WHERE name IN ('shared_buffers','work_mem','max_connections','fsync','synchronous_commit','track_io_timing','track_commit_timestamp','max_wal_size','effective_cache_size')),'databaseStats',(SELECT row_to_json(s) FROM pg_stat_database s WHERE datname=current_database()),'activity',(SELECT coalesce(json_agg(a),'[]') FROM (SELECT pid,state,wait_event_type,wait_event,query_start,left(query,250) query FROM pg_stat_activity WHERE datname=current_database()) a),'backlog',(SELECT count(*) FROM resource_work),'oldestPending',(SELECT min(first_pending_at) FROM resource_work),'builds',(SELECT coalesce(json_agg(b),'[]') FROM resource_build b))`).trim());

// Production serves the app over HTTP/2 (Cloudflare). `h2` gives the measurement origin the same multiplexed transport
// with an ephemeral self-signed loopback certificate; `h1` keeps the HTTP/1.1 per-origin socket queue of earlier cases.
export type Transport = 'h1' | 'h2';
const HOP_BY_HOP = new Set(['connection', 'keep-alive', 'proxy-connection', 'transfer-encoding', 'upgrade', 'http2-settings', 'te', 'host']);
/** Drop pseudo, hop-by-hop and Connection-listed headers (RFC 9110 7.6.1), which HTTP/2 forbids. */
const endToEnd = (headers: http.IncomingHttpHeaders) => {
  const listed = new Set(String(headers.connection ?? '').split(',').map(x => x.trim().toLowerCase()).filter(Boolean));
  return Object.fromEntries(Object.entries(headers).filter(([k]) => !k.startsWith(':') && !HOP_BY_HOP.has(k.toLowerCase()) && !listed.has(k.toLowerCase())));
};
/**
 * Forward one origin request to the coordinator over HTTP/1.1. HTTP/1.1 requests and answers pass unchanged; HTTP/2 ones
 * lose only connection-specific headers. A closed client stream (an EventSource closed, a page navigated) also ends
 * the coordinator request, so no abandoned subscription keeps streaming.
 */
export function proxyRequest(req: http.IncomingMessage, res: http.ServerResponse, base: string, extra: Record<string, string>, onError: (e: Error) => void) {
  const target = new URL(base), h2 = req.httpVersionMajor === 2;
  let answer: http.IncomingMessage | null = null, closed = false;
  const upstream = http.request({ hostname: target.hostname, port: target.port, method: req.method, path: req.url, headers: { ...(h2 ? endToEnd(req.headers) : req.headers), host: target.host, origin: base, ...extra } }, response => { answer = response; res.writeHead(response.statusCode!, h2 ? endToEnd(response.headers) : response.headers); response.pipe(res); });
  // HTTP/2 compatibility marks a reset stream's response finished, so completeness is read from the coordinator answer.
  res.on('close', () => { closed = true; if (!answer?.complete) upstream.destroy(); });
  upstream.on('error', e => { if (closed) return; // the client went away first; not a coordinator failure
    onError(e); if (!res.headersSent) res.writeHead(502); res.end(); }); req.pipe(upstream);
}
export function ephemeralCertificate(out: string) {
  const key = resolve(out, 'origin-key.pem'), cert = resolve(out, 'origin-cert.pem');
  execFileSync('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-days', '2', '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost', '-keyout', key, '-out', cert], { stdio: ['ignore', 'ignore', 'pipe'] }); // a failure carries openssl's stderr
  const pem = readFileSync(cert); return { key: readFileSync(key), cert: pem, fingerprint256: new X509Certificate(pem).fingerprint256 };
}
export async function serve(o: { out: string; history: number; projects: number; cardinality: number; port: number; development: boolean; placement: string; longEvents?: number; transport?: Transport }) {
  const transport: Transport = o.transport ?? 'h1';
  if (!['h1', 'h2'].includes(transport)) throw Error('transport must be h1 or h2');
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
  const tls = transport === 'h2' ? ephemeralCertificate(o.out) : null; // before any process starts: a failure leaves nothing running
  const baselineManifest = manifest(f), sourceCandidate = candidate();
  writeFileSync(resolve(o.out, 'manifest.json'), JSON.stringify(baselineManifest, null, 2));
  writeFileSync(resolve(o.out, 'server.json'), JSON.stringify({ at: new Date().toISOString(), development: o.development, placement, hardware: hardware(), candidate: sourceCandidate, clock: 'server monotonic milliseconds; lower before source submission, upper after commit; render receipt is a conservative actual-render upper bound including return network' }, null, 2));
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
  const server: http.Server | http2.Http2SecureServer = tls ? http2.createSecureServer({ key: tls.key, cert: tls.cert, allowHTTP1: true }) : http.createServer();
  const sessions = new Set<http2.ServerHttp2Session>();
  if (tls) (server as http2.Http2SecureServer).on('session', session => { sessions.add(session); session.once('close', () => sessions.delete(session)); });
  let monitor: ReturnType<typeof setInterval> | undefined, closing = false;
  const close = async () => {
    if (closing) return; closing = true;
    if (monitor) clearInterval(monitor);
    if (run) {
      if (!run.done && !run.finalization) run.abort.abort();
      await run.producer;
      await run.finalization;
    }
    server.close(); for (const session of sessions) session.destroy(); if (!tls) (server as http.Server).closeAllConnections(); await Promise.all(writers.map(w=>w.close())); await c.stop(); sink.close();
    execFileSync('pg_ctl', ['-D', pgdata, '-m', 'fast', '-w', 'stop'], { stdio: 'ignore' });
    log.end(); sqlLog.end(); spans.end();
  };
  const closeSafely = () => void close().catch(e => console.error('fixture cleanup failed:', e));
  process.once('SIGTERM', closeSafely); process.once('SIGINT', closeSafely);
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
    c.sql(installPublicationJournalSQL(ids, marker));
    const anchors: JournalPublication[] = JSON.parse(c.sql('SELECT ' + publicationAnchorsSQL));
    assert.equal(anchors.length, ids.length, 'all fixture projects need a ready journal anchor');
    writeFileSync(resolve(o.out, 'journal-anchors.json'), JSON.stringify(anchors, null, 2));
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
    const acknowledge = (r: Run, reader: string, fact: Fact) => {
      if (fact.upper === null || fact.error) return;
      const entry = r.bounds.confirm(reader + '/' + fact.id); if (!entry) return;
      const {browser, publication, leaf} = entry.value, seen = r.seen.get(reader) ?? new Set<string>();
      const correction = seen.has(fact.id);
      fact.proof ??= publication; seen.add(fact.id); r.seen.set(reader, seen); r.unseen.get(reader)!.delete(fact.id);
      emit(correction ? 'render-sample-correction' : 'render-sample', {run:r.id,reader,fact:fact.id,execution:fact.execution,phase:fact.phase,project:fact.project,proof:publication,leaf,upperMs:entry.receipt-fact.lower,sourceAfterCommit:fact.upper,receipt:entry.receipt,proofAcknowledged:stamp(),observation:browser.observation,browser});
    };
    const writeFact = async (r: Run, index: number, phase: string, due: number) => {
      if (r.abort.signal.aborted) return;
      const project = ids[index % ids.length], list = r.facts.get(project)!;
      const n = list.length + 1, execution = r.spec.sourceDistribution === 'hot' ? project + '-marker' : `${project}-${r.id}-leaf-${index}`;
      const fact: Fact = { id: `${r.id}-${index}`, execution, project, n, calls: r.spec.sourceDistribution === 'hot' ? n : 1, errors: r.spec.sourceDistribution === 'distinct' && index % 7 === 0 ? 1 : 0, lower: stamp(), upper: null, phase, due }; list.push(fact); r.count++;
      for (const [reader, state] of r.readers) if (state.project === project) r.unseen.get(reader)!.set(fact.id, fact);
      emit('source-submitted', { run: r.id, sourceDistribution: r.spec.sourceDistribution, ...fact });
      try {
        if (native && project === ids[0]) await ingest([usage(++native.seq)]);
        const receipt = await sourceWriters[index % sourceWriters.length].run(sourceTransaction(fact, r.spec.sourceDistribution), `json_build_object('transaction',:'source_tx','beforeCommit',:'before_commit')`);
        fact.upper = stamp(); fact.transaction = receipt.payload.transaction;
        r.metrics.writes.push(fact.upper - fact.lower); r.metrics.schedule.push(fact.lower - due); emit('confirmed', { run: r.id, sourceDistribution: r.spec.sourceDistribution, canonicalMutations: r.spec.sourceDistribution === 'hot' ? 1 : 2, ...fact, receipt });
        for (const [reader,state] of r.readers) if (state.project === project) acknowledge(r,reader,fact);
      } catch (e) { fact.error = String(e); r.errors.push(String(e)); emit('source-error', { run: r.id, ...fact }); }
    };
    // Read immutable committed publication history, including OLD painted versions.
    const proofReads = new Map<string, Promise<void>>();
    const prove = (r: Run, project: string, version: string) => {
      const v = publicationVersion(version)!;
      const known = r.history.latest(project, v.epoch);
      if (known !== null && BigInt(known) >= BigInt(v.revision)) return Promise.resolve();
      const key = project + '/' + v.epoch, active = proofReads.get(key); if (active) return active;
      const task = (async () => {
        for (;;) {
          const before = r.history.latest(project, v.epoch);
          const receipt = await observer.run('', publicationJournalSQL(project, v.epoch, before));
          const rows = receipt.payload as JournalPublication[];
          for (const row of rows) r.history.ingest(row);
          emit('publication-history', { run: r.id, project, requestedVersion: version, rows, receipt });
          const failure = r.history.epochs.get(key)?.invalid;
          if (failure) { const error = 'journal:' + key + ':' + failure; if (!r.errors.includes(error)) r.errors.push(error); break; }
          const after = r.history.latest(project, v.epoch);
          if (rows.length < 1000 || after !== null && BigInt(after) >= BigInt(v.revision)) break;
          if (after === before) throw Error('journal history did not advance');
        }
      })().finally(() => proofReads.delete(key));
      proofReads.set(key, task); return task;
    };
    const finishOwned = async (r: Run) => {
      await Promise.allSettled([...proofReads.values()]);
      const missing: object[] = [];
      for (const [reader, { project }] of r.readers) {
        const seen = r.seen.get(reader) ?? new Set<string>();
        for (const fact of r.unseen.get(reader)!.values()) if (!seen.has(fact.id) && !fact.error && fact.phase !== 'warmup') missing.push({ reader, project, n: fact.n, age: stamp() - fact.lower });
      }
      const allFacts = [...r.facts.values()].flat();
      const expected = ids.map(project => {
        const facts = r.facts.get(project)!.filter(f => !f.error && f.upper !== null);
        const calls = r.spec.sourceDistribution === 'hot' ? facts.at(-1)?.calls ?? 0 : facts.reduce((s, f) => s + f.calls, 0), errors = facts.reduce((s, f) => s + f.errors, 0);
        return { project, tools: { ...baselineManifest.expected[project].tools, [marker]: { calls, errors } }, totalCalls: baselineManifest.expected[project].metrics.toolCalls.sum + calls, totalErrors: baselineManifest.expected[project].metrics.toolErrors.sum + errors, calls: r.spec.sourceDistribution === 'hot' ? facts.at(-1)?.calls ?? 0 : facts.reduce((s, f) => s + f.calls, 0), errors: facts.reduce((s, f) => s + f.errors, 0), executions: baselineManifest.expected[project].executions + 1 + (r.spec.sourceDistribution === 'distinct' ? facts.length : 0) };
      });
      const recovery = expected.map(e => ({ ...e, observed: JSON.parse(c.sql(`SELECT json_build_object('calls',coalesce((SELECT calls FROM resource_tool WHERE project_id=${literal(e.project)} AND name=${literal(marker)} AND connection=''),0),'errors',coalesce((SELECT errors FROM resource_tool WHERE project_id=${literal(e.project)} AND name=${literal(marker)} AND connection=''),0),'tools',(SELECT coalesce(json_object_agg(name,v),'{}') FROM (SELECT name,sum(calls)::bigint calls,sum(errors)::bigint errors FROM resource_tool WHERE project_id=${literal(e.project)} GROUP BY name) v),'totalCalls',(SELECT sum(sum_value) FROM resource_metric WHERE project_id=${literal(e.project)} AND metric='toolCalls'),'totalErrors',(SELECT sum(sum_value) FROM resource_metric WHERE project_id=${literal(e.project)} AND metric='toolErrors'),'executions',(SELECT executions FROM resource_project_summary WHERE project_id=${literal(e.project)}),'pending',(SELECT count(*) FROM resource_work_scope WHERE project_id=${literal(e.project)}))`)) }));
      for (const x of recovery) if (x.calls !== x.observed.calls || x.errors !== x.observed.errors || x.executions !== x.observed.executions || x.totalCalls !== x.observed.totalCalls || x.totalErrors !== x.observed.totalErrors || Object.keys(x.tools).length !== Object.keys(x.observed.tools).length || Object.entries(x.tools).some(([name, t]) => t.calls !== x.observed.tools[name]?.calls || t.errors !== x.observed.tools[name]?.errors) || x.observed.pending) r.errors.push('recovery/double-count mismatch: ' + JSON.stringify(x));
      r.metrics.lags = [...r.bounds.entries.values()].filter(x => x.confirmed && x.value.fact.phase !== 'warmup').map(x => x.receipt-x.value.fact.lower);
      if (!r.metrics.lags.length) r.errors.push('no measured connected foreground samples');
      const expectedSamples = [...r.readers.values()].reduce((n, reader) => n + r.facts.get(reader.project)!.filter(f => !f.error && f.phase !== 'warmup').length, 0);
      if (r.metrics.lags.length + missing.length !== expectedSamples) r.errors.push('render sample count mismatch');
      let journalStats: unknown = null;
      const journalErrors: string[] = [];
      const journalPath = resolve(o.out, 'publication-journal.jsonl'), notificationPath = resolve(o.out, 'publication-notifications.jsonl');
      const journalFile = createWriteStream(journalPath + '.partial', { flags: 'wx' });
      const notificationFile = createWriteStream(notificationPath + '.partial', { flags: 'wx' });
      const journalClosed = Promise.allSettled([finished(journalFile), finished(notificationFile)]);
      let notificationLinks=0;
      try {
        await Promise.all([once(journalFile, 'open'), once(notificationFile, 'open')]);
        const scopes = await observer.run('', `(SELECT coalesce(jsonb_agg(p),'[]'::jsonb) FROM (SELECT DISTINCT project,epoch FROM interface_publication ORDER BY project,epoch) p)`);
        for (const scope of scopes.payload) {
          let cursor: string | null = null;
          for (;;) {
            const page = await observer.run('', publicationJournalSQL(scope.project, scope.epoch, cursor));
            for (const row of page.payload as JournalPublication[]) await writeEvidenceLine(journalFile, JSON.stringify(row) + '\n');
            if(page.payload.length){
              const links=await observer.run('',publicationNotificationsSQL(scope.project,page.payload.map((row:JournalPublication)=>row.transaction)));
              for(const row of links.payload){notificationLinks++;await writeEvidenceLine(notificationFile,JSON.stringify(row)+'\n');}
            }
            if (page.payload.length < 1000) break;
            const next = page.payload.at(-1).revision as string; if (next === cursor) throw Error('journal export cursor did not advance'); cursor = next;
          }
        }
        journalFile.end(); notificationFile.end();
        for (const result of await journalClosed) if (result.status === 'rejected') throw result.reason;
        journalStats = {notificationLinks,...JSON.parse(c.sql(`SELECT json_build_object('publications',(SELECT count(*) FROM interface_publication),'deltas',(SELECT count(*) FROM interface_publication_delta),'bytes',pg_total_relation_size('interface_publication')+pg_total_relation_size('interface_publication_delta'))`))};
        renameSync(journalPath + '.partial', journalPath); renameSync(notificationPath + '.partial', notificationPath);
      } catch (e) { journalFile.end(); notificationFile.end(); await journalClosed; journalErrors.push('journal-export:' + String(e)); }
      const auxiliaryErrors: string[] = [];
      try { writeFileSync(resolve(o.out, r.id + '-pg-after.json'), JSON.stringify(pgSnapshot(c), null, 2)); } catch (e) { auxiliaryErrors.push('pg-snapshot:' + String(e)); }
      try { await exportQueryStatistics(db, resolve(o.out, r.id + '-query-statistics.json')); } catch (e) { auxiliaryErrors.push('query-statistics:' + String(e)); }
      const artifactsComplete = journalErrors.length === 0 && auxiliaryErrors.length === 0;
      const rate = sourceRate(allFacts, r.spec, sourceClockUncertaintyMs, r.phaseWindows);
      const failed = hasRenderFailure(r.spec.mode, r.metrics.lags, r.errors.length, missing.length);
      const source = { distribution: r.spec.sourceDistribution, logicalFactCount: allFacts.length, logicalFacts: allFacts.map(f => f.id), projects: [...r.facts.keys()], leafIDs: [...new Set(allFacts.map(f => f.execution))], distinctLeaves: new Set(allFacts.map(f => f.execution)).size, canonicalMutations: allFacts.filter(f => f.upper !== null).length * (r.spec.sourceDistribution === 'hot' ? 1 : 2), commitTransactionCount: new Set(allFacts.flatMap(f => f.transaction ? [f.transaction] : [])).size, commitTransactions: [...new Set(allFacts.flatMap(f => f.transaction ? [f.transaction] : []))] };
      const recordedSpec = { ...r.spec, fixture: f };
      const result = { id: r.id, spec: recordedSpec, candidate: sourceCandidate, development: o.development, status: rate.status !== 'qualified' ? 'incomplete' : failed ? 'failed' : !artifactsComplete ? 'incomplete' : r.spec.mode === 'exceed-capacity' ? 'recovered' : 'passed', reason: rate.status !== 'qualified' ? 'invalid-source-rate' : failed ? 'render-or-recovery-failed' : !artifactsComplete ? 'artifact-export-failed' : null, renderVerdict: failed ? 'failed' : r.spec.mode === 'exceed-capacity' ? 'recovered' : 'passed', sourceRate: rate, source, diagnostic: r.spec.mode === 'controlled-burst', classifications: [r.spec.mode, r.spec.sourceDistribution, ...(r.spec.mode === 'controlled-burst' ? ['diagnostic', 'not-full-envelope'] : [])], observations: r.observations.entries.size, journalStats, facts: r.count, readers: [...r.readers], expectedSamples, samples: stats(r.metrics.lags), sourceWriteMs: stats(r.metrics.writes), scheduleDelayMs: stats(r.metrics.schedule), actionAckMs: stats(r.metrics.controls), recovery, missing, errors: [...r.errors], journalErrors, auxiliaryErrors, artifactsComplete, note: 'Hot mode is hot-execution replacement evidence, never distinct-launch throughput. Legacy hot passed results without source-rate proof are unqualified. Render bounds use the original pre-proof server receipt and include observation transport; proof acknowledgement is separate. Development and controlled-burst are not capacity acceptance. This run alone does not prove the full envelope.' };
      writeFileSync(resolve(o.out, 'source-manifest.json'), JSON.stringify({ run: r.id, spec: recordedSpec, candidate: sourceCandidate, phaseWindows: r.phaseWindows, source, expected, facts: allFacts, journalStats, journalErrors, auxiliaryErrors, artifactsComplete, observationCount: r.observations.entries.size }, null, 2));
      writeFileSync(resolve(o.out, r.id + '-result.json'), JSON.stringify(result, null, 2));
      r.finish = structuredClone(result); r.done = true; emit('finish', r.finish as object);
    };
    const finish = (r: Run): Promise<void> => {
      if (r.finalization) return r.finalization;
      let settled!: () => void;
      r.finalization = new Promise<void>(resolve => { settled = resolve; });
      emit('finish-start', { run: r.id });
      void (async () => {
        try { await finishOwned(r); }
        catch (e) {
          const facts = [...r.facts.values()].flat(), partialSamples = stats([...r.bounds.entries.values()].filter(x => x.confirmed && x.value.fact.phase !== 'warmup').map(x => x.receipt - x.value.fact.lower)), failure = { id: r.id, spec: { ...r.spec, fixture: f }, candidate: sourceCandidate, status: 'incomplete', reason: 'required-finalization-evidence-missing', renderVerdict: partialSamples.violations ? 'failed' : 'unknown', sourceRate: sourceRate(facts, r.spec, sourceClockUncertaintyMs, r.phaseWindows), samples: partialSamples, recovery: null, missing: null, errors: [...r.errors, String(e)], artifactsComplete: false };
          r.finish = structuredClone(failure); r.done = true;
          try { writeFileSync(resolve(o.out, 'source-manifest.json'), JSON.stringify({ run: r.id, spec: failure.spec, candidate: sourceCandidate, phaseWindows: r.phaseWindows, facts, recovery: null, errors: failure.errors }, null, 2)); writeFileSync(resolve(o.out, r.id + '-result.json'), JSON.stringify(failure, null, 2)); emit('finish', failure); } catch (writeError) { console.error('fixture result persistence failed:', writeError); }
        } finally { settled(); }
      })();
      return r.finalization;
    };
    const produce = async (r: Run) => {
      const tasks = new Set<Promise<void>>(); let ordinal = 0;
      // Distinct facts have no shared mutable leaf: eight lanes per project, each with
      // its own persistent transaction session. Hot keeps one ordered lane/project.
      if (r.spec.sourceDistribution === 'distinct') for (let i = sourceWriters.length; i < ids.length * 8; i++) { const writer = new Writer(db, sqlLog); writers.push(writer); sourceWriters.push(writer); }
      emit('source-lanes', { run: r.id, distribution: r.spec.sourceDistribution, lanes: sourceWriters.length });
      const chains=sourceWriters.map(()=>Promise.resolve());
      const cancel=()=>{for(const writer of sourceWriters)void writer.cancel(Error('source run cancelled'));};
      r.abort.signal.addEventListener('abort',cancel,{once:true});
      const schedule = (phase: SourcePhase, due: number) => {
        const n = ordinal++, slot = n % sourceWriters.length, project = ids[n % ids.length];
        emit('source-offered', { run: r.id, id: `${r.id}-${n}`, execution: r.spec.sourceDistribution === 'hot' ? project + '-marker' : `${project}-${r.id}-leaf-${n}`, project, phase, due, offeredAt: stamp(), lane: slot });
        const p = r.spec.sourceDistribution==='distinct'?writeFact(r,n,phase,due):chains[slot].then(()=>writeFact(r,n,phase,due));if(r.spec.sourceDistribution==='hot')chains[slot]=p; tasks.add(p); void p.finally(() => tasks.delete(p));
      };
      try { r.phaseWindows = await scheduleSourcePhases(r.spec, schedule, (event, window) => emit('source-phase-' + event, { run: r.id, window }), { now: stamp, sleep: ms => delay(ms, undefined, { signal: r.abort.signal }) }); }
      finally { await Promise.allSettled(tasks);r.abort.signal.removeEventListener('abort',cancel);if(r.abort.signal.aborted)await Promise.all(sourceWriters.map(w=>w.stopped)); }
      await delay(r.spec.recovery * 1000, undefined, { signal: r.abort.signal }); await finish(r);
    };
    (server as http.Server).on('request', async (req: http.IncomingMessage, res: http.ServerResponse) => {
      const requestReceived = stamp();
      try {
        if (req.url?.startsWith('/__interface/')) {
          const path = req.url.split('?')[0]; let b: any = {};
          if (req.method === 'POST') { const chunks: Buffer[] = []; let size = 0; for await (const chunk of req) { size += chunk.length; if (size > 1024 * 1024) throw Error('measurement body too large'); chunks.push(chunk); } b = JSON.parse(Buffer.concat(chunks).toString()); }
          let result: unknown;
          if (path === '/__interface/config') result = { fixture: f, projects: ids, marker, longEvents: o.longEvents ?? 0, development: o.development, hostname: hostname(), candidate: sourceCandidate.sha, run: run?.id, transport, certificatePEM: tls?.cert.toString() ?? null };
          else if (path === '/__interface/clock') result = { monotonic: stamp(), wall: Date.now() };
          else if (path === '/__interface/start') {
            if (run && !run.done) throw Error('run already active');
            const spec = workload(b.spec, o.development);
            checkSourceFixture(spec.mode, f, o.longEvents);
            if (native && spec.sourceDistribution === 'distinct') throw Error('long native hot-execution fixture requires hot source distribution; use a separate fresh distinct case');
            if (!Array.isArray(b.readers) || b.readers.length !== spec.readers || new Set(b.readers.map((x: any) => x.id)).size !== spec.readers || b.readers.some((x: any) => !ids.includes(x.project))) throw Error('reader barrier mismatch');
            // Fresh server per case: marker counters and prepared baseline must not carry a prior run.
            if (run) throw Error('start a fresh isolated server per case');
            run = { id: randomUUID(), spec, start: stamp(), done: false, abort: new AbortController(), phaseWindows: [], facts: new Map(ids.map(p => [p, []])), seen: new Map(), history: new PublicationHistory(), observations: new ObservationBook(), bounds: new RenderBounds(), processedReceipt: new Map(), unseen: new Map(b.readers.map((x: any) => [x.id, new Map()])), metrics: { lags: [], writes: [], schedule: [], controls: [] }, errors: [], readers: new Map(b.readers.map((x: any) => [x.id, { project: x.project, visible: true }])), count: 0 };
            for (const anchor of anchors) run.history.ingest(anchor);
            emit('start', { id: run.id, spec, readers: b.readers, browser: b.browser }); result = { id: run.id }; const startedRun = run; startedRun.producer = produce(startedRun).catch(async e => { startedRun.errors.push(startedRun.abort.signal.aborted ? 'run-cancelled' : String(e)); await finish(startedRun); });
          } else if (path === '/__interface/render') {
            const r = run; if (!r || r.id !== b.run || !r.readers.has(b.reader)) throw Error('unregistered render');
            const reader = r.readers.get(b.reader)!;
            const observation = r.observations.receive(b, requestReceived);
            emit('render-observation', { run: r.id, observation: b, receipt: requestReceived, firstReceipt: observation.firstReceipt, attempt: observation.attempts });
            let reason: string;
            if (r.done || r.finalization) { reason = r.done ? 'run-finished' : 'run-finalizing'; result = { accepted: false, reason }; }
            else if (!observation.consistent) { reason = 'observation-changed'; r.errors.push(reason); result = { accepted: false, reason }; }
            else if (reader.project !== b.project || !Number.isSafeInteger(b.value) || !Number.isSafeInteger(b.errors) || b.value < 0 || b.errors < 0 || !publicationVersion(b.version ?? '') || b.visible !== true || b.text !== `${b.value} · ${b.errors} failed` || !Number.isFinite(b.browserMonotonic) || !Number.isFinite(b.browserWall)) { reason = 'unverified-render-envelope'; r.errors.push(reason); result = { accepted: false, reason }; }
            else {
              await prove(r, b.project, b.version);
              const found = r.history.lookup(b.project, b.version, b.text);
              if (!found.accepted) { reason = found.reason; if (reason === 'publication-text-mismatch') r.errors.push(reason + ':' + b.observation); result = { accepted: false, reason }; }
              else {
                let pendingReceipt = false;
                // Normal increasing receipts visit pending facts only. A late older proof must
                // also correct already acknowledged facts instead of losing the earlier bound.
                const previousReceipt = r.processedReceipt.get(b.reader) ?? -Infinity;
                const candidates = observation.firstReceipt < previousReceipt ? r.facts.get(b.project)! : r.unseen.get(b.reader)!.values();
                r.processedReceipt.set(b.reader,Math.max(previousReceipt,observation.firstReceipt));
                for (const fact of candidates) {
                  if (fact.error || fact.lower > observation.firstReceipt) continue;
                  const proof = r.history.check(fact, b.version, b.text, r.spec.sourceDistribution);
                  if (!proof.accepted) continue;
                  const publication = {epoch:proof.publication.epoch,revision:proof.publication.revision,calls:proof.publication.calls,errors:proof.publication.errors};
                  const changed = r.bounds.offer(b.reader+'/'+fact.id,observation.firstReceipt,{fact,reader:b.reader,browser:b,publication,leaf:proof.leaf});
                  if (fact.upper === null) pendingReceipt = true;
                  else if (changed) acknowledge(r,b.reader,fact);
                }
                reason = pendingReceipt ? 'source-receipt-pending' : 'committed-publication'; result = { accepted: !pendingReceipt, reason };
              }
            }
            emit('render-attempt', { run: r.id, reader: b.reader, observation: b.observation, version: b.version, receipt: requestReceived, firstReceipt: observation.firstReceipt, acknowledged: stamp(), ...(result as { accepted: boolean; reason: string }) });
          } else if (path === '/__interface/candidate') {
            if (!run || b.run !== run.id || !run.readers.has(b.reader)) throw Error('unregistered DOM candidate');
            emit('dom-candidate', { ...b, receipt: requestReceived }); result = { accepted: true };
          } else if (path === '/__interface/error') { if (run) run.errors.push(JSON.stringify(b)); emit('browser-error', b); result = { accepted: true }; }
          else if (path === '/__interface/status') result = run?.finish ?? { done: false, id: run?.id, submitted: run?.count, finalizing: !!run?.finalization, errors: run?.errors };
          else if (path === '/__interface/control') {
            const begin = stamp(), detail = await op('project_detail', { projectId: ids[0] });
            const readMs = stamp() - begin, t = stamp();
            const content = structuredClone(detail.content); content.settings.name = ids[0] + ' control ' + randomUUID().slice(0, 6);
            await op('project_update', { projectId: ids[0], requestId: randomUUID(), baseRevision: detail.revision, content });
            const ackMs = stamp() - t; run?.metrics.controls.push(ackMs); emit('control', { readMs, ackMs }); result = { readMs, ackMs };
          } else if (path === '/__interface/stop') { result = { stopped: true }; res.end(JSON.stringify(result)); closeSafely(); return; }
          else throw Error('unknown measurement route');
          res.setHeader('content-type', 'application/json'); res.end(JSON.stringify(result)); return;
        }
        proxyRequest(req, res, c.base, { authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test' }, e => errors.push(String(e)));
      } catch (e) { emit('request-error', { error: String(e), path: req.url }); res.writeHead(400, { 'content-type': 'application/json' }); res.end(JSON.stringify({ error: String(e) })); }
    });
    await new Promise<void>(r => server.listen(o.port, '127.0.0.1', r));
    console.log(JSON.stringify({ ready: true, port: (server.address() as any).port, projects: ids, out: o.out, development: o.development, transport, certificateSHA256: tls?.fingerprint256 ?? null }));
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
  if (!['127.0.0.1', 'localhost'].includes(url.hostname) || !['http:', 'https:'].includes(url.protocol)) throw Error('use a loopback SSH tunnel to isolated server, never production');
  mkdirSync(o.out, { recursive: true });
  // The https origin is the fixture's own ephemeral loopback certificate. Only the config read accepts it unverified;
  // its PEM then becomes the sole trusted CA, so every later request verifies the same key at handshake.
  let trusted: https.Agent | null = null;
  const fetchOrigin = (target: string, init: { method?: string; headers?: Record<string, string>; body?: string } = {}) => url.protocol === 'http:' ? fetch(target, init) : new Promise<{ ok: boolean; status: number; json(): Promise<any>; fingerprint256: string }>((ok, fail) => {
    const r = https.request(target, { method: init.method ?? 'GET', headers: init.headers, ...(trusted ? { agent: trusted } : { agent: false, rejectUnauthorized: false }) }, response => {
      const fingerprint256 = (response.socket as import('node:tls').TLSSocket).getPeerCertificate().fingerprint256;
      const chunks: Buffer[] = []; response.on('data', c => chunks.push(c)); response.on('error', fail);
      response.on('end', () => { const text = Buffer.concat(chunks).toString(); ok({ ok: response.statusCode! < 400, status: response.statusCode!, fingerprint256, json: async () => JSON.parse(text) }); });
    });
    r.on('error', fail); r.end(init.body);
  });
  const request = async (path: string, body?: object) => { const r = await fetchOrigin(o.origin + '/__interface/' + path, body ? { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) } : {}); const b = await r.json() as any; if (!r.ok) throw Error(JSON.stringify(b)); return b; };
  const transport: Transport = url.protocol === 'https:' ? 'h2' : 'h1';
  const first = await fetchOrigin(o.origin + '/__interface/config'), config = await first.json();
  if (!first.ok) throw Error(JSON.stringify(config));
  const spec = workload(o.spec, config.development);
  const certificate = transport === 'h2' ? new X509Certificate(config.certificatePEM) : null;
  if ((config.transport ?? 'h1') !== transport || (certificate && certificate.fingerprint256 !== (first as any).fingerprint256)) throw Error('origin transport or certificate does not match the fixture');
  if (certificate) trusted = new https.Agent({ keepAlive: true, ca: config.certificatePEM });
  const placement = JSON.parse(readFileSync(o.placement, 'utf8'));
  if (!config.development && (hostname() === config.hostname || placement.separateMachine !== true || !placement.reservation)) throw Error('separate reserved Chromium machine required');
  const { chromium } = await import('playwright');
  // Chromium accepts exactly the fixture's public key; every other certificate error still fails.
  const spki = certificate ? createHash('sha256').update(certificate.publicKey.export({ type: 'spki', format: 'der' })).digest('base64') : null;
  const browser = await chromium.launch({ headless: true, args: ['--disable-background-timer-throttling', '--disable-renderer-backgrounding', '--disable-backgrounding-occluded-windows', ...(spki ? ['--ignore-certificate-errors-spki-list=' + spki] : [])] });
  const notifications=new EventSourceRecorder(o.out);
  const evidence: any = { spec, placement, transport, hardware: hardware(), chromium: browser.version(), server: config, navigation: [], networkProbes: [] };
  const readers: { id: string; project: string; page: import('playwright').Page }[] = [];
  let browserComplete = false;
  const reportError = async (body: object) => { try { await request('error', body); } catch (e) { (evidence.reportingErrors ??= []).push(String(e)); } };
  let controls: ReturnType<typeof setInterval> | undefined;
  try {
    for (let i = 0; i < spec.readers; i++) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
      const page = await context.newPage(), project = config.projects[i % config.projects.length], id = 'reader-' + i;
      readers.push({ id, project, page });
      const session = await context.newCDPSession(page);
      await page.addInitScript(installEventSourceCapture,{projects:config.projects});
      if (spec.network === '80ms-10Mbps') { await session.send('Network.emulateNetworkConditions', { offline: false, latency: 80, downloadThroughput: 10_000_000 / 8, uploadThroughput: 10_000_000 / 8 }); }
      page.on('pageerror', error => void reportError({ reader: id, type: 'pageerror', error: error.message }));
      page.on('response', response => { if (response.status() >= 400) void reportError({ reader: id, type: 'http', status: response.status(), path: new URL(response.url()).pathname }); });
      await page.addInitScript(()=>{
        const original=window.fetch.bind(window),events:any[]=[];(window as any).__interfaceAPIs=events;
        window.fetch=(input,init)=>{
          const path=new URL(input instanceof Request?input.url:String(input),location.href);
          if(path.origin!==location.origin||!['/api/ops/project_resources','/api/ops/project_resource_items'].includes(path.pathname))return original(input,init);
          const trace=crypto.randomUUID().replaceAll('-',''),span=crypto.randomUUID().replaceAll('-','').slice(0,16);
          const headers=new Headers(init?.headers??(input instanceof Request?input.headers:undefined));headers.set('traceparent',`00-${trace}-${span}-01`);
          const event:any={trace,span,path:path.pathname,startedAt:performance.now(),browserWall:Date.now()};events.push(event);
          return original(input,{...init,headers}).then(response=>{
            event.headersAt=performance.now();event.status=response.status;
            const json=response.json.bind(response);response.json=()=>json().then(body=>{event.bodyParsedAt=performance.now();event.version=response.ok&&typeof body?.version==='string'?body.version:null;event.kind=typeof body?.kind==='string'?body.kind:null;return body;});return response;
          }).catch(error=>{event.failedAt=performance.now();event.error=error?.name??'fetch-error';throw error;});
        };
      });
      await page.addInitScript(() => { (window as any).__interfaceLongTasks = []; new PerformanceObserver(list => { for (const entry of list.getEntries()) (window as any).__interfaceLongTasks.push({ start: entry.startTime, duration: entry.duration }); }).observe({ type: 'longtask', buffered: true }); });
      const start = stamp(); await page.goto(o.origin + '/projects/' + project);
      await page.getByRole('button', { name: 'Save project', exact: true }).waitFor();
      const editableMs = stamp() - start;
      await page.waitForFunction(() => !!document.querySelector('[data-resource-project]')?.getAttribute('data-resource-version'));
      const summaryMs = stamp() - start;
      await notifications.capture({id,page},true);
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
      await session.send('Performance.enable');
      const metricClock=async()=>{const r=await session.send('Performance.getMetrics');return r.metrics.find(x=>x.name==='Timestamp')?.value??null;};
      const before=await metricClock(),browserClock=await page.evaluate(()=>({monotonic:performance.now(),wall:Date.now(),timeOrigin:performance.timeOrigin})),after=await metricClock();
      (evidence.clockBridges??=[]).push({reader:id,cdpBeforeSeconds:before,browser:browserClock,cdpAfterSeconds:after,note:'raw same-browser clock bracket; no estimated cross-machine offset subtracted'});
    }
    notifications.start(readers);
    const { id: run } = await request('start', { spec, readers: readers.map(({ id, project }) => ({ id, project })), browser: evidence });
    for (const reader of readers) {
      await reader.page.evaluate(({ run, reader, project, marker }) => {
        let lastCaptured = '', scheduled = false, inEnvelope = true, stopped = false, sequence = 0;
        const pending = new Map<string, { body: any; sending: boolean; retryAt: number }>();
        const reportError = (error: object) => { if (!stopped) void fetch('/__interface/error', { method: 'POST', body: JSON.stringify({ reader, ...error }) }); };
        const terminalReasons = new Set(['missing-anchor', 'publication-history-gap', 'publication-ledger-mismatch', 'contradictory-publication', 'invalid-publication-counts', 'invalid-leaf-counts', 'publication-text-mismatch', 'unverified-render-envelope', 'observation-changed', 'run-finished']);
        const send = (entry: { body: any; sending: boolean; retryAt: number }) => {
          if (stopped || entry.sending || performance.now() < entry.retryAt) return;
          entry.sending = true;
          void fetch('/__interface/render', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(entry.body) }).then(async response => {
            if (!response.ok) throw Error('render receipt rejected');
            const result = await response.json();
            if (result.accepted || terminalReasons.has(result.reason)) pending.delete(entry.body.observation);
            else entry.retryAt = performance.now() + 100;
          }).catch(error => { reportError({ error: String(error), observation: entry.body.observation }); entry.retryAt = performance.now() + 100; }).finally(() => { entry.sending = false; });
        };
        const scene = () => {
          const win = document.querySelector('[data-resource-window="tools"]');
          const label = [...(win?.querySelectorAll('span') ?? [])].find(x => x.textContent === marker);
          const row = label?.closest('[data-key]'), text = row?.querySelector('.num')?.textContent ?? '';
          const value = /^(\d+) · (\d+) failed$/.exec(text), version = win?.getAttribute('data-resource-version');
          const visible = !!row && document.visibilityState === 'visible' && row.getBoundingClientRect().height > 0 && row.getBoundingClientRect().top >= 0 && row.getBoundingClientRect().bottom <= innerHeight;
          return { win, label, row, text, value, version, visible };
        };
        const observe = () => {
          if (stopped) return;
          const candidate = scene();
          if (!candidate.visible && inEnvelope) reportError({ type: 'left-connected-foreground-envelope', visibility: document.visibilityState, rowPresent: !!candidate.row });
          inEnvelope = candidate.visible;
          if (!candidate.value || !candidate.version || !candidate.visible || candidate.version + '/' + candidate.text === lastCaptured || scheduled) return;
          scheduled = true;
          requestAnimationFrame(() => requestAnimationFrame(() => {
            scheduled = false; if (stopped) return;
            const actual = scene();
            if (actual.text !== candidate.text || actual.version !== candidate.version || !actual.row?.isConnected || actual.label?.textContent !== marker || !actual.visible) {
              void fetch('/__interface/candidate', { method: 'POST', body: JSON.stringify({ run, reader, project, reason: 'changed-before-double-raf', candidate: { text: candidate.text, version: candidate.version }, actual: { text: actual.text, version: actual.version, visible: actual.visible }, browserMonotonic: performance.now(), browserWall: Date.now() }) });
              observe(); return;
            }
            lastCaptured = actual.version + '/' + actual.text;
            const observation = reader + '-' + (++sequence);
            const body = { observation, run, reader, project, value: Number(actual.value![1]), errors: Number(actual.value![2]), text: actual.text, version: actual.version, visible: actual.visible, browserMonotonic: performance.now(), browserWall: Date.now(), domNodes: document.querySelectorAll('*').length, longTasks: (window as any).__interfaceLongTasks.splice(0) };
            const entry = { body, sending: false, retryAt: 0 }; pending.set(observation, entry); send(entry);
          }));
        };
        const mutations = new MutationObserver(observe); mutations.observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true });
        document.addEventListener('visibilitychange', observe);
        const timer = setInterval(() => { observe(); for (const entry of pending.values()) send(entry); }, 50);
        (window as any).__interfaceStopObservation = () => { stopped = true; clearInterval(timer); mutations.disconnect(); document.removeEventListener('visibilitychange', observe); pending.clear(); };
        observe();
      }, { run, reader: reader.id, project: reader.project, marker: config.marker });
    }
    let controlling = false;
    controls = setInterval(() => { if (controlling) return; controlling = true; void request('control').catch(e => reportError({ error: String(e), type: 'control' })).finally(() => { controlling = false; }); }, 1000);
    let result;
    for (;;) { await sleep(1000); result = await request('status'); if (result.status) break; }
    clearInterval(controls); controls = undefined;
    await Promise.all(readers.map(r => r.page.evaluate(() => (window as any).__interfaceStopObservation?.())));
    writeFileSync(resolve(o.out,'api-traces.json'),JSON.stringify(await Promise.all(readers.map(async r=>({reader:r.id,requests:await r.page.evaluate(()=>(window as any).__interfaceAPIs)}))),null,2));
    await notifications.stop();
    for(const reader of readers)await notifications.capture(reader,true);
    await notifications.finish(true);
    evidence.notificationCapture=notifications.snapshot();
    evidence.result = result;
    await readers[0].page.screenshot({ path: resolve(o.out, 'rendered.png') });
    writeFileSync(resolve(o.out, 'browser.json'), JSON.stringify(evidence, null, 2)); browserComplete = true;
    console.log(JSON.stringify(result));
    if (!['passed', 'recovered'].includes(result.status)) process.exitCode = 1;
  } catch (e) { evidence.failure = String(e); throw e; }
  finally {
    if (controls) clearInterval(controls);
    await notifications.stop();
    if (!browserComplete) {
      evidence.incomplete = true; evidence.visualAcceptance = false;
      const partial = await Promise.all(readers.map(async r => { try { return { reader: r.id, requests: await r.page.evaluate(() => (window as any).__interfaceAPIs) }; } catch (e) { return { reader: r.id, unavailable: String(e) }; } }));
      try { writeFileSync(resolve(o.out, 'api-traces-incomplete.json'), JSON.stringify(partial, null, 2)); } catch (e) { evidence.tracePersistenceError = String(e); }
      try { if (readers[0]) await readers[0].page.screenshot({ path: resolve(o.out, 'rendered-incomplete.png') }); else evidence.screenshotError = 'no reader available'; } catch (e) { evidence.screenshotError = String(e); }
      for(const reader of readers)try{await notifications.capture(reader,true);}catch{}
      await notifications.finish(false);
      evidence.notificationCapture=notifications.snapshot();
      try { writeFileSync(resolve(o.out, 'browser-incomplete.json'), JSON.stringify(evidence, null, 2)); } catch (e) { console.error('incomplete browser evidence persistence failed:', e); }
    }
    try { await notifications.finish(false); } finally { await browser.close(); trusted?.destroy(); }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = Object.fromEntries(process.argv.slice(3).map(a => { const [k, v] = a.replace(/^--/, '').split('='); return [k, v ?? 'true']; }));
  const out = resolve(args.out ?? '.egm/interface-performance-run');
  if (process.argv[2] === 'serve') await serve({ out, history: Number(args.history ?? 618), projects: Number(args.projects ?? 1), cardinality: Number(args.cardinality ?? 3), longEvents: Number(args['long-events'] ?? 0), port: Number(args.port ?? 9479), development: args.development === 'true', placement: args.placement, transport: (args.transport ?? 'h1') as Transport });
  else if (process.argv[2] === 'browse') await browse({ origin: args.origin ?? 'http://127.0.0.1:9479', out, placement: args.placement, spec: { readers: Number(args.readers ?? 1), rate: Number(args.rate ?? 1), warmup: Number(args.warmup ?? 60), duration: Number(args.duration ?? 600), burst: Number(args.burst ?? 1000), recovery: Number(args.recovery ?? 60), sourceDistribution: (args['source-distribution'] ?? 'hot') as SourceDistribution, mode: args.mode as Workload['mode'] ?? 'connected', network: args.network as Workload['network'] ?? 'unshaped' } });
  else throw Error('usage: interface.ts serve|browse --placement=<json> --out=<ignored directory> [--key=value]');
}
