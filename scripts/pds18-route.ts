// Which heavy ci.yml jobs run on the warm PDS18 pool (deploy/infrastructure/pds18-ci/README.md) and which stay hosted.
// Only a merge queue group of main or a push to main may use the pool; .github/workflows/pds18.yml enforces the same.
// Free slots are the pool size less the pool's jobs queued or running in this repository. The pool counts as down when
// the newest successful heartbeat (pds18-heartbeat.yml, every 5 minutes on the pool) is older than BEAT_S, or when any
// pool job, a heartbeat included, has waited for a runner longer than STUCK_S: a dead pool shows within one beat, a
// full one the same way. Down or full: hosted. Concurrent selections see the same free slots (no reservation); the
// overbooked jobs wait for a slot, and --watch bounds that wait.
//   node scripts/pds18-route.ts            GH_TOKEN, GITHUB_REPOSITORY, GITHUB_EVENT_NAME, GITHUB_REF, RUNNER_TEMP
//                                          (test-selection.json); appends to GITHUB_OUTPUT
//   node scripts/pds18-route.ts --watch <n>  this run (GITHUB_RUN_ID) routed n jobs to the pool: exits once all n started;
//                                          cancels the run when one has waited WATCH_S while no pool job runs (the pool
//                                          died after routing; an overbooked pool is running jobs; a lost runner's job
//                                          shows as running until GitHub fails it),
//                                          so the merge queue drops the group now instead of at its timeout
//   node scripts/pds18-route.ts --self-check
import assert from 'node:assert/strict';
import {appendFileSync, readFileSync} from 'node:fs';

