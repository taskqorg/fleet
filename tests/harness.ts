// The Go coordinator under test (go/bin/fleet-coordinator, or FLEET_TRIAL_SERVER): a fresh Postgres database with the PDS18
// roles on the machine's one test PostgreSQL, which the Go tests share (docs/designs/fleet-host-cleanup.md#8-test-postgresql;
// tests/go/run-files.ts gives each parallel file its own database on it), the binary started against it on a free port.
// Used by tests/trial/run.ts and the app checks in tests/ui. Node standard library and the PostgreSQL client programs only;
// seed data goes in through sql().
//
// Environment (all optional): FLEET_TRIAL_SERVER (shell command, default go/bin/fleet-coordinator), FLEET_TEST_PGDATA
// (default ~/.cache/fleet-test/pg; created with initdb and started on first use, left running), FLEET_TEST_PGPORT (default
// 54339). FLEET_TRIAL_PGDATA and FLEET_TRIAL_PGPORT, the older names, override them for one run (tests/performance/interface.ts
// runs its own server); both pairs are set to the values in use, for tests and their children that read them.
import {execFileSync, spawn} from 'node:child_process';
import {createHash, randomBytes} from 'node:crypto';
import {appendFileSync, copyFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync} from 'node:fs';
import {createServer} from 'node:net';
import {homedir, tmpdir} from 'node:os';
import {basename, dirname, join} from 'node:path';

