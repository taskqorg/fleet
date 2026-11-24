// FLT-9 mixed-state diagnostic (docs: tests/performance/README.md "Mixed rules diagnostic"). Not capacity qualification.
//   FLEET_TRIAL_PGDATA=<task-owned dir> FLEET_TRIAL_PGPORT=<port> node tests/performance/mixed-rules.ts --out=<dir> [--seconds=20] [--writers=4] [--tick=100]
// One fresh isolated database through tests/harness.ts (ordinary rules, admission, capture). It seeds running-active,
// running-young, running-silent, running-unconfirmed, waiting and open-question executions, runs the real rules pass on a short
// tick while operator writes overlap, and samples pg_locks. Measured here: operator write acknowledgement (server-local HTTP),
// advisory-lock wait/hold lower bounds (sampled), rules findings per case. Not measured: browser render, publication.
import {execFileSync, spawn, type ChildProcess} from 'node:child_process';
import {mkdirSync, writeFileSync} from 'node:fs';
import {join, resolve} from 'node:path';
import {pgdata, pgport, startCoordinator} from '../harness.ts';

const arg = (k: string, d: string) => process.argv.find(a => a.startsWith('--' + k + '='))?.slice(k.length + 3) ?? d;
const out = resolve(arg('out', '.egm/mixed-rules')), seconds = Number(arg('seconds', '20')), writers = Number(arg('writers', '4')), tick = arg('tick', '100'), scale = Number(arg('scale', '1')), SAMPLE_MS = 20;
mkdirSync(out, {recursive: true});
const attach = arg('attach', ''), OWNER = 'owner@trial.test', P = arg('project', 'desiign');
// --attach=<origin>: load an already running interface.ts serve fixture (same database, its proxy supplies credentials) instead of a new coordinator.
const pg = (db: string, sql: string) => execFileSync('psql', ['-h', pgdata, '-p', pgport, '-U', 'fleet', '-d', db, '-At', '-q', '-v', 'ON_ERROR_STOP=1'], {input: "SET fleet.project_ids = '*';\n" + sql, encoding: 'utf8'});
const attachedDb = attach ? execFileSync('psql', ['-h', pgdata, '-p', pgport, '-U', 'postgres', '-d', 'postgres', '-At', '-c', "SELECT datname FROM pg_database WHERE datname LIKE 'fleet_trial_%' ORDER BY datname"], {encoding: 'utf8'}).trim().split('\n').filter(Boolean) : [];
if (attach && attachedDb.length !== 1) throw Error('attach needs exactly one fleet_trial_ database, found ' + attachedDb.join(','));
const c = attach ? {base: attach, secret: '', sql: (t: string) => pg(attachedDb[0], t), stop: async () => {}} : await startCoordinator({name: 'mixed', env: {FLEET_RULES_TICK_MS: tick}});
let sampler: ChildProcess | undefined;
try {
const q = (sql: string) => c.sql(sql).trim();
const op = async (name: string, body: object) => {
  const t = performance.now();
  const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: attach ? {'content-type': 'application/json'} : {authorization: 'Bearer ' + c.secret, 'x-fleet-person': OWNER, 'content-type': 'application/json'}, body: JSON.stringify(body)});
  const text = await r.text(); return {status: r.status, ms: performance.now() - t, text};
};
const ex = (id: string, state: string, obs: string, ageMin: number) => `INSERT INTO execution(id,kind,state,observation,project_id,directed_by,started_at,data) VALUES ('${id}','agent_run','${state}','${obs}','${P}','${OWNER}',now()-interval '${ageMin} minutes','${state === 'waiting' ? '{"waitReason":"capacity","waitingSince":1}' : '{}'}');`;
const ev = (id: string, e: string, agoMin: number) => `INSERT INTO event(id,execution_id,project_id,event_seq,type,at,updated_at) VALUES ('${id}','${e}','${P}',1,'tool.call',now()-interval '${agoMin} minutes',now()-interval '${agoMin} minutes');`;
const groups: Record<string, string[]> = {
  'running-active': Array.from({length: 8 * scale}, (_, i) => i + 1).map(i => 'mx-active-' + i), 'running-young': Array.from({length: 4 * scale}, (_, i) => i + 1).map(i => 'mx-young-' + i),
  'running-silent': Array.from({length: 3 * scale}, (_, i) => i + 1).map(i => 'mx-silent-' + i), 'running-unconfirmed': Array.from({length: 2 * scale}, (_, i) => i + 1).map(i => 'mx-unconf-' + i), waiting: Array.from({length: 4 * scale}, (_, i) => i + 1).map(i => 'mx-wait-' + i),
};
let sql = '';
for (const e of groups['running-active']) sql += ex(e, 'running', 'confirmed', 120) + ev('ev-' + e, e, 0);
for (const e of groups['running-young']) sql += ex(e, 'running', 'confirmed', 2);
for (const e of groups['running-silent']) sql += ex(e, 'running', 'confirmed', 120) + ev('ev-' + e, e, 100);
for (const e of groups['running-unconfirmed']) sql += ex(e, 'running', 'unconfirmed', 120);
for (const e of groups.waiting) sql += ex(e, 'waiting', 'confirmed', 120);
const ids = (k: string) => Array.from({length: 2 * scale}, (_, i) => `mx-q-${k}-${i + 1}`), answerIds = ids('ans'), questions = [...ids('open'), ...answerIds];
questions.forEach((id, i) => { sql += `INSERT INTO question(id,kind,project_id,execution_id,text,asked_by) VALUES ('${id}','operator_decision','${P}','${groups.waiting[i % groups.waiting.length]}','Proceed with ${id}?','agent');`; });
q(sql);

