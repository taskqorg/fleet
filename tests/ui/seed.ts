// Seed rows for the app checks, written straight into the coordinator's database (tests/harness.ts sql) in the shapes the
// coordinator writes them; plus the browser-origin proxy that adds the credentials, as Cloudflare Access does on PDS18.
import http from 'node:http';
import {createHash, randomUUID} from 'node:crypto';
import {mkdtempSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fakeS3} from '../s3-fake.ts';
import {lit, startCoordinator, type Coordinator} from '../harness.ts';

export const OWNER = 'owner@trial.test';
const LEGACY: Record<string, string> = {queued: 'preparing', accepted: 'preparing', pending: 'preparing', started: 'running', running: 'running', waiting: 'waiting', completed: 'succeeded', failed: 'failed', stopped: 'cancelled', cancelled: 'cancelled'};

// The conversation tables of migration 77: record field, column, type (as go/coordinator/sessions.go convTables).
type ConvTable = {table: string, cols: [string, string, string][], delta?: boolean};
const EVENT_COLS: [string, string, string][] = [['executionId', 'execution_id', 'text'], ['sessionId', 'session_id', 'text'], ['projectId', 'project_id', 'text'], ['seq', 'event_seq', 'int'], ['type', 'type', 'text'], ['timestamp', 'at', 'ms']];
const CONV: Record<string, ConvTable> = {
  session: {table: 'session', cols: [['projectId', 'project_id', 'text'], ['taskId', 'task_id', 'text'], ['hostId', 'host_id', 'text'], ['actor', 'actor', 'text'], ['directedBy', 'directed_by', 'text'],
    ['directorAccessRevokedAt', 'director_access_revoked_at', 'ms'], ['parentExecutionId', 'parent_execution_id', 'text'], ['createdByExecutionId', 'created_by_execution_id', 'text'],
    ['createdBySessionId', 'created_by_session_id', 'text'], ['sourceSessionId', 'source_session_id', 'text'], ['pipelineRunId', 'pipeline_run_id', 'text'], ['createdAt', 'created_at', 'ms'],
    ['archivedAt', 'archived_at', 'ms'], ['title', 'title', 'text'], ['titleBy', 'title_by', 'text'], ['titleCall', 'title_call', 'text'], ['titleAttemptAt', 'title_attempt_at', 'ms'],
    ['titleAttempts', 'title_attempts', 'count'], ['titleUnknownCall', 'title_unknown_call', 'text']]},
  message: {table: 'message', cols: [['sessionId', 'session_id', 'text'], ['executionId', 'execution_id', 'text'], ['projectId', 'project_id', 'text'], ['requestId', 'request_id', 'text'], ['text', 'text', 'text'], ['sentBy', 'sent_by', 'text'], ['source', 'source', 'text']]},
  event: {table: 'event', cols: EVENT_COLS, delta: false},
  'event-delta': {table: 'event', cols: EVENT_COLS, delta: true},
  'queued-message': {table: 'queued_message', cols: [['sessionId', 'session_id', 'text'], ['projectId', 'project_id', 'text'], ['taskId', 'task_id', 'text'], ['requestId', 'request_id', 'text'], ['text', 'text', 'text'],
    ['state', 'state', 'text'], ['executionId', 'execution_id', 'text'], ['steerCommandId', 'steer_command_id', 'text'], ['sentBy', 'sent_by', 'text'], ['source', 'source', 'text'], ['createdAt', 'created_at', 'ms']]},
  command: {table: 'command', cols: [['executionId', 'execution_id', 'text'], ['sessionId', 'session_id', 'text'], ['hostId', 'host_id', 'text'], ['projectId', 'project_id', 'text'], ['kind', 'kind', 'text'],
    ['status', 'status', 'status'], ['stopDetail', 'stop_detail', 'text'], ['handedOut', 'handed_out', 'bool'], ['traceparent', 'traceparent', 'text']]},
};

