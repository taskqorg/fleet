// The Go coordinator under test (go/bin/fleet-coordinator, or FLEET_TRIAL_SERVER): a fresh Postgres database with the PDS18
// roles on one shared local server, the binary started against it on a free port. Used by tests/trial/run.ts and the app
// checks in tests/ui. Node standard library and the PostgreSQL client programs only; seed data goes in through sql().
//
// Environment (all optional): FLEET_TRIAL_SERVER (shell command, default go/bin/fleet-coordinator), FLEET_TRIAL_PGDATA
// (default ~/.cache/fleet-trial/pg; created with initdb and started on first use), FLEET_TRIAL_PGPORT (default 54329).
import {execFileSync, spawn} from 'node:child_process';
import {createHash, randomBytes} from 'node:crypto';
import {copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync} from 'node:fs';
import {createServer} from 'node:net';
import {homedir, tmpdir} from 'node:os';
import {basename, join} from 'node:path';

export const root = new URL('../', import.meta.url).pathname;
const pgdata = process.env.FLEET_TRIAL_PGDATA || join(homedir(), '.cache/fleet-trial/pg');
const pgport = process.env.FLEET_TRIAL_PGPORT || '54329';
const pgArgs = ['-h', pgdata, '-p', pgport, '-U', 'postgres'];
const sh = (cmd: string, args: string[], input?: string) => execFileSync(cmd, args, {input, encoding: 'utf8', stdio: [input === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe']});
const ready = () => { try { sh('pg_isready', pgArgs); return true; } catch { return false; } };
export const freePort = () => new Promise<number>(r => { const s = createServer().listen(0, '127.0.0.1', () => { const p = (s.address() as any).port; s.close(() => r(p)); }); });

// SQL literal for seed statements (psql has no parameters on stdin).
export const lit = (v: unknown): string => v === null || v === undefined ? 'NULL' : typeof v === 'number' || typeof v === 'boolean' ? String(v)
  : "'" + (typeof v === 'string' ? v : JSON.stringify(v)).replace(/'/g, "''") + "'";

export type Coordinator = {base: string, secret: string, pid: number, sql: (text: string) => string, exited: () => boolean, stop: () => Promise<void>};

// name: a database name suffix, so parallel users in one worktree do not share a database.
export async function startCoordinator(o: {name?: string, env?: Record<string, string>, people?: string} = {}): Promise<Coordinator> {
  if (!ready()) {
    if (!existsSync(join(pgdata, 'PG_VERSION'))) { mkdirSync(pgdata, {recursive: true}); sh('initdb', ['-D', pgdata, '-U', 'postgres', '-A', 'trust', '-E', 'UTF8', '--no-instructions']); }
    // ponytail: max_connections 400 fits N=4 worktrees with one pool each; raise it for more parallel worktrees.
    sh('pg_ctl', ['-D', pgdata, '-l', join(pgdata, 'server.log'), '-w', '-o', `-k ${pgdata} -p ${pgport} -c listen_addresses= -c max_connections=400`, 'start']);
  }
  // deploy/infrastructure/fleet-postgres.sql itself, with only the database name changed (roles are per server).
  const db = 'fleet_trial_' + basename(root.replace(/\/$/, '')).replace(/\W/g, '_').toLowerCase().slice(0, 24) + '_' + createHash('sha256').update(root + (o.name ?? '')).digest('hex').slice(0, 6) + (o.name ? '_' + o.name.replace(/\W/g, '_').slice(0, 10) : '');
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
  const port = await freePort(), secret = randomBytes(24).toString('hex'), base = `http://127.0.0.1:${port}`;
  const server = spawn(process.env.FLEET_TRIAL_SERVER || 'go/bin/fleet-coordinator', {cwd: root, shell: true, detached: true, stdio: ['ignore', 'inherit', 'inherit'], env: {...process.env,
    PORT: String(port), FLEET_DATABASE_URL: url('fleet_app'), FLEET_MIGRATION_DATABASE_URL: url('fleet'),
    FLEET_PEOPLE_FILE: people, FLEET_BROWSER_SECRET: secret, FLEET_HOST_ID: 'trial-host', ...o.env}});
  let exited = false; server.on('exit', () => { exited = true; });
  // A crashed caller must not leave the coordinator running (it holds the caller's stdout open).
  process.on('exit', () => { if (!exited) try { process.kill(-server.pid!, 'SIGKILL'); } catch {} });
  const stop = async () => { if (!exited) { try { process.kill(-server.pid!, 'SIGTERM'); } catch {} for (let i = 0; i < 50 && !exited; i++) await new Promise(r => setTimeout(r, 100)); if (!exited) try { process.kill(-server.pid!, 'SIGKILL'); } catch {} } dropDb(); };
  for (let i = 0; ; i++) {
    if (exited) { dropDb(); throw Error('coordinator exited before it served requests'); }
    try { await fetch(base + '/api/profiles'); break; } catch { if (i > 600) { await stop(); throw Error('coordinator did not start in 60 s'); } await new Promise(r => setTimeout(r, 100)); }
  }
  // Seeds run as the owner role, after the coordinator applied the schema.
  return {base, secret, pid: server.pid!, sql: text => psql(['-d', db, '-U', 'fleet', '-At'], "SET fleet.project_ids = '*';\n" + text), exited: () => exited, stop};
}
