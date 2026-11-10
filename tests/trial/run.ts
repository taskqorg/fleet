// Trial slice runner (docs/designs/fleet-stack.md#trial-slice-l1). One command: a fresh Postgres database for this worktree
// on one local server, the coordinator under test started against it, the outside tests, teardown. Node standard library
// and the PostgreSQL client programs only.
//
//   node tests/trial/run.ts                     current TypeScript coordinator
//   FLEET_TRIAL_ARM=mixed node tests/trial/run.ts   the Go front door (go/bin/fleet-coordinator) with TypeScript behind it,
//                                                  started by deploy/start-coordinator.sh as a release starts it
//
// Environment (all optional):
//   FLEET_TRIAL_ARM      typescript (default) or mixed; sets the default of FLEET_TRIAL_SERVER.
//   FLEET_TRIAL_SERVER   shell command that starts the coordinator; default per arm.
//   FLEET_TRIAL_PG       postgresql://admin:pass@host:port of a disposable server (CI) instead of the local one below.
//   FLEET_TRIAL_PGDATA   data directory of the shared local server; default ~/.cache/fleet-trial/pg. Created with initdb
//                        (trust authentication, Unix socket only) and started on first use; left running for other worktrees.
//   FLEET_TRIAL_PGPORT   its port number on the socket; default 54329.
//   FLEET_TRIAL_TESTS    test files; default tests/trial/*.test.ts.
// Prints one line `trial-run setup_ms=… tests_ms=… teardown_ms=… total_ms=… server_peak_rss_kb=… result=pass|fail`;
// server_peak_rss_kb is the largest summed resident size of the server's process group, sampled every 100 ms.
import {execFileSync, spawn} from 'node:child_process';
import {createHash, randomBytes} from 'node:crypto';
import {existsSync, mkdirSync, readFileSync, readdirSync} from 'node:fs';
import {createServer} from 'node:net';
import {homedir} from 'node:os';
import {basename, join} from 'node:path';