export function seeder(c: Coordinator, store?: {port: number, objects: Map<string, Buffer>, secrets: string}) {
  // bulk(fn): the statements of fn go to the database in one psql call (hundreds of seed rows in one round trip).
  let buf: string[] | null = null;
  const sql = (text: string) => { if (buf) { buf.push(text + ';'); return ''; } return c.sql(text); };
  const bulk = (fn: () => void) => { buf = []; try { fn(); } finally { const all = buf.join('\n'); buf = null; c.sql('BEGIN;\n' + all + '\nCOMMIT;'); } };
  const ms = (v: any) => typeof v === 'number' ? `to_timestamp(${v / 1000})` : 'NULL';
  // A record of kind (session, message, event, host-observation, command, delivery ...): the coordinator's put(). The
  // conversation kinds have their tables (migration 77, go/coordinator/sessions.go convTables): a field with a column goes
  // there, the rest to payload; every other kind is a document.
  const put = (kind: string, d: any, seq?: number) => { if (kind === 'execution') return execution(d);
    const t = CONV[kind];
    if (t) {
      const cols = ['id'], vals = [lit(d.id)], rest: any = {};
      for (const [k, v] of Object.entries(d)) if (k !== 'id' && !t.cols.some(c => c[0] === k)) rest[k] = v;
      for (const [field, col, typ] of t.cols) {
        const v = d[field]; cols.push(col);
        const ok = v != null && (typ === 'text' || typ === 'status' ? typeof v === 'string' : typ === 'bool' ? typeof v === 'boolean' : typeof v === 'number' && (typ === 'ms' || Number.isInteger(v)));
        if (v != null && !ok) rest[field] = v;
        vals.push(!ok ? (typ === 'count' ? '0' : 'NULL') : typ === 'ms' ? `to_timestamp(${v / 1000})` : typ === 'status' ? `${lit(v)}::command_status` : typeof v === 'string' ? lit(v) : String(v));
      }
      if (t.delta !== undefined) { cols.push('delta'); vals.push(String(t.delta)); }
      if (t.table === 'command' && typeof d.kind !== 'string') vals[cols.indexOf('kind')] = "''";
      if (t.table === 'command' && typeof d.status !== 'string') vals[cols.indexOf('status')] = "'pending'";
      cols.push('payload'); vals.push(`${lit(rest)}::jsonb`);
      if (seq !== undefined) { cols.push('seq'); vals.push(String(seq)); }
      const sets = cols.filter(c => c !== 'id' && c !== 'delta').map(c => `${c}=excluded.${c}`).concat('updated_at=now()');
      sql(`INSERT INTO ${t.table}(${cols.join(',')}) VALUES(${vals.join(',')}) ON CONFLICT(id) DO UPDATE SET ${sets.join(',')}`);
      return;
    }
    sql(`INSERT INTO document(kind,id,data,project_id${seq !== undefined ? ',seq' : ''}) VALUES(${lit(kind)},${lit(d.id)},${lit(d)}::jsonb,${lit(typeof d.projectId === 'string' ? d.projectId : null)}${seq !== undefined ? ',' + seq : ''}) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=now()`); };
  // An agent run as the API record (state mapped to the typed column) or typed (kind and typed state).
  const execution = (e: any) => {
    const state = e.kind ? e.state ?? 'preparing' : LEGACY[e.state] ?? 'preparing';
    sql(`INSERT INTO execution(id,kind,state,project_id,task_id,session_id,host_id,harness,model,started_at,ended_at,data,config_ref)
     VALUES(${lit(e.id)},${lit(e.kind ?? 'agent_run')},${lit(state)},${lit(e.projectId ?? null)},${lit(e.taskId ?? null)},${lit(e.sessionId ?? null)},${lit(e.hostId ?? null)},${lit(e.harness ?? e.agentConfig?.harness ?? 'codex')},${lit(e.model ?? null)},${ms(e.startedAt)},${ms(e.finishedAt)},
      ${lit(e.kind ? {} : {...e, state: undefined})}::jsonb,${lit({effort: e.effort ?? null})}::jsonb)
     ON CONFLICT(id) DO UPDATE SET state=excluded.state,data=excluded.data,started_at=excluded.started_at,ended_at=excluded.ended_at`);
  };
  const task = (v: {title: string, projectId?: string, priority?: string, expectedOutcome?: string, state?: string, waitReason?: string}) => {
    const id = randomUUID();
    sql(`INSERT INTO task(id,project_id,title,expected_outcome) VALUES(${lit(id)},${lit(v.projectId ?? null)},${lit(v.title)},${lit(v.expectedOutcome ?? null)})`);
    if (v.priority && v.priority !== 'normal') sql(`UPDATE task SET priority=${lit(v.priority)} WHERE id=${lit(id)}`);
    if (v.state) sql(`UPDATE task SET state=${lit(v.state)},wait_reason=${lit(v.waitReason ?? null)} WHERE id=${lit(id)}`);
    return id;
  };
  const question = (v: {kind: string, text: string, taskId?: string, executionId?: string, sessionId?: string, options?: string[], askedBy?: string}) => {
    const id = randomUUID();
    sql(`INSERT INTO question(id,kind,project_id,task_id,execution_id,session_id,text,options,asked_by) VALUES(${lit(id)},${lit(v.kind)},
     ${v.executionId ? `(SELECT project_id FROM execution WHERE id=${lit(v.executionId)})` : 'NULL'},${v.taskId ? lit(v.taskId) : v.executionId ? `(SELECT task_id FROM execution WHERE id=${lit(v.executionId)})` : 'NULL'},
     ${lit(v.executionId ?? null)},${lit(v.sessionId ?? null)},${lit(v.text)},${lit(v.options ?? null)}::jsonb,${lit(v.askedBy ?? null)})`);
    return id;
  };
  // A stored artifact: its bytes in the fake store under the content key, the artifact row and its verified copy.
  let storeRow = false;
  const artifact = (v: {id: string, executionId: string, name: string, mediaType: string, bytes: Buffer, state?: string}) => {
    if (!store) throw Error('checkServer({store: true}) for artifacts');
    if (!storeRow) { storeRow = true; writeFileSync(join(store.secrets, 'check-store.key'), JSON.stringify({accessKeyId: 'k', secretAccessKey: 's'}), {mode: 0o600});
      sql(`INSERT INTO store(id,host_id,bucket,endpoint,state,observed_at) VALUES('check-store','pds18','fleet','http://127.0.0.1:${store.port}','reachable',now())`); }
    const hash = createHash('sha256').update(v.bytes).digest('hex');
    store.objects.set(`/fleet/_/prod/${hash}`, v.bytes);
    sql(`INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,state) VALUES(${lit(v.id)},'prod',${lit(hash)},${v.bytes.length},${lit(v.mediaType)},${lit(v.name)},${lit(v.executionId)},${lit(v.state ?? 'stored')})`);
    if ((v.state ?? 'stored') === 'stored') sql(`INSERT INTO artifact_copy(artifact_id,store_id,state,verified_at) VALUES(${lit(v.id)},'check-store','verified',now())`);
  };
  // Rows as JSON objects.
  const rows = (query: string): any[] => JSON.parse(sql(`SELECT coalesce(json_agg(r),'[]') FROM (${query}) r`));
  return {sql, bulk, put, execution, task, question, artifact, rows};
}

