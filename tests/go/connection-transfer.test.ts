// Outside tests of file tool accounting (docs/designs/fleet-connections.md §9): gateway spool lines with transfer and
// a download's two lines become one tool_run. A coordinator of its own, so the rows can be read back with SQL; the spool
// files are the seam the gateway shares with it.
//   FLEET_TRIAL_TESTS=tests/go/connection-transfer.test.ts node tests/trial/run.ts
import {after, before, test} from 'node:test';
import assert from 'node:assert/strict';
import {createHash, randomUUID} from 'node:crypto';
import {appendFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {startCoordinator, type Coordinator} from '../harness.ts';

const OWNER = 'owner@trial.test';
const dir = mkdtempSync(join(tmpdir(), 'fleet-transfer-')), gatewayDir = join(dir, 'gateway'), spool = join(gatewayDir, 'state', 'spool');
const tokenSha = createHash('sha256').update('invented-transfer-token-' + randomUUID()).digest('hex');
let c: Coordinator;

before(async () => {
  mkdirSync(spool, {recursive: true});
  mkdirSync(join(dir, 'secret'), {recursive: true});
  c = await startCoordinator({name: 'transfer', env: {FLEET_GATEWAY_DIR: gatewayDir, FLEET_SECRET_DIR: join(dir, 'secret'), FLEET_CONNECTION_TICK_MS: '120'}});
  // An owner token's row, as connections_token_create writes it.
  c.sql(`INSERT INTO document(kind,id,data) VALUES ('run-token','run-token:${tokenSha}',jsonb_build_object('id','run-token:${tokenSha}','sha256','${tokenSha}',
    'executionId','token:files','directedBy','${OWNER}','personal','files','connections',jsonb_build_array('youtrack'),'createdAt',1));`);
});
after(async () => { await c?.stop(); });

const today = () => new Date().toISOString().slice(0, 10);
const write = (file: string, ...lines: object[]) => appendFileSync(join(spool, file + '.jsonl'), lines.map(l => JSON.stringify(l) + '\n').join(''));
const row = (id: string) => { const t = c.sql(`SELECT row_to_json(r)::text FROM (SELECT e.state, e.problem, e.updated_at, e.data,
  (extract(epoch FROM e.ended_at-e.started_at)*1000)::int AS span, x.wall_ms, x.connection_cost, x.computed_at
  FROM execution e LEFT JOIN execution_totals x ON x.execution_id=e.id WHERE e.id='call:${id}') r`).trim(); return t ? JSON.parse(t) : undefined; };
async function until<T>(fn: () => T | undefined, what: string): Promise<T> {
  for (let i = 0; i < 100; i++) { const v = fn(); if (v) return v; await new Promise(r => setTimeout(r, 100)); }
  throw Error('timed out: ' + what);
}
const sha = 'a'.repeat(64), transfer = {direction: 'download', bytes: 1000, mediaType: 'image/png', name: 'экран 1.png'};
const started = (callId: string, startedAt: number) => ({callId, tokenSha256: tokenSha, executionId: 'token:files', connection: 'youtrack', tool: 'attachment_get',
  keyFingerprint: 'fp-files-key', status: 200, outcome: 'unknown', errorCode: 'download-unfinished', startedAt, ms: 40, phase: 'started', transfer});
const completed = (callId: string, startedAt: number, end: object = {outcome: 'ok', transfer: {...transfer, sha256: sha}}) => ({callId, tokenSha256: tokenSha,
  executionId: 'token:files', connection: 'youtrack', tool: 'attachment_get', status: 200, startedAt, ms: 300, phase: 'completed', ...end});

test('C10 transfer ingest: a spool line with transfer gives tool_run.data.transfer with the same five fields (requirement: docs/designs/fleet-connections.md §9 accounting; risk: an upload is recorded without its bytes and sha256, so a delivered file cannot be proved)', async () => {
  const callId = randomUUID(), up = {direction: 'upload', bytes: 63_100_000, mediaType: 'video/quicktime', name: 'запись экрана.mov', sha256: sha};
  write(today(), {callId, tokenSha256: tokenSha, executionId: 'token:files', connection: 'youtrack', tool: 'attachment_add', keyFingerprint: 'fp-files-key',
    status: 200, outcome: 'ok', startedAt: Date.now() - 900, ms: 800, transfer: up});
  const r = await until(() => row(callId), 'upload row');
  assert.equal(r.state, 'succeeded');
  assert.deepEqual(r.data.transfer, up);
  assert.equal(r.data.phase, undefined, 'a one-line call has no phase');
  assert.equal(r.data.tokenName, 'files');
});

test('C12 download completion line updates its row (requirement: docs/designs/fleet-connections.md §9 download accounting, two lines with one callId; risk: a download cut mid-stream leaves no tool_run, a finished one stays unknown, or a repeated pass counts or publishes it again)', async () => {
  // (a) the started line alone: failed, unknown: download-unfinished, the lookup size and no sha.
  const id = randomUUID(), at = Date.now() - 1000;
  write(today(), started(id, at));
  let r = await until(() => row(id), 'started row');
  assert.deepEqual([r.state, r.problem, r.data.phase, r.data.transfer], ['failed', 'unknown:download-unfinished', 'started', transfer]);
  // (b) the completed line in the same file: succeeded, ended, with the sha, the problem cleared.
  write(today(), completed(id, at));
  r = await until(() => { const x = row(id); return x?.state === 'succeeded' ? x : undefined; }, 'completed row');
  assert.deepEqual([r.problem, r.span, r.wall_ms, r.data.phase, r.data.outcome, r.data.errorCode, r.data.transfer.sha256, r.data.keyFingerprint],
    [null, 300, 300, 'completed', 'ok', null, sha, 'fp-files-key']);
  // (c) more passes over the same file change nothing: row, connection_cost, and no second snapshot publish.
  await new Promise(res => setTimeout(res, 600));
  const generated = () => JSON.parse(readFileSync(join(gatewayDir, 'snapshot.json'), 'utf8')).generated_ms;
  const published = generated();
  await new Promise(res => setTimeout(res, 800));
  assert.deepEqual(row(id), r);
  assert.equal(generated(), published, 'the snapshot was published again');
  // (d) a completed line in the next day's file updates the row from the previous day's file; (g) both files go.
  const day1 = `19${10 + Math.floor(Math.random() * 80)}-03-01`, day2 = day1.slice(0, 4) + '-03-02', crossing = randomUUID();
  write(day1, started(crossing, at));
  await until(() => row(crossing), 'started row of the first day');
  write(day2, completed(crossing, at));
  await until(() => row(crossing)?.state === 'succeeded' || undefined, 'completed from the next day');
  await until(() => (!existsSync(join(spool, day1 + '.jsonl')) && !existsSync(join(spool, day2 + '.jsonl'))) || undefined, 'past days deleted once imported');
  // (e) a completed line whose started line was lost: one row from it alone.
  const lost = randomUUID();
  write(today(), completed(lost, at));
  r = await until(() => row(lost), 'row from the completed line');
  assert.deepEqual([r.state, r.data.phase, r.data.transfer.sha256], ['succeeded', 'completed', sha]);
  assert.equal(c.sql(`SELECT count(*) FROM execution WHERE id='call:${lost}'`).trim(), '1');
  // (f) a cut download: failed with its code and the bytes actually sent.
  const cut = randomUUID();
  write(today(), started(cut, at), completed(cut, at, {outcome: 'error', errorCode: 'download-cut', transfer: {...transfer, bytes: 300}}));
  r = await until(() => { const x = row(cut); return x?.data.phase === 'completed' ? x : undefined; }, 'cut row');
  assert.deepEqual([r.state, r.problem, r.data.errorCode, r.data.transfer], ['failed', 'step:download-cut', 'download-cut', {...transfer, bytes: 300}]);
});