// ponytail: the pool size is pool.sh's slot count, written twice; read it from the runners API if a token for it appears.
// BEAT_S is generous: GitHub delays scheduled runs under load; a dead pool shows sooner by its waiting beat (STUCK_S).
const POOL = 4, STUCK_S = 120, BEAT_S = 40 * 60, WATCH_S = 300;
const {GH_TOKEN, GITHUB_REPOSITORY: repo} = process.env;
const api = async (path: string, method = 'GET') => {
  const r = await fetch(`https://api.github.com/repos/${repo}/${path}`, {method, headers: {authorization: `Bearer ${GH_TOKEN}`, accept: 'application/vnd.github+json'}});
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status}`);
  return method === 'GET' ? r.json() as any : null;
};
// Matrix entries per family; ORDER gives the pool's free slots to the slowest first.
export const FAMILIES = {
  go: [{part: 'unit', clickhouse: true}, {part: 'outside-0', clickhouse: true}, {part: 'vet', clickhouse: false}],
  app: [{part: 'app'}],
  browser: [{shard: 0}, {shard: 1}],
  release: [{part: 'release'}],
} as const;
type Family = keyof typeof FAMILIES;
// By hosted duration (2026-11-12 merge queue runs): go outside 185 s, release-build 125 s, browser shards about 60 s,
// go vet 57-90 s; app (bundle and UI unit tests, no Go since #407) is the lightest. Go unit stays hosted: the executor's
// MCP owned-dependency tests fail on the guest and pass on the hosted image (TestMCPOwnedDependencyNewSession,
// TestMCPOwnedDependencyJournalRecovery: "owned dependency did not start"); route it once they pass there.
const ORDER: [Family, number][] = [['go', 1], ['release', 0], ['browser', 0], ['browser', 1], ['go', 2], ['app', 0]];
export type Job = {status: string, labels: string[], created_at: string, started_at?: string | null};

export function plan(o: {trusted: boolean, now: number, beat: string | null, jobs: Job[], run: string[]}) {
  const pool = o.jobs.filter(j => j.labels.includes('fleet-pds18') && j.status !== 'completed');
  const stuck = pool.some(j => j.status === 'queued' && o.now - Date.parse(j.created_at) > STUCK_S * 1000);
  const alive = o.beat !== null && o.now - Date.parse(o.beat) < BEAT_S * 1000;
  const free = o.trusted && alive && !stuck ? Math.max(0, POOL - pool.length) : 0;
  const chosen = new Set(ORDER.filter(([f]) => o.run.includes(f)).slice(0, free).map(([f, i]) => `${f}/${i}`));
  const out: Record<string, string> = {};
  for (const f of Object.keys(FAMILIES) as Family[]) {
    const all = FAMILIES[f].map((e, i) => ({e, p: chosen.has(`${f}/${i}`)}));
    out[`${f}_hosted`] = JSON.stringify(all.filter(x => !x.p).map(x => x.e));
    out[`${f}_pds18`] = JSON.stringify(all.filter(x => x.p).map(x => x.e));
  }
  return {out, free, alive, stuck, busy: pool.length};
}

// The jobs of the active runs that can use the pool (merge queue, main pushes, the heartbeat; never pull requests).
async function activeJobs(): Promise<Job[]> {
  // ponytail: one jobs call per active run, first 100 runs per status; page them if more run at once.
  const runs = [...(await api('actions/runs?status=in_progress&per_page=100')).workflow_runs, ...(await api('actions/runs?status=queued&per_page=100')).workflow_runs]
    .filter((r: any) => r.event !== 'pull_request');
  return (await Promise.all(runs.map((r: any) => api(`actions/runs/${r.id}/jobs?per_page=100`)))).flatMap((j: any) => j.jobs);
}

const waited = (own: Job[], now: number) => own.some(j => j.status === 'queued' && now - Date.parse(j.created_at) > WATCH_S * 1000);
export const dead = (own: Job[], all: Job[], now: number) =>
  waited(own, now) && !all.some(j => j.labels.includes('fleet-pds18') && j.status === 'in_progress');

async function watch(n: number) {
  const id = process.env.GITHUB_RUN_ID;
  // Every 30 s; the repository-wide jobs only once one of ours has waited WATCH_S (the token's hourly quota is shared).
  for (const end = Date.now() + 25 * 60_000; Date.now() < end; await new Promise(r => setTimeout(r, 30_000))) {
    try {
      const own: Job[] = (await api(`actions/runs/${id}/jobs?per_page=100`)).jobs.filter((j: Job) => j.labels.includes('fleet-pds18'));
      const started = own.filter(j => j.status !== 'queued').length;
      console.log(`${started}/${n} pool jobs started`);
      if (started >= n) return;
      if (waited(own, Date.now()) && dead(own, await activeJobs(), Date.now())) {
        await api(`actions/runs/${id}/cancel`, 'POST');
        throw Object.assign(new Error(`a pool job waited ${WATCH_S} s while no pool job runs: run cancelled; the waiting heartbeat routes the next runs hosted`), {final: true});
      }
    } catch (e) {
      if ((e as any).final) throw e;
      console.log(`retrying: ${(e as Error).message}`);
    }
  }
  throw new Error('pool jobs not started in 25 minutes');
}

async function main() {
  const {RUNNER_TEMP, GITHUB_EVENT_NAME: event, GITHUB_REF: ref = '', GITHUB_OUTPUT} = process.env;
  const trusted = (event === 'merge_group' && ref.startsWith('refs/heads/gh-readonly-queue/main/')) || (event === 'push' && ref === 'refs/heads/main');
  let result;
  try {
    if (!trusted) throw new Error(`untrusted event ${event} ${ref}`);
    const beat = (await api('actions/workflows/pds18-heartbeat.yml/runs?status=success&per_page=1')).workflow_runs[0]?.updated_at ?? null;
    const jobs = await activeJobs();
    // The families this run executes (test-selection.json, the selection step's plan); release-build runs only in the queue.
    const run = [...JSON.parse(readFileSync(`${RUNNER_TEMP}/test-selection.json`, 'utf8')).executed, ...(event === 'merge_group' ? ['release'] : [])];
    result = plan({trusted, now: Date.now(), beat, jobs, run});
  } catch (e) {
    // Any doubt is hosted.
    console.log(`hosted: ${(e as Error).message}`);
    result = plan({trusted: false, now: 0, beat: null, jobs: [], run: []});
  }
  console.log(JSON.stringify(result));
  if (GITHUB_OUTPUT) appendFileSync(GITHUB_OUTPUT, Object.entries(result.out).map(([k, v]) => `${k}=${v}\n`).join(''));
}

if (process.argv.includes('--self-check')) {
  const now = Date.parse('2026-11-12T12:00:00Z'), ago = (s: number) => new Date(now - s * 1000).toISOString();
  const job = (status: string, s = 10): Job => ({status, labels: ['self-hosted', 'fleet-pds18'], created_at: ago(s)});
  const p = (o: Partial<Parameters<typeof plan>[0]>) => plan({trusted: true, now, beat: ago(60), jobs: [], run: ['go', 'app', 'browser', 'release'], ...o});
  // Idle pool: the four slowest entries.
  assert.deepEqual(p({}).out, {go_hosted: '[{"part":"unit","clickhouse":true},{"part":"vet","clickhouse":false}]', go_pds18: '[{"part":"outside-0","clickhouse":true}]',
    app_hosted: '[{"part":"app"}]', app_pds18: '[]', browser_hosted: '[]', browser_pds18: '[{"shard":0},{"shard":1}]', release_hosted: '[]', release_pds18: '[{"part":"release"}]'});
  assert.equal(p({jobs: [job('in_progress'), job('queued')]}).free, 2);
  assert.equal(p({jobs: [job('completed'), {...job('queued'), labels: ['ubuntu-24.04']}]}).free, 4);
  // Down, stuck, untrusted: all hosted.
  for (const o of [{beat: ago(BEAT_S + 1)}, {beat: null}, {jobs: [job('queued', STUCK_S + 1)]}, {trusted: false}]) {
    const r = p(o);
    assert.equal(r.free, 0);
    assert.ok(Object.entries(r.out).every(([k, v]) => k.endsWith('_hosted') || v === '[]'));
  }
  // Families this run skips take no slot.
  assert.equal(p({run: ['browser']}).out.browser_pds18, '[{"shard":0},{"shard":1}]');
  // A dead pool: the beat after its death waits; the earlier success is still fresh.
  assert.equal(p({beat: ago(BEAT_S - 60), jobs: [job('queued', STUCK_S + 1)]}).free, 0);
  // Watch: overbooked (the pool started a job lately) waits; dead (nothing started for WATCH_S) cancels.
  const own = [job('queued', WATCH_S + 1)];
  assert.equal(dead(own, [...own, {...job('in_progress', 900), started_at: ago(WATCH_S + 60)}], now), false);
  assert.equal(dead(own, [...own, job('completed', 900)], now), true);
  assert.equal(dead([job('queued', 60)], [], now), false);
  console.log('pds18-route self-check ok');
} else if (process.argv[2] === '--watch') await watch(Number(process.argv[3]));
else await main();