// Lock sampler: one psql session, \watch every 20 ms; keys matched by the coordinator's hashtextextended(key, 0).
const names = ['fleet:write', 'dispatch', 'problems:rules', 'project:' + P, 'project:hello-world'];
const keyCase = names.map(n => `WHEN ((l.classid::bigint<<32)|l.objid::bigint)=hashtextextended('${n}',0) THEN '${n}'`).join(' ');
const sampleSql = `SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint,l.pid,l.granted,CASE ${keyCase} ELSE 'other' END,left(replace(a.query,E'\\n',' '),50) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.locktype='advisory' AND a.datname=current_database();`;
const url = `postgresql://fleet@/${attach ? attachedDb[0] : q('SELECT current_database()')}?host=${encodeURIComponent(pgdata)}&port=${pgport}`;
const samples: string[] = [];
sampler = spawn('psql', [url, '-At', '-F', '|', '-q'], {stdio: ['pipe', 'pipe', 'inherit']});
let buf = ''; sampler.stdout!.on('data', d => { buf += d; const l = buf.split('\n'); buf = l.pop()!; for (const x of l) samples.push(x); });
sampler.stdin!.write(`${sampleSql}\n\\watch ${SAMPLE_MS / 1000}\n`);
await new Promise(r => setTimeout(r, 500));

const problems = () => q(`SELECT cause||'|'||state||'|'||coalesce(addressed_to,'') FROM question WHERE cause LIKE 'problem:%' ORDER BY cause`).split('\n').filter(Boolean);
const findingsOf = async () => { const r = await op('telemetry_findings', {}); try { return (JSON.parse(r.text).findings ?? []).map((f: any) => f.rule + ':' + f.subject); } catch { return [r.status + ' ' + r.text.slice(0, 100)]; } };
const before = problems(), findingsBefore = await findingsOf();
const acks: Record<string, number[]> = {hosts_register: [], questions_answer: []};
const failures: string[] = [];
const record = (bucket: number[], r: {status: number; ms: number; text: string}) => {
  if (r.status === 200) bucket.push(r.ms);
  else failures.push(`${r.status} ${r.text.slice(0, 80)}`);
};
const end = Date.now() + seconds * 1000; let n = 0;
const writer = async (w: number) => { while (Date.now() < end) { record(acks.hosts_register, await op('hosts_register', {id: `mx-host-${w}-${n++}`, holder: OWNER})); await new Promise(r => setTimeout(r, 25)); } };
const answering = (async () => { await new Promise(r => setTimeout(r, seconds * 400));
  for (const id of answerIds) record(acks.questions_answer, await op('questions_answer', {questionId: id, answer: 'accepted'})); })();
