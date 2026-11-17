// The file runner of tests/go/run.sh: each outside test file through tests/trial/run.ts, `jobs` files at once.
//   node tests/go/run-files.ts <jobs> <file>...
// Each file gets its own database (FLEET_TRIAL_NAME), secret and store directories and fake-gateway port, so files that
// run at once share only the PostgreSQL server. A file's output is printed whole when it ends, with its duration; after
// a failed file no new file starts, and the run exits 1 once the running files end, naming the failed ones.
import {spawn} from 'node:child_process';
import {mkdtempSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {basename, join} from 'node:path';
import {freePort, root, startPostgres} from '../harness.ts';

const [jobsArg, ...files] = process.argv.slice(2);
const jobs = Number(jobsArg);
if (!Number.isInteger(jobs) || jobs < 1 || !files.length) { console.error('usage: run-files.ts <jobs ≥ 1> <file>...'); process.exit(64); }
// Once, before any file: two first uses at once would both create the server.
startPostgres();

const failed: string[] = [], took: [number, string][] = [];
const t0 = performance.now();
const one = async (f: string) => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-outside-')), gateway = await freePort(), t = performance.now();
  const out: Buffer[] = [];
  const code = await new Promise<number>(r => {
    const child = spawn(process.execPath, ['tests/trial/run.ts'], {cwd: root, stdio: ['ignore', 'pipe', 'pipe'], env: {...process.env,
      FLEET_TRIAL_TESTS: f, FLEET_TRIAL_NAME: 'file-' + basename(f, '.test.ts'), FLEET_TRIAL_SERVER: process.env.FLEET_TRIAL_SERVER || 'go/bin/fleet-coordinator',
      FLEET_STORE_SECRETS: dir, FLEET_STORE_ENDPOINT_HOSTS: '127.0.0.1', FLEET_DEFAULT_ARTIFACT_LOCATIONS: 'trial-host',
      FLEET_SECRET_DIR: join(dir, 'secret'), FLEET_GATEWAY_DIR: join(dir, 'gateway'), FLEET_GATEWAY_URL: `http://127.0.0.1:${gateway}`,
      FLEET_CONNECTION_TICK_MS: '200', FLEET_PIPELINE_TICK_MS: '200', FLEET_STREAM_PING_MS: '300', FLEET_WATCH_TICK_MS: '200'}});
    child.stdout.on('data', b => out.push(b)); child.stderr.on('data', b => out.push(b));
    child.on('close', c => r(c ?? 1));
  });
  rmSync(dir, {recursive: true, force: true});
  const s = (performance.now() - t) / 1000;
  took.push([s, f]);
  if (code) failed.push(f);
  process.stdout.write(`\n== ${f} ${code ? 'FAILED' : 'passed'} in ${s.toFixed(1)} s\n${Buffer.concat(out).toString()}`);
};
const queue = [...files];
await Promise.all(Array.from({length: Math.min(jobs, files.length)}, async () => {
  for (let f; !failed.length && (f = queue.shift()); ) await one(f);
}));
took.sort((a, b) => b[0] - a[0]);
console.log(`\n${files.length - queue.length} of ${files.length} files, ${jobs} at once, in ${((performance.now() - t0) / 1000).toFixed(1)} s; slowest: ${took.slice(0, 5).map(([s, f]) => `${basename(f)} ${s.toFixed(1)} s`).join(', ')}`);
if (failed.length) { console.log(`failed: ${failed.join(' ')}`); process.exit(1); }