export const root = new URL('../', import.meta.url).pathname;
export const pgdata = process.env.FLEET_TRIAL_PGDATA || process.env.FLEET_TEST_PGDATA || join(homedir(), '.cache/fleet-test/pg');
export const pgport = process.env.FLEET_TRIAL_PGPORT || process.env.FLEET_TEST_PGPORT || '54339';
Object.assign(process.env, {FLEET_TEST_PGDATA: pgdata, FLEET_TEST_PGPORT: pgport, FLEET_TRIAL_PGDATA: pgdata, FLEET_TRIAL_PGPORT: pgport});
const pgArgs = ['-h', pgdata, '-p', pgport, '-U', 'postgres'];
// The PostgreSQL programs get a UTF-8 locale: initdb refuses a missing one ("invalid locale settings"), the macOS default without LANG.
const sh = (cmd: string, args: string[], input?: string) => execFileSync(cmd, args, {input, encoding: 'utf8', stdio: [input === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe'], env: {...process.env, LC_ALL: 'C.UTF-8', LANG: 'C.UTF-8'}});
const ready = () => { try { sh('pg_isready', pgArgs); return true; } catch { return false; } };
// startPostgres: the server ready, as the Go tests' startTestCluster (go/coordinator/pg_fixture_test.go) does it, with the
// same options. Durability is off (testPGConf), written at initdb, so a server in another directory (such as
// tests/performance/interface.ts's) keeps its own settings. initdb runs only without PG_VERSION, into a private directory
// renamed into place: of two racing starters the loser uses the winner's. pg_ctl start runs only when the server does not
// answer, and a lost start race waits for the winner's server. The server runs without the executor's run marker
// (FLEET_EXECUTION_ID): it outlives the run that started it.
// ponytail: max_connections 400 fits about 4 worktrees' coordinators and Go test processes at once; raise it for more.
const testPGConf = '\nfsync = off\nsynchronous_commit = off\nfull_page_writes = off\n';
function startPostgres() {
  if (ready()) return;
  if (!existsSync(join(pgdata, 'PG_VERSION'))) {
    mkdirSync(dirname(pgdata), {recursive: true});
    const tmp = `${pgdata}.init-${process.pid}`;
    rmSync(tmp, {recursive: true, force: true});
    sh('initdb', ['-D', tmp, '-U', 'postgres', '-A', 'trust', '-E', 'UTF8', '--no-instructions', '--no-sync']);
    appendFileSync(join(tmp, 'postgresql.auto.conf'), testPGConf);
    try { renameSync(tmp, pgdata); } catch (e) { rmSync(tmp, {recursive: true, force: true}); if (!existsSync(join(pgdata, 'PG_VERSION'))) throw e; }
  }
  const {FLEET_EXECUTION_ID: _, ...env} = process.env;
  try {
    execFileSync('pg_ctl', ['-D', pgdata, '-l', join(pgdata, 'server.log'), '-w', '-t', '60', '-o', `-k ${pgdata} -p ${pgport} -c listen_addresses= -c max_connections=400`, 'start'],
      {encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], env: {...env, LC_ALL: 'C.UTF-8', LANG: 'C.UTF-8'}});
  } catch (e) {
    for (const until = Date.now() + 60_000; Date.now() < until; execFileSync('sleep', ['0.2'])) if (ready()) return;
    throw e;
  }
}
export const freePort = () => new Promise<number>(r => { const s = createServer().listen(0, '127.0.0.1', () => { const p = (s.address() as any).port; s.close(() => r(p)); }); });

// testConfig: the shipped config/ (but its Lab assets, read from config/lab) plus the fixture projects of
// tests/fixtures/projects, in a fresh directory per coordinator: definition writes never touch the checkout.
export function testConfig(): string {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-config-'));
  cpSync(join(root, 'config'), dir, {recursive: true, filter: src => src !== join(root, 'config/lab')});
  cpSync(join(root, 'tests/fixtures/projects'), join(dir, 'projects'), {recursive: true});
  return dir;
}

// SQL literal for seed statements (psql has no parameters on stdin).
export const lit = (v: unknown): string => v === null || v === undefined ? 'NULL' : typeof v === 'number' || typeof v === 'boolean' ? String(v)
  : "'" + (typeof v === 'string' ? v : JSON.stringify(v)).replace(/'/g, "''") + "'";

export type Coordinator = {base: string, secret: string, pid: number, sql: (text: string) => string, exited: () => boolean, stop: () => Promise<void>, restart: () => Promise<void>};

// name: a database name suffix, so parallel users in one worktree do not share a database.
export async function startCoordinator(o: {name?: string, env?: Record<string, string>, people?: string} = {}): Promise<Coordinator> {
  startPostgres();
  // deploy/infrastructure/fleet-postgres.sql itself, with only the database name changed (roles are per server).
  // scope: the file's run (FLEET_TRIAL_NAME, tests/go/run-files.ts), so files run at once on the one server never share a
  // database, also for their unnamed or same-named coordinators.
  const scope = process.env.FLEET_TRIAL_NAME ? process.env.FLEET_TRIAL_NAME + '/' : '';
  const db = 'fleet_trial_' + basename(root.replace(/\/$/, '')).replace(/\W/g, '_').toLowerCase().slice(0, 24) + '_' + createHash('sha256').update(root + scope + (o.name ?? '')).digest('hex').slice(0, 6) + (o.name ? '_' + o.name.replace(/\W/g, '_').slice(0, 10) : '');
  const psql = (args: string[], input?: string) => sh('psql', [...pgArgs, '-v', 'ON_ERROR_STOP=1', '-q', ...args], input);
  const dropDb = () => psql(['-d', 'postgres', '-c', `DROP DATABASE IF EXISTS ${db} WITH (FORCE)`]);
  dropDb();
  const roles = readFileSync(join(root, 'deploy/infrastructure/fleet-postgres.sql'), 'utf8')
    .replace(/DATABASE fleet\b/g, `DATABASE ${db}`).replace(/datname = 'fleet'/, `datname = '${db}'`).replace(/^\\connect fleet$/m, `\\connect ${db}`);
  try { psql(['-d', 'postgres'], roles); } catch { psql(['-d', 'postgres'], roles); } // two worktrees at once: the loser retries
  const url = (user: string) => `postgresql://${user}@/${db}?host=${encodeURIComponent(pgdata)}&port=${pgport}`;
  // A copy: people_set rewrites the file it was started with.
  const people = join(mkdtempSync(join(tmpdir(), 'fleet-people-')), 'people.json');
  copyFileSync(o.people ?? join(root, 'tests/trial/people.json'), people);
  // The coordinator binds the port itself (PORT=0 the first time) and names it on stderr: a port picked here and bound
  // later can be taken in between (tests and their clients share the ephemeral range), and the coordinator then exits
  // with "bind: address already in use". A restart binds the same port again.
  let port = 0, base = '';
  const secret = randomBytes(24).toString('hex'), config = testConfig();
  let server: ReturnType<typeof spawn>, exited = false, gone: Promise<void> = Promise.resolve();
  const launch = async () => {
    exited = false;
    const child = server = spawn(process.env.FLEET_TRIAL_SERVER || 'go/bin/fleet-coordinator', {cwd: root, shell: true, detached: true, stdio: ['ignore', 'inherit', 'pipe'], env: {...process.env,
      PORT: String(port), FLEET_DATABASE_URL: url('fleet_app'), FLEET_MIGRATION_DATABASE_URL: url('fleet'),
      FLEET_PEOPLE_FILE: people, FLEET_BROWSER_SECRET: secret, FLEET_HOST_ID: 'trial-host', FLEET_PROJECT_CONFIG_DIRECTORY: config,
      // Never the host's own span store (~/.config/fleet/clickhouse.env): a test names its ClickHouse.
      FLEET_CLICKHOUSE_URL: 'off', FLEET_CLICKHOUSE_ENV: '/nonexistent',
      // Never GitHub from tests: the Runners CI pass would spend the runner's keyless 60 requests an hour.
      FLEET_RUNNERS_CI_TICK_MS: '0',
    // The lost sweep at test speed (tests/go/run.sh used to set it; a file run alone must behave the same).
    FLEET_LOST_AFTER_MS: process.env.FLEET_LOST_AFTER_MS ?? '6000', ...o.env}});
    // Each process reports only its own exit: a late exit of the previous one never marks this one exited.
    gone = new Promise(r => child.on('exit', () => { if (server === child) exited = true; r(); }));
    let log = '';
    child.stderr!.on('data', (b: Buffer) => {
      process.stderr.write(b);
      if (port) return;
      log = (log + b.toString()).slice(-4096);
      const m = /serving on 127\.0\.0\.1:(\d+)/.exec(log);
      if (m) { port = Number(m[1]); base = `http://127.0.0.1:${port}`; }
    });
    for (let i = 0; ; i++) {
      if (exited) { dropDb(); throw Error('coordinator exited before it served requests'); }
      if (!port) { if (i > 600) { await stop(); throw Error('coordinator did not bind in 60 s'); } await new Promise(r => setTimeout(r, 100)); continue; }
      try { await fetch(base + '/api/ops/me', {method: 'POST'}); break; } catch { if (i > 600) { await stop(); throw Error('coordinator did not start in 60 s'); } await new Promise(r => setTimeout(r, 100)); }
    }
  };
  // A crashed caller must not leave the coordinator running (it holds the caller's stdout open).
  process.on('exit', () => { if (!exited) try { process.kill(-server.pid!, 'SIGKILL'); } catch {} });
  const kill = async () => { if (!exited) { try { process.kill(-server.pid!, 'SIGTERM'); } catch {} for (let i = 0; i < 50 && !exited; i++) await new Promise(r => setTimeout(r, 100)); if (!exited) try { process.kill(-server.pid!, 'SIGKILL'); } catch {} await gone; } };
  const stop = async () => { await kill(); dropDb(); rmSync(config, {recursive: true, force: true}); };
  // restart: a new coordinator process on the same database (what a deploy does).
  const restart = async () => { await kill(); await launch(); };
  await launch();
  // Seeds run as the owner role, after the coordinator applied the schema.
  return {base, secret, get pid() { return server.pid!; }, sql: text => psql(['-d', db, '-U', 'fleet', '-At'], "SET fleet.project_ids = '*';\n" + text), exited: () => exited, stop, restart};
}

// hostCommand: the first polled command pick accepts, polled as an executor does: one plain poll, then short held polls
// for up to 10 s in all. One poll does not prove a command is ready or not: it may come a round later (host_poll.go
// pollReadyCommand withholds a command whose execution row is busy at that moment). A pending command is handed out
// again on every poll until its receipt, so polling again is safe. poll sends the host poll with query ('' or
// '?wait=1s') appended to its path and answers the reply; undefined when no such command came within ms, for the
// caller's own assertion.
export async function hostCommand(poll: (query: string) => Promise<{commands: any[]}>, pick: (c: any) => unknown, ms = 10_000): Promise<any> {
  for (let query = '', end = Date.now() + ms; ; query = '?wait=1s') {
    const c = (await poll(query)).commands.find(pick);
    if (c || Date.now() >= end) return c;
    await new Promise(r => setTimeout(r, 200)); // a held poll answers at once while another command is pending
  }
}

// noHostCommand: true when no command pick accepts came in 2 s of polling as hostCommand does. One poll does not prove a
// command absent either: one withheld for a round would pass it.
export async function noHostCommand(poll: (query: string) => Promise<{commands: any[]}>, pick: (c: any) => unknown): Promise<boolean> {
  return await hostCommand(poll, pick, 2_000) === undefined;
}
