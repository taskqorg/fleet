// Outside tests of the coordinator's commit boundaries: what happens on disk and in the schema when a transaction waits for
// another coordinator or fails at commit. Each starts its own coordinator (tests/harness.ts) on its own database, because
// the cases need the database itself: a held migration lock or a deferred trigger that fails the commit.
//   FLEET_TRIAL_TESTS=tests/go/commit.test.ts node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {after, before, test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash, randomBytes, randomUUID} from 'node:crypto';
import {copyFileSync, existsSync, mkdirSync, mkdtempSync, writeFileSync} from 'node:fs';
import {homedir, tmpdir} from 'node:os';
import {join} from 'node:path';
import {freePort, root, startCoordinator, type Coordinator} from '../harness.ts';

const OWNER = 'owner@trial.test';
const dirs = mkdtempSync(join(tmpdir(), 'fleet-commit-'));
const SECRET_DIR = join(dirs, 'secret'), GATEWAY_DIR = join(dirs, 'gateway');
let c: Coordinator;
before(async () => {
  c = await startCoordinator({name: 'commit', env: {FLEET_SECRET_DIR: SECRET_DIR, FLEET_GATEWAY_DIR: GATEWAY_DIR, FLEET_GATEWAY_URL: 'http://127.0.0.1:9', FLEET_CONNECTION_TICK_MS: '200'}});
});
after(() => c.stop());

async function op(name: string, body: unknown = {}) {
  const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': OWNER, 'content-type': 'application/json'}, body: JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const ok = (r: {status: number, body: any}) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));

// A deferred constraint trigger fails the commit of any transaction that writes a document of this kind (or an execution
// with this id prefix): the handler's own statements all succeed, only COMMIT fails.
function failCommitOn(name: string, where: string) {
  c.sql(`CREATE OR REPLACE FUNCTION ${name}() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '${name}'; END $$;
    CREATE CONSTRAINT TRIGGER ${name} AFTER INSERT OR UPDATE ON ${where.split(' ')[0]} DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (${where.slice(where.indexOf(' ') + 1)}) EXECUTE FUNCTION ${name}();`);
}
const clear = (name: string, table: string) => c.sql(`DROP TRIGGER ${name} ON ${table}; DROP FUNCTION ${name}();`);

test('M1 a migration applied by another coordinator while this one waited for the lock is not run again (requirement: each schema section applies once; risk: two coordinators starting together both run a section and one fails startup)', async () => {
  const db = c.sql('SELECT current_database()').trim();
  const v = c.sql('SELECT max(version) FROM schema_migration').trim();
  const pgdata = process.env.FLEET_TRIAL_PGDATA || join(homedir(), '.cache/fleet-trial/pg'), pgport = process.env.FLEET_TRIAL_PGPORT || '54329';
  const url = (user: string) => `postgresql://${user}@/${db}?host=${encodeURIComponent(pgdata)}&port=${pgport}`;
  // The other coordinator: it holds the migration lock while it records the newest version, as if it had just applied it.
  c.sql(`DELETE FROM schema_migration WHERE version=${v}`);
  const other = spawn('psql', ['-h', pgdata, '-p', pgport, '-U', 'fleet', '-d', db, '-v', 'ON_ERROR_STOP=1', '-q'], {stdio: ['pipe', 'ignore', 'inherit']});
  other.stdin.end(`BEGIN; SELECT pg_advisory_xact_lock(hashtext('fleet:write')); INSERT INTO schema_migration(version) VALUES(${v}); SELECT pg_sleep(3); COMMIT;\n`);
  const otherDone = new Promise(r => other.on('exit', r));
  await sleep(500);
  const port = await freePort(), people = join(dirs, 'people.json');
  copyFileSync(join(root, 'tests/trial/people.json'), people);
  const second = spawn(process.env.FLEET_TRIAL_SERVER || 'go/bin/fleet-coordinator', {cwd: root, shell: true, detached: true, stdio: 'inherit', env: {...process.env,
    PORT: String(port), FLEET_DATABASE_URL: url('fleet_app'), FLEET_MIGRATION_DATABASE_URL: url('fleet'), FLEET_PEOPLE_FILE: people,
    FLEET_BROWSER_SECRET: randomBytes(24).toString('hex'), FLEET_HOST_ID: 'trial-host', FLEET_CONNECTION_TICK_MS: '0', FLEET_PIPELINE_TICK_MS: '0'}});
  let exited = false; second.on('exit', () => { exited = true; });
  try {
    let served = false;
    for (let i = 0; i < 200 && !exited && !served; i++) { try { await fetch(`http://127.0.0.1:${port}/api/profiles`); served = true; } catch { await sleep(100); } }
    assert.ok(served && !exited, 'the second coordinator failed its migration instead of starting');
  } finally {
    try { process.kill(-second.pid!, 'SIGKILL'); } catch {}
    await otherDone;
  }
});

const sha = (s: string) => createHash('sha256').update(s).digest('hex');
const name = (p: string) => p + '-' + randomUUID().slice(0, 8);
async function tavily() {
  const conn = name('tavily');
  ok(await op('connections_create', {name: conn, provider: 'tavily'}));
  ok(await op('connections_set_access', {connection: conn, access: {projects: ['*']}}));
  return conn;
}

test('S1 a past spool file stays until its imported lines are committed (requirement: every gateway call is accounted once; risk: a failed commit after the file was deleted loses the billable calls in it)', async () => {
  const conn = await tavily();
  const m = ok(await op('connections_token_create', {name: name('t'), connections: [conn]}));
  const callId = randomUUID(), dir = join(GATEWAY_DIR, 'state', 'spool'), file = join(dir, '2020-01-01.jsonl');
  mkdirSync(dir, {recursive: true});
  failCommitOn('fail_spool', `execution NEW.id = 'call:${callId}'`);
  writeFileSync(file, JSON.stringify({callId, tokenSha256: sha(m.token), executionId: 'token:x', tool: 'web_search', connection: conn, outcome: 'ok', startedAt: Date.now(), ms: 5}) + '\n');
  await sleep(1500); // several connection ticks, each failing at commit
  assert.ok(existsSync(file), 'the spool file was deleted although its lines never committed');
  clear('fail_spool', 'execution');
  for (let i = 0; i < 50 && existsSync(file); i++) await sleep(100);
  assert.equal(existsSync(file), false, 'the spool file stays after a committed import');
  assert.equal(c.sql(`SELECT count(*) FROM execution WHERE id='call:${callId}'`).trim(), '1');
});