await Promise.all([...Array.from({length: writers}, (_, w) => writer(w)), answering]);
const during = problems();
// A genuinely resolved synthetic problem: a fresh qualifying event on one silent run must close its finding after the clean runs.
q(ev('ev-resolve', 'mx-silent-1', 0));
await new Promise(r => setTimeout(r, Number(tick) * 12 + 2000));
const after = problems();
const answered = q(`SELECT id||'|'||state FROM question WHERE id LIKE 'mx-q-%' ORDER BY id`).split('\n');
const states = q(`SELECT id||'|'||state||'|'||observation FROM execution WHERE id LIKE 'mx-%' ORDER BY id`).split('\n');
sampler.kill('SIGTERM'); sampler = undefined;

// Wait/hold lower bounds from consecutive samples of the same (pid,key,granted); resolution SAMPLE_MS.
type Span = {k: string; g: boolean; first: number; last: number; query: string};
const spans: Span[] = [], active = new Map<string, Span>();
for (const s of samples) { const [t, pid, g, key, query] = s.split('|'); if (!t || !key) continue; const ts = Number(t), id = `${pid}|${key}|${g}`, o = active.get(id); if (o && ts - o.last <= SAMPLE_MS * 4) o.last = ts; else { const span = {k: key, g: g === 't', first: ts, last: ts, query}; active.set(id, span); spans.push(span); } }
const stat = (xs: number[]) => { xs = xs.sort((a, b) => a - b); const p = (f: number) => xs[Math.min(xs.length - 1, Math.floor(xs.length * f))]; return xs.length ? {count: xs.length, p50: p(.5), p95: p(.95), max: xs[xs.length - 1]} : {count: 0}; };
const lock: Record<string, any> = {};
for (const k of new Set(spans.map(s => s.k))) lock[k] = {holdMsLowerBound: stat(spans.filter(s => s.k === k && s.g).map(s => s.last - s.first + SAMPLE_MS)), waitMsLowerBound: stat(spans.filter(s => s.k === k && !s.g).map(s => s.last - s.first + SAMPLE_MS))};
const result = {
  kind: 'FLT-9 mixed rules diagnostic (co-located, not reference qualification)', at: new Date().toISOString(), seconds, writers, scale, rulesTickMs: Number(tick), sampleMs: SAMPLE_MS, sampleRows: samples.length,
  seeded: Object.fromEntries(Object.entries(groups).map(([k, v]) => [k, v.length]).concat([['questions', questions.length]])),
  acknowledgementMs: {hosts_register: stat(acks.hosts_register), questions_answer: stat(acks.questions_answer), failures},
  locks: lock, findingsBefore, stuckProblems: {before, during, afterResolvingOne: after}, questions: answered, executionStates: states.length,
  expect: {silentStuck: groups['running-silent'].length, activeOrUnconfirmedOrWaitingOrYoungFindings: 0},
  unknown: ['browser render', 'publication journal', 'server-side span durations (ClickHouse off)'],
};
writeFileSync(join(out, 'mixed-rules.json'), JSON.stringify(result, null, 2)); writeFileSync(join(out, 'lock-samples.psv'), samples.join('\n'));
console.log(JSON.stringify(result, null, 2));
} finally { sampler?.kill('SIGTERM'); await c.stop(); }
