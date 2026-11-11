// Trial slice runner (docs/designs/fleet-stack-trial.md#trial-slice-l1). One command: a fresh Postgres database for this worktree
// on one local server, the coordinator under test started against it, the outside tests, teardown. Node standard library
// and the PostgreSQL client programs only.
//
//   node tests/trial/run.ts      the Go coordinator (go/bin/fleet-coordinator; FLEET_TRIAL_SERVER for another command)
//
// Environment (all optional):
//   FLEET_TRIAL_SERVER   shell command that starts the coordinator; default go/bin/fleet-coordinator (tests/harness.ts).
//   FLEET_TRIAL_PGDATA   data directory of the shared local server; default ~/.cache/fleet-trial/pg. Created with initdb
//                        (trust authentication, Unix socket only) and started on first use; left running for other worktrees.
//   FLEET_TRIAL_PGPORT   its port number on the socket; default 54329.
//   FLEET_TRIAL_TESTS    test files; default tests/trial/*.test.ts.
// Prints one line `trial-run setup_ms=… tests_ms=… teardown_ms=… total_ms=… server_peak_rss_kb=… result=pass|fail`;
// server_peak_rss_kb is the largest summed resident size of the server's process group, sampled every 100 ms.
import {execFileSync, spawn} from 'node:child_process';
import {readdirSync} from 'node:fs';
import {join} from 'node:path';
import {root, startCoordinator} from '../harness.ts';

const t0 = performance.now();
const sh = (cmd: string, args: string[]) => execFileSync(cmd, args, {encoding: 'utf8'});
const c = await startCoordinator();
const {base, secret} = c, server = {pid: c.pid};

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
await c.stop();
const t3 = performance.now();
console.log(`trial-run setup_ms=${Math.round(t1 - t0)} tests_ms=${Math.round(t2 - t1)} teardown_ms=${Math.round(t3 - t2)} total_ms=${Math.round(t3 - t0)} server_peak_rss_kb=${peakRss} result=${code ? 'fail' : 'pass'}`);
process.exit(code);
