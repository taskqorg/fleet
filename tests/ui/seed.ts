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

export function seeder(c: Coordinator, store?: {port: number, objects: Map<string, Buffer>, secrets: string}) {
  // bulk(fn): the statements of fn go to the database in one psql call (hundreds of seed rows in one round trip).
  let buf: string[] | null = null;
  const sql = (text: string) => { if (buf) { buf.push(text + ';'); return ''; } return c.sql(text); };
  const bulk = (fn: () => void) => { buf = []; try { fn(); } finally { const all = buf.join('\n'); buf = null; c.sql('BEGIN;\n' + all + '\nCOMMIT;'); } };
  const ms = (v: any) => typeof v === 'number' ? `to_timestamp(${v / 1000})` : 'NULL';
  // A document of kind (session, message, event, host-observation, command, delivery ...): the coordinator's put().
  const put = (kind: string, d: any) => { if (kind === 'execution') return execution(d);
    sql(`INSERT INTO document(kind,id,data,project_id) VALUES(${lit(kind)},${lit(d.id)},${lit(d)}::jsonb,${lit(typeof d.projectId === 'string' ? d.projectId : null)}) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=now()`); };
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