const t0 = performance.now();
const root = new URL('../../', import.meta.url).pathname;
const pgdata = process.env.FLEET_TRIAL_PGDATA || join(homedir(), '.cache/fleet-trial/pg');
const pgport = process.env.FLEET_TRIAL_PGPORT || '54329';
const sock = pgdata; // the socket lives in the data directory: no system path, no TCP listener
const adminUrl = process.env.FLEET_TRIAL_PG;
const pgArgs = ['-h', sock, '-p', pgport, '-U', 'postgres'];
const withDb = (u: string, name: string, user?: string) => { const x = new URL(u); x.pathname = '/' + name; if (user) { x.username = user; x.password = user; } return x.toString(); };
const dbArgs = (name: string) => adminUrl ? ['-d', withDb(adminUrl, name)] : [...pgArgs, '-d', name];
const sh = (cmd: string, args: string[], input?: string) => execFileSync(cmd, args, {input, encoding: 'utf8', stdio: [input === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe']});
const ready = () => { try { sh('pg_isready', pgArgs); return true; } catch { return false; } };

// 1. The shared local server.
if (!adminUrl && !ready()) {
  if (!existsSync(join(pgdata, 'PG_VERSION'))) { mkdirSync(pgdata, {recursive: true}); sh('initdb', ['-D', pgdata, '-U', 'postgres', '-A', 'trust', '-E', 'UTF8', '--no-instructions']); }
  // ponytail: max_connections 400 fits N=4 worktrees with one pool each; raise it for more parallel worktrees.
  sh('pg_ctl', ['-D', pgdata, '-l', join(pgdata, 'server.log'), '-w', '-o', `-k ${sock} -p ${pgport} -c listen_addresses= -c max_connections=400`, 'start']);
}

// 2. A fresh database for this worktree with the PDS18 roles and grants: deploy/infrastructure/fleet-postgres.sql itself,
// with only the database name changed (roles are per server and shared by every worktree).
const db = 'fleet_trial_' + basename(root.replace(/\/$/, '')).replace(/\W/g, '_').toLowerCase().slice(0, 30) + '_' + createHash('sha256').update(root).digest('hex').slice(0, 6);
const psql = (name: string, args: string[], input?: string) => sh('psql', [...dbArgs(name), '-v', 'ON_ERROR_STOP=1', '-q', ...args], input);
const dropDb = () => psql('postgres', ['-c', `DROP DATABASE IF EXISTS ${db} WITH (FORCE)`]);
dropDb();
const roles = readFileSync(join(root, 'deploy/infrastructure/fleet-postgres.sql'), 'utf8')
  .replace(/DATABASE fleet\b/g, `DATABASE ${db}`).replace(/datname = 'fleet'/, `datname = '${db}'`).replace(/^\\connect fleet$/m, `\\connect ${db}`);
// Two worktrees creating the roles at the same moment: the loser fails on "already exists" and succeeds on the retry.
// \connect inside the file keeps the admin connection's other parameters.
try { psql('postgres', [], roles); } catch { psql('postgres', [], roles); }
// Over TCP the roles log in with a password equal to their name (disposable server only).
if (adminUrl) psql('postgres', ['-c', "ALTER ROLE fleet PASSWORD 'fleet'", '-c', "ALTER ROLE fleet_app PASSWORD 'fleet_app'"]);
const url = (user: string) => adminUrl ? withDb(adminUrl, db, user) : `postgresql://${user}@/${db}?host=${encodeURIComponent(sock)}&port=${pgport}`;

// 3. The coordinator under test, on a free port.
const port = await new Promise<number>(r => { const s = createServer().listen(0, '127.0.0.1', () => { const p = (s.address() as any).port; s.close(() => r(p)); }); });
const secret = randomBytes(24).toString('hex');
const base = `http://127.0.0.1:${port}`;
const arm = process.env.FLEET_TRIAL_ARM || 'typescript';
const servers: Record<string, string> = {typescript: 'node packages/coordinator/server.ts',
  mixed: `FLEET_TS_SERVER='node packages/coordinator/server.ts' FLEET_GO_SERVER=go/bin/fleet-coordinator sh deploy/start-coordinator.sh`};
const serverCmd = process.env.FLEET_TRIAL_SERVER || servers[arm] || (() => { throw Error('FLEET_TRIAL_ARM is typescript or mixed'); })();
const server = spawn(serverCmd, {cwd: root, shell: true, detached: true, stdio: ['ignore', 'inherit', 'inherit'], env: {...process.env,
  PORT: String(port), FLEET_DATABASE_URL: url('fleet_app'), FLEET_MIGRATION_DATABASE_URL: url('fleet'),
  FLEET_PEOPLE_FILE: join(root, 'tests/trial/people.json'), FLEET_BROWSER_SECRET: secret, FLEET_HOST_ID: 'trial-host'}});
let exited = false; server.on('exit', () => { exited = true; });
const stop = async () => { if (exited) return; try { process.kill(-server.pid!, 'SIGTERM'); } catch {} for (let i = 0; i < 50 && !exited; i++) await new Promise(r => setTimeout(r, 100)); if (!exited) try { process.kill(-server.pid!, 'SIGKILL'); } catch {} };
for (let i = 0; ; i++) {
  if (exited) { dropDb(); throw Error('coordinator exited before it served requests'); }
  // 502: the front door is up, the coordinator behind it not yet.
  try { if ((await fetch(base + '/api/profiles')).status !== 502) break; throw 0; } catch { if (i > 600) { await stop(); dropDb(); throw Error('coordinator did not start in 60 s'); } await new Promise(r => setTimeout(r, 100)); }
}
const t1 = performance.now();
let peakRss = 0;
const rss = () => { try { const sum = sh('ps', ['-A', '-o', 'pgid=,rss=']).split('\n').map(l => l.trim().split(/\s+/)).filter(([g]) => Number(g) === server.pid).reduce((a, [, r]) => a + Number(r), 0); peakRss = Math.max(peakRss, sum); } catch {} };
rss(); const sampler = setInterval(rss, 100);

// 4. The outside tests: they get only the base URL and the credentials, never the database.
const files = process.env.FLEET_TRIAL_TESTS?.split(' ') ?? readdirSync(join(root, 'tests/trial')).filter(f => f.endsWith('.test.ts')).map(f => join('tests/trial', f));
const code = await new Promise<number>(r => spawn(process.execPath, ['--test', '--test-concurrency=1', ...files], {cwd: root, stdio: 'inherit',
  env: {...process.env, FLEET_TRIAL_URL: base, FLEET_TRIAL_BROWSER_SECRET: secret}}).on('exit', c => r(c ?? 1)));
const t2 = performance.now();
clearInterval(sampler); rss();

// 5. Teardown.
await stop(); dropDb();
const t3 = performance.now();
console.log(`trial-run arm=${arm} setup_ms=${Math.round(t1 - t0)} tests_ms=${Math.round(t2 - t1)} teardown_ms=${Math.round(t3 - t2)} total_ms=${Math.round(t3 - t0)} server_peak_rss_kb=${peakRss} result=${code ? 'fail' : 'pass'}`);
process.exit(code);