// The coordinator and the browser origin in front of it: every request gets the browser secret and the owner.
export async function checkServer(o: {env?: Record<string, string>, store?: boolean} = {}) {
  const s3 = o.store ? {...await fakeS3(), secrets: mkdtempSync(join(tmpdir(), 'fleet-check-stores-'))} : undefined;
  // One database per check script, so checks of one worktree can run at once.
  const c = await startCoordinator({name: (process.argv[1] ?? 'ui').split('/').pop()!.split('.')[0], env: {...o.env, ...(s3 ? {FLEET_STORE_SECRETS: s3.secrets, FLEET_STORE_ENDPOINT_HOSTS: '127.0.0.1'} : {})}});
  const target = new URL(c.base);
  const proxy = http.createServer((req, res) => {
    const up = http.request({host: target.hostname, port: target.port, path: req.url, method: req.method, headers: {...req.headers, authorization: 'Bearer ' + c.secret, 'x-fleet-person': OWNER}}, r => { res.writeHead(r.statusCode!, r.headers); r.pipe(res); });
    up.on('error', () => { res.writeHead(502); res.end(); }); req.pipe(up);
  });
  await new Promise<void>(r => proxy.listen(0, '127.0.0.1', r));
  const origin = `http://127.0.0.1:${(proxy.address() as any).port}`;
  // A host the checks post events as (its executions use host id pds18).
  const host = async (id: string) => { const r = await fetch(c.base + '/api/ops/hosts_register', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + c.secret, 'x-fleet-person': OWNER}, body: JSON.stringify({id})});
    const {secret} = await r.json() as any; return (path: string, body: unknown) => fetch(c.base + path, {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + secret}, body: JSON.stringify(body)}); };
  return {c, origin, host, seed: seeder(c, s3), close: async () => { proxy.close(); s3?.close(); await c.stop(); }};
}
